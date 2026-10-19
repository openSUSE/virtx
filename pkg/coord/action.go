/*
 * Copyright (c) 2026 SUSE LLC
 *
 * This program is free software; you can redistribute it and/or
 * modify it under the terms of the GNU General Public License
 * as published by the Free Software Foundation; either version 2
 * of the License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, see
 * <https://www.gnu.org/licenses/>
 */

/*
 * The coordinator's action half (Slice C of the action plan): the one path that
 * may really start a VM.
 *
 *	judgement with its evidence complete (pkg/verdict)
 *	  ⇒ this host must be holding the coordinator office
 *	  ⇒ the landing rule: no preemption
 *	  ⇒ the instruction is issued, and it is not a dry run
 *	  ⇒ the landing host's own confirmation: the VM counts as in place only
 *	    once it is running at the landing
 *	  ⇒ on timeout: the failure path, and the registration stays at the landing
 *
 * Three things are deliberately NOT here, and each one is recorded as a deliberate
 * absence in the plan for this slice: the ladder's per-tier re-authorisation and epoch
 * invalidation (it follows the first action; it does not precede it), the
 * backoff BACKOFF_MS (the same), and the second lease check, which is not
 * implemented: the disk leases of a host's VMs being free is the only positive
 * evidence this slice has. Preemption is not missing but forbidden: no candidate means the
 * VM stays pending with an alert.
 *
 * The arm switch is NOT checked here either: it belongs to the caller
 * (pkg/verdict.Gate), so that "off means off" cannot be defeated by a caller
 * that forgets it - the execution half is simply never reached.
 */
package coord

import (
	"fmt"
	"sync"
	"time"

	"suse.com/virtx/pkg/hastate"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/policy"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/ts"
	"suse.com/virtx/pkg/verdict"
	openapi "suse.com/virtx/pkg/model"
)

/*
 * ATTEMPT_SEQ_FIRST is the attempt number of the first tier and the only
 * one this slice issues: the retry ladder supplies the next numbers, and the
 * key's shape - (epoch, vm_uuid, that attempt number) - is what the design's
 * idempotency key requires.
 */
const ATTEMPT_SEQ_FIRST = 1

/* ATTEMPT_SEQ_MAX is the last tier: a take-over is tried at most this many times. */
const ATTEMPT_SEQ_MAX = 3

/*
 * The concurrency limit (the design's "within the concurrency
 * limit") is the configuration key max_inflight, whose documented default is 1:
 * one VM at a time, so that the first real action is a single observable event
 * rather than a burst. It is read at each use, so a configuration change is
 * seen without another wiring step.
 */

/* LANDING_CONFIRM_POLL_MS is how often the landing host's own confirmation is re-read
 * while an attempt is in
 * flight: the judgement loop's own period, which the design calls "the
 * ladder's cadence". */
const LANDING_CONFIRM_POLL_MS = 5000

/*
 * attempt_timeout_ms is how long the coordinator waits for the landing host's own
 * confirmation before it
 * takes the failure path. It is MASK_WINDOW_MS, the same window the landing re-checks
 * the instruction against: past it the instruction is stale everywhere. It is a
 * function of T_COORD_HANDOFF_MS rather than a second constant, so that the two
 * cannot drift apart when the sanlock tier changes.
 *
 * It is a ceiling, not the only way an attempt can end: since 2026-10-06 a
 * register-only instruction (the VM was shut off when its owner died) is
 * finished by its own success criterion long before this window is over, which
 * is what the 180 s wait was never meant for (measured 2026-10-06: the accepted
 * register instruction had nothing to wait for and the old rule timed it out).
 */
/*
 * ATTEMPT_UNREADABLE_TIMEOUT_MS is how long an attempt waits when the landing confirmation cannot
 * be read at all: short, because it says nothing about whether the take-over is
 * still on its way, and a failed start has to be tried again soon.
 */
const ATTEMPT_UNREADABLE_TIMEOUT_MS = 15000

func attempt_timeout_ms() int64 {
	return T_COORD_HANDOFF_MS
}

/* what one reading of the landing host's own confirmation can say */
const (
	C4_RUNNING = "running-at-landing"
	C4_ELSEWHERE = "running-elsewhere"
	C4_NOT_RUNNING = "not-running"
	C4_OFF_AT_LANDING = "off-at-landing"
	C4_UNREADABLE = "unreadable"
)

/*
 * What one instruction is waiting for, decided once, at issue time, from the
 * state discriminator (observe_old_state -> Takeover_state):
 *
 *	WAIT_RUNNING    - the VM was running when its owner died, so success is the
 *	                  old, only criterion: RUNNING at the landing.
 *	WAIT_REGISTERED - the VM was shut off, so what was asked for is the two
 *	                  things the landing was told to do: move the registration
 *	                  and leave the VM off. Success is therefore the registration
 *	                  being at the landing AND the landing confirming the VM is
 *	                  shut off there ("off-at-landing", read from the landing's
 *	                  own published view of its domains).
 */
const (
	WAIT_RUNNING = "running-at-landing"
	WAIT_REGISTERED = "registered-off"
)

/*
 * attempt_expectation is the one place that maps the state discriminator's
 * answer onto what the coordinator waits for.
 *
 * WAS_UNKNOWN deliberately expects RUNNING: the landing does not start a VM it
 * could not classify, so waiting for a RUNNING that will not come ends in the
 * timed-out alert - which is the honest outcome ("we could not tell what that
 * VM was doing, nothing was started, a human should look"). Turning it into a
 * "landed" through the weaker register-only criterion would report a rescue
 * nobody performed.
 */
func attempt_expectation(was string) string {
	switch (was) {
	case WAS_POWEROFF:
		return WAIT_REGISTERED
	}
	return WAIT_RUNNING
}

/* what the coordinator then makes of it */
const (
	ATTEMPT_LANDED = "landed"
	ATTEMPT_IN_FLIGHT = "in-flight"
	ATTEMPT_TIMED_OUT = "timed-out"
)

/*
 * Idempotency_key is the idempotency key of one claim attempt: (epoch, the victim the VM is
 * claimed from, vm_uuid, the tier's attempt number). It is a string only so that it can be
 * logged and compared; the fields are what matters.
 *
 * The victim is in the key, and the design does not ask for it: it reads
 * "epoch, vm_uuid, attempt", which let one key cover two different jobs. Measured
 * 2026-10-16, in one term: a VM was claimed from h13 and landed on h12, then h12 itself was
 * cut off and the same VM had to be claimed from h12 - and the term member answered "this
 * key already landed" every five seconds, so no attempt was ever spent (the driven line
 * said planned=0 pending=1 inflight=0), the registration never moved, and the VM was never
 * rescued. The same VM, in the same term, claimed from another victim is another job; the
 * tier's retry rules still count attempts within one victim.
 */
func Idempotency_key(epoch int64, victim_uuid string, vm_uuid string, attempt int) string {
	return fmt.Sprintf("epoch=%d victim=%s vm=%s attempt=%d", epoch, victim_uuid, vm_uuid, attempt)
}

/* The outcomes a key can have. They are what the audit line reports for a VM
 * that is not planned again: "attempt-refused" says what happened, where
 * "duplicate-key" claimed a repeat that never took place (review F7, confirmed
 * by the first instructed takeover of 2026-10-05: the refused instruction left
 * the key recorded and every later round blamed a duplicate). */
const (
	OUTCOME_ISSUED = "issued"
	OUTCOME_ACCEPTED = "accepted"
	OUTCOME_REFUSED = "refused"
	OUTCOME_UNKNOWN = "unknown"
	OUTCOME_LANDED = "landed"
	OUTCOME_TIMED_OUT = "timed-out"
)

/*
 * Term_memory is the coordinator's in-term memory of issued keys ("the
 * coordinator remembers them for its term") and of what became of
 * each of them.
 *
 * It is deliberately not persisted: a new term re-runs everything - a new
 * epoch starts from an empty memory, so a successor re-runs the judgement
 * instead of inheriting an intention.
 *
 * A key is remembered as soon as it is issued, and its outcome is updated as
 * the attempt progresses, so a later round can say what really happened instead
 * of calling it a duplicate. The mutex is for the watcher goroutine, which
 * records the attempt's end while the judgement loop may be planning the next
 * round.
 */
type Term_memory struct {
	mu sync.Mutex
	epoch int64
	keys map[string]string
}

/*
 * Allow reports whether that key may be issued now, and records it as issued. A
 * new epoch clears the memory first, so the same VM can be attempted again in a
 * new term with the same attempt number - the term is part of the key.
 */
func (m *Term_memory) Allow(epoch int64, key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if (m.keys == nil || m.epoch != epoch) {
		m.epoch = epoch
		m.keys = make(map[string]string)
	}
	_, seen := m.keys[key]
	if (seen) {
		return false
	}
	m.keys[key] = OUTCOME_ISSUED
	return true
}

/* Learn records what happened to a key that was already issued. */
func (m *Term_memory) Learn(key string, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if (m.keys == nil) {
		return
	}
	_, seen := m.keys[key]
	if (!seen) {
		return
	}
	m.keys[key] = outcome
}

/* Outcome is the last thing known about a key ("" when it was never issued). */
func (m *Term_memory) Outcome(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if (m.keys == nil) {
		return ""
	}
	return m.keys[key]
}

/*
 * next_free_attempt walks the tiers above the first one and answers the first
 * tier this term has not used, or 0 when they are all spent. A tier is a separate
 * idempotency key, which is what lets a failed attempt be tried again without ever
 * resending a key that has already been executed.
 */
func next_free_attempt(mem *Term_memory, victim_uuid string, epoch int64, vm_uuid string) int {
	var (
		tier int
	)
	for tier = ATTEMPT_SEQ_FIRST + 1; tier <= ATTEMPT_SEQ_MAX; tier++ {
		/*
		 * Outcome and not Allow: Allow records the key it is asked about, so
		 * asking whether a tier is free would spend it. Only the plan below,
		 * which really issues the attempt, records anything.
		 */
		if (mem.Outcome(Idempotency_key(epoch, victim_uuid, vm_uuid, tier)) == "") {
			return tier
		}
	}
	return 0
}

/* Planned is one VM the coordinator may take over now. */
type Planned struct {
	Vm_uuid string
	Landing string
	Epoch int64
	Attempt int
	Key string
}

/* Pending is one VM that stays pending, with the reason (no
 * preemption, so this is an alert and not a smaller action). */
type Pending struct {
	Vm_uuid string
	Reason string
}

/*
 * Plan_round is the "take the candidates, pick a landing, build the key" step
 * as a pure-ish function: the same inputs and the same term memory give the
 * same plan, and the second call of a round is deduplicated instead of planned
 * twice.
 *
 * budget is how many attempts may be in flight at once (max_inflight); it is
 * applied here so that a victim with many VMs cannot exceed the limit. The
 * expected placement is not wired yet (see landing_candidates), so one landing
 * per round is picked, exactly as the design's deterministic order will do once
 * the placement input exists.
 */
func Plan_round(epoch int64, victim_uuid string, vm_uuids []string, candidates []Landing_candidate, budget int, mem *Term_memory) (planned []Planned, pending []Pending) {
	var (
		landing string = Pick_landing(candidates)
	)
	for _, vm_uuid := range vm_uuids {
		var (
			key string
		)
		if (landing == "") {
			pending = append(pending, Pending{ vm_uuid, "no-landing" })
			continue
		}
		if (len(planned) >= budget) {
			pending = append(pending, Pending{ vm_uuid, "concurrency-limit" })
			continue
		}
		key = Idempotency_key(epoch, victim_uuid, vm_uuid, ATTEMPT_SEQ_FIRST)
		if (!mem.Allow(epoch, key)) {
			/*
			 * The key was already used in this term. That is not a duplicate: it
			 * is an attempt whose outcome we know, so the audit line says what
			 * became of it (attempt-refused, attempt-timed-out, ...).
			 */
			var (
				outcome string = mem.Outcome(key)
			)
			if (outcome == "") {
				outcome = OUTCOME_ISSUED
			}
			/*
			 * One outcome is a retry: the attempt was accepted and the landing confirmation
			 * never showed the VM running. That is what a take-over looks like
			 * when the disk lock of the VM is still held by the host that has
			 * just stopped it - the start fails, and a moment later the same
			 * start succeeds. The next tier is a new idempotency key, so nothing
			 * that was executed is ever sent again; everything else stays put: an
			 * attempt still in flight is waited for, and a refusal or a landed
			 * take-over is not repeated.
			 */
			if (outcome == OUTCOME_TIMED_OUT) {
				attempt := next_free_attempt(mem, victim_uuid, epoch, vm_uuid)
				if (attempt != 0 && mem.Allow(epoch, Idempotency_key(epoch, victim_uuid, vm_uuid, attempt))) {
					planned = append(planned, Planned{
						Vm_uuid: vm_uuid,
						Landing: landing,
						Epoch: epoch,
						Attempt: attempt,
						Key: Idempotency_key(epoch, victim_uuid, vm_uuid, attempt),
					})
					continue
				}
				outcome = "tiers-exhausted"
			}
			pending = append(pending, Pending{ vm_uuid, "attempt-" + outcome })
			continue
		}
		planned = append(planned, Planned{
			Vm_uuid: vm_uuid,
			Landing: landing,
			Epoch: epoch,
			Attempt: ATTEMPT_SEQ_FIRST,
			Key: key,
		})
	}
	return planned, pending
}

/*
 * landing_confirmed_state reads the landing host's own confirmation: the actual
 * runstate of the VM, and only AT THE LANDING counts. A RUNNING domain for the
 * same uuid somewhere else is not "in place" - the design's object is "that
 * VM's domain state on the target host", and a domain elsewhere means the
 * instruction did not do what it was meant to (or something else is writing).
 * A reading we could not make is not evidence either.
 *
 * A shut-off domain AT the landing is its own reading (C4_OFF_AT_LANDING), not
 * C4_NOT_RUNNING: "the landing says it is shut off" is the positive
 * confirmation a register-only instruction waits for, while "we do not see it
 * running" is what a boot is expected to produce on its way. Keeping the two
 * apart is what makes the register-only criterion say what it means.
 */
func landing_confirmed_state(landing_uuid string, host_uuid string, runstate openapi.Vmrunstate, read_err error) string {
	if (read_err != nil) {
		return C4_UNREADABLE
	}
	if (host_uuid != landing_uuid) {
		if (runstate == openapi.RUNSTATE_RUNNING) {
			return C4_ELSEWHERE
		}
		return C4_NOT_RUNNING
	}
	if (runstate == openapi.RUNSTATE_RUNNING) {
		return C4_RUNNING
	}
	if (runstate == openapi.RUNSTATE_POWEROFF) {
		return C4_OFF_AT_LANDING
	}
	return C4_NOT_RUNNING
}

/*
 * attempt_verdict maps one reading of the landing host's own confirmation plus the clock onto the attempt's
 * state. This is a pure function: the instruction's 202 is not success,
 * and what success is depends on what the instruction asked for
 * (attempt_expectation). For a VM that was running it is, as it always was,
 * only RUNNING at the landing; everything else waits until the window is over
 * and then takes the failure path.
 *
 * For a VM that was shut off (WAIT_REGISTERED) success is the weaker pair the
 * instruction asked for: the registration was moved (moved, read by the caller
 * from the shared registration tree - the same authority the move itself used)
 * and the landing says the VM is shut off there. Without it a register-only
 * instruction could only ever end in "timed-out" after 180 s of waiting for a
 * RUNNING that was never requested (measured 2026-10-06).
 *
 * Note the order: RUNNING at the landing lands an attempt under either
 * expectation. A VM someone started where its registration now lives is in
 * place, whatever the instruction expected.
 */
func attempt_verdict(expect string, c4 string, moved bool, started_ms int64, now_ms int64, timeout_ms int64) string {
	if (c4 == C4_RUNNING) {
		return ATTEMPT_LANDED
	}
	if (expect == WAIT_REGISTERED && moved && c4 == C4_OFF_AT_LANDING) {
		return ATTEMPT_LANDED
	}
	if ((now_ms - started_ms) >= timeout_ms) {
		return ATTEMPT_TIMED_OUT
	}
	return ATTEMPT_IN_FLIGHT
}

/* inflight_attempt is one instruction whose outcome is not known yet. */
type inflight_attempt struct {
	vm_uuid string
	victim string
	landing string
	row string
	/*
	 * What the state discriminator said about that VM and what the landing host's own confirmation
	 * therefore waits for; both are decided at issue time and never re-read,
	 * because after the move the registration (and with it the leases' XML)
	 * belongs to the landing.
	 */
	was string
	expect string
	epoch int64
	attempt int
	key string
	started_ms int64
	mask_deadline_ms int64
}

/* the attempts in flight, for the concurrency limit and for the lock-value block's
 * deadline; a goroutine per attempt updates it. */
var inflight struct {
	mu sync.Mutex
	items map[string]inflight_attempt
}

func inflight_add(a inflight_attempt) {
	inflight.mu.Lock()
	if (inflight.items == nil) {
		inflight.items = make(map[string]inflight_attempt)
	}
	inflight.items[a.key] = a
	inflight.mu.Unlock()
}

func inflight_del(key string) {
	inflight.mu.Lock()
	delete(inflight.items, key)
	inflight.mu.Unlock()
}

/* count_inflight is how many attempts are in flight right now. */
func count_inflight() int {
	var (
		n int
	)
	inflight.mu.Lock()
	n = len(inflight.items)
	inflight.mu.Unlock()
	return n
}

/*
 * mask_now is the mask deadline to publish while attempts are in flight: the
 * furthest one, or 0 when nothing is in flight. Two goroutines publish from it:
 * the patrol refreshes it every round, and the judgement loop publishes it as
 * well when it issues an instruction (issue_one) - which is what keeps the
 * lock-value block's second item true to the action that is running.
 */
func mask_now() int64 {
	var (
		max int64
	)
	inflight.mu.Lock()
	for _, a := range inflight.items {
		if (a.mask_deadline_ms > max) {
			max = a.mask_deadline_ms
		}
	}
	inflight.mu.Unlock()
	return max
}

/*
 * landing_candidates turns this round's rows into the landing rule's input.
 * Two of the six filters are implemented honestly and two
 * are not, and the difference is written here rather than left implicit:
 *
 *	- "not the victim" and "that host's row is Running this round" are the
 *	  caller's rows, exactly as the rule asks;
 *	- the ledger freshness is read from the host's own ledger timestamp, and a
 *	  ledger with no timestamp is treated as stale (= excluded), which is what
 *	  the design's filter 4 requires ("a stale reading is treated as unknown, and
 *	  excluded from the judgement");
 *	- "disabled / in maintenance" is NOT implemented (G10) - no host is
 *	  excluded for it, so this filter currently passes everyone;
 *	- the capacity check is CAPACITY_UNCALIBRATED, the named constant of observe.go,
 *	  and the ordering's free-capacity key uses the ledger's available memory
 *	  only (the CPU dimension of the design's "take the tighter one" is not
 *	  wired).
 */
func landing_candidates(r verdict.Round) []Landing_candidate {
	var (
		cands []Landing_candidate
	)
	for _, p := range r.Peers {
		var (
			info inventory.HostInfo
			err error
			age int64 = LEDGER_FRESH_MS + 1
			free int64
		)
		if (p.Host_uuid == r.Victim_uuid) {
			continue
		}
		if (p.Row != ROW_RUNNING) {
			continue
		}
		info, err = inventory.Get_hostinfo(p.Host_uuid)
		if (err == nil) {
			if (info.Ts > 0) {
				age = r.Now_ms - info.Ts
			}
			free = int64(info.Memavail)
		}
		cands = append(cands, Landing_candidate{
			Host_uuid: p.Host_uuid,
			Is_victim: false,
			Disabled: false,
			Row: p.Row,
			Ledger_age_ms: age,
			Meets_R4: CAPACITY_UNCALIBRATED,
			Capacity_ok: CAPACITY_UNCALIBRATED,
			Expected_placement: false,
			Free_capacity: free,
		})
	}
	return cands
}

/*
 * Drive_round is the execution half, installed as pkg/verdict.Drive. It
 * returns one line for the judgement loop's log; its own audit lines carry the
 * vm_uuid, the epoch, the landing, the outcome and the duration.
 */
/*
 * iso_offered_vms keeps the VMs an isolated host says it runs.
 *
 * That host is alive, so its own report is what says which of its VMs it gave up
 * (isolation_stopped_vms). A VM it did not stop is still its own: free leases mean
 * the VM is not running, which is a reason to leave it alone and not a reason to
 * start it somewhere else. A host that is gone says nothing and is not filtered:
 * everything it had is taken over, as it always was.
 */
func iso_offered_vms(victim_uuid string, _ []string) []string {
	var (
		pub hastate.Published
		peer hastate.Peer
		kept []string
	)
	pub = hastate.Published_view()
	for _, peer = range pub.Peers {
		if (peer.Host_uuid != victim_uuid) {
			continue
		}
		/*
		 * The list this host published is the list, and it is used as it stands.
		 * The registration of a VM moves to whoever claimed it, so asking what is
		 * registered under the isolated host answers the wrong question - and an
		 * isolated host normally has other VMs registered too, which is how the
		 * shortcut that once stood here came out empty and the take-over that had
		 * already been started was never carried through (measured 2026-10-15).
		 */
		kept = append(kept, peer.Isolation_running_vms...)
	}
	return kept
}

/* iso_last_start_ms is when a rescue last started a VM, for the start interval. */
var iso_last_start_ms int64

/*
 * iso_claimed_vms answers the VMs this host has already claimed from that victim
 * and not started yet: their record sits in their own registration directory,
 * which travelled with the move, so even a different coordinator finds them and
 * carries the rescue through.
 */
func iso_claimed_vms(victim_uuid string, known []string) []string {
	var (
		uuids []string
		uuid string
		from string
		seen bool
		mine []string
		k string
		err error
	)
	uuids, err = reg.Uuids(machine.Uuid())
	if (err != nil) {
		return nil
	}
	for _, uuid = range uuids {
		from, err = reg.Load_claim(machine.Uuid(), uuid)
		if (err != nil || from != victim_uuid) {
			continue
		}
		seen = false
		for _, k = range known {
			if (k == uuid) {
				seen = true
				break
			}
		}
		if (!seen) {
			mine = append(mine, uuid)
		}
	}
	return mine
}

func Drive_round(r verdict.Round) string {
	var (
		epoch int64
		ok bool
		vm_uuids []string
		err error
		budget int
		planned []Planned
		pending []Pending
	)
	/*
	 * Only the coordinator may authorise. This is a
	 * second gate, independent of the arm switch: a host that does not hold the
	 * coordinator lease does not act even when it is armed.
	 */
	epoch, ok = Term()
	if (!ok) {
		logger.Log("coord: action suppressed host=%s row=%s verdict=%s reason=not-coordinator",
			r.Victim_uuid, r.Row, r.Verdict)
		return "action=not-coordinator"
	}
	/*
	 * The victim is dead, so its VMs are enumerated from the shared registration
	 * tree - asking the host itself is exactly what is impossible here.
	 *
	 * Every VM found there is planned: the per-VM HA-unprotected (`disabled`)
	 * marker does not exist in this tree yet (nothing
	 * writes or reads it), so it cannot filter here, and that is registered as a
	 * gap rather than assumed away.
	 */
	vm_uuids, err = reg.Uuids(r.Victim_uuid)
	if (err != nil) {
		logger.Log("coord: action suppressed host=%s row=%s reason=victim-unreadable error=%s",
			r.Victim_uuid, r.Row, err.Error())
		return "action=victim-unreadable"
	}
	if (r.Row == verdict.ROW_ISOLATED) {
		/*
		 * An isolated host is alive: it is the one that decided what to stop, and
		 * its report says which VMs those were. Only those may be taken over.
		 */
		vm_uuids = iso_offered_vms(r.Victim_uuid, vm_uuids)
		vm_uuids = append(vm_uuids, iso_claimed_vms(r.Victim_uuid, vm_uuids)...)
		logger.Log("coord: action host=%s row=%s vms=%d reason=relaunch-only-self-shut-down-vms",
			r.Victim_uuid, r.Row, len(vm_uuids))
		if (len(vm_uuids) == 0) {
			return "action=suppressed reason=isolated-nothing-stopped"
		}
	}
	budget = max_inflight() - count_inflight()
	if (r.Row == verdict.ROW_ISOLATED) {
		/*
		 * A rescue starts its VMs one at a time, at the pace the policy sets: a
		 * host that takes over another's load should not take all of it in one
		 * breath. What does not fit is not dropped - the next round picks it up
		 * from the claim records and from the list of the isolated host.
		 */
		var (
			ip *policy.Policy
			interval_seconds int
		)
		ip = current_policy()
		interval_seconds = ip.Isolation_start_interval()
		if (r.Now_ms - iso_last_start_ms < int64(interval_seconds) * 1000) {
			logger.Log("coord: action host=%s row=%s result=pending reason=start-interval interval=%ds",
				r.Victim_uuid, r.Row, interval_seconds)
			return "action=pending reason=start-interval"
		}
		if (budget > 1) {
			budget = 1
		}
	}
	if (budget < 1) {
		logger.Log("coord: action host=%s row=%s vms=%d result=pending reason=concurrency-limit inflight=%d",
			r.Victim_uuid, r.Row, len(vm_uuids), count_inflight())
		return fmt.Sprintf("action=pending vms=%d inflight=%d", len(vm_uuids), count_inflight())
	}
	planned, pending = Plan_round(epoch, r.Victim_uuid, vm_uuids, landing_candidates(r), budget, &term_mem)
	for _, p := range pending {
		/*
		 * No preemption, no shrinking of the capacity check, no waiting it out: the VM stays
		 * pending and the alert is the log line.
		 */
		logger.Log("coord: action vm=%s epoch=%d result=pending reason=%s (no preemption)", p.Vm_uuid, epoch, p.Reason)
	}
	for _, p := range planned {
		issue_one(r, p)
	}
	return fmt.Sprintf("action=driven row=%s epoch=%d vms=%d planned=%d pending=%d inflight=%d",
		r.Row, epoch, len(vm_uuids), len(planned), len(pending), count_inflight())
}

/* term_mem is the coordinator's memory for the term it holds the lease in. */
var term_mem Term_memory

/*
 * issue_one publishes the mask deadline, asks the landing host to take the VM
 * over (NOT a dry run), and hands the outcome to the landing side.
 *
 * Before the instruction goes out, this host asks the state discriminator for
 * itself (observe_old_state): the answer decides what the landing side will call
 * success, and - because it is the same pure function the landing applies - it
 * is also the audit line's answer to "what did this instruction intend to do
 * with that VM". The landing still re-reads and decides on its own; nothing
 * here authorises anything.
 *
 * A refusal is an outcome, not a failure of this function: it is logged with
 * its code and stops here. The retry rules of the design bind
 * `stale-*`/`capacity`/`gate-held` to a re-judged, re-authorised attempt - the
 * ladder, which is not this slice - so this slice never re-issues a key
 * it has already used in this term.
 */
func issue_one(r verdict.Round, p Planned) {
	var (
		mask int64
		refusal string
		err error
		ts_start int64
		elapsed int64
		was string
		a inflight_attempt
	)
	ts_start = ts.Now()
	mask = Mask_deadline_ms(ts_start)
	was = observe_old_state(r.Victim_uuid, p.Vm_uuid)
	a = inflight_attempt{
		vm_uuid: p.Vm_uuid,
		victim: r.Victim_uuid,
		landing: p.Landing,
		row: r.Row,
		was: was,
		expect: attempt_expectation(was),
		epoch: p.Epoch,
		attempt: p.Attempt,
		key: p.Key,
		started_ms: ts_start,
		mask_deadline_ms: mask,
	}
	/*
	 * The attempt is registered before the instruction goes out, and the mask
	 * deadline is published before it too: the landing re-checks the deadline,
	 * and the design says it shares its origin with the epoch in the
	 * lock-value block. A failure to accept the instruction takes the attempt out again.
	 */
	inflight_add(a)
	publish(mask_now())
	refusal, err = Issue_takeover(p.Landing, p.Vm_uuid, r.Victim_uuid, r.Row, p.Epoch, mask, p.Attempt, false)
	elapsed = ts.Now() - ts_start
	if (err != nil) {
		inflight_del(p.Key)
		publish(mask_now())
		/*
		 * Remember what really happened, so a later round reports it instead of
		 * blaming a duplicate (review F7 / the first shot of 2026-10-05). A
		 * refusal code means the landing answered; no code at all means the
		 * answer never arrived and the outcome is unknown.
		 */
		if (refusal != "") {
			term_mem.Learn(p.Key, OUTCOME_REFUSED)
		} else {
			term_mem.Learn(p.Key, OUTCOME_UNKNOWN)
		}
		logger.Log("coord: action vm=%s victim=%s epoch=%d key=%q landing=%s row=%s was=%s result=refused refusal=%q elapsed_ms=%d (the key stays used for this term: the retry rules belong to the retry ladder, not to this slice)",
			p.Vm_uuid, r.Victim_uuid, p.Epoch, p.Key, p.Landing, r.Row, was, refusal, elapsed)
		return
	}
	term_mem.Learn(p.Key, OUTCOME_ACCEPTED)
	logger.Log("coord: action vm=%s victim=%s epoch=%d key=%q attempt=%d landing=%s row=%s was=%s expectation=%s result=accepted mask_deadline_ms=%d elapsed_ms=%d (202 is not success: waiting for the landing host's own confirmation)",
		p.Vm_uuid, r.Victim_uuid, p.Epoch, p.Key, p.Attempt, p.Landing, r.Row, was, a.expect, mask, elapsed)
	go watch_attempt(a)
}

/*
 * watch_attempt waits for the landing host's own confirmation, against the criterion
 * the instruction was issued under (attempt_expectation): RUNNING at the landing
 * for a VM that was running, and "the registration is at the landing and the VM
 * is shut off there" for a VM that was shut off. On the failure path
 * the registration stays where the move put it - the landing
 * is the expected placement now - and only a newly chosen landing moves it
 * again; nothing here moves anything back.
 *
 * "The registration is at the landing" is read from the shared registration
 * tree (reg.Access), not from the inventory: the move is a rename in that tree,
 * so that is the authority on where the record is, and it is readable the moment
 * the move is done rather than one snapshot period later.
 */
/*
 * landing_vm_record reads one VM's record out of the landing host's own file in
 * the shared registration tree, falling back to the local inventory.
 */
func landing_vm_record(landing_uuid string, vm_uuid string) (inventory.VmInfo, error) {
	var (
		vms []inventory.VmInfo
		vm inventory.VmInfo
		err error
	)
	_, vms, err = reg.Load_vminfo(landing_uuid)
	if (err != nil) {
		return inventory.Get_vminfo(vm_uuid)
	}
	for _, vm = range vms {
		if (vm.Uuid == vm_uuid) {
			return vm, nil
		}
	}
	return inventory.Get_vminfo(vm_uuid)
}

func watch_attempt(a inflight_attempt) {
	defer inflight_del(a.key)
	for {
		var (
			info inventory.VmInfo
			err error
			moved bool
			c4 string
			state string
			elapsed int64
		)
		/*
		 * Channel 4 is read from the shared registration tree - the record the
		 * landing host keeps for itself - for the same reason the move below is:
		 * that tree is the authority on where the record is, and it can be read
		 * the moment the record is written instead of one gossip period later.
		 * The local inventory is only the fallback.
		 */
		info, err = landing_vm_record(a.landing, a.vm_uuid)
		c4 = landing_confirmed_state(a.landing, info.Host, info.Runstate, err)
		moved = (reg.Access(a.landing, a.vm_uuid) == nil)
		elapsed = ts.Now() - a.started_ms
		/*
		 * An unreadable landing confirmation is not evidence that the take-over is still on
		 * its way, only that we cannot tell. Waiting the whole mask window for it
		 * kept a start that had already failed from being tried again for three
		 * minutes (measured 2026-10-15: result=timed-out c4=unreadable
		 * elapsed_ms=180219), so this waits the short window instead and lets the
		 * ladder move on. A second attempt cannot execute anything twice: the
		 * landing refuses a VM that already runs there.
		 */
		timeout_ms := attempt_timeout_ms()
		if (c4 == C4_UNREADABLE) {
			timeout_ms = ATTEMPT_UNREADABLE_TIMEOUT_MS
		}
		state = attempt_verdict(a.expect, c4, moved, a.started_ms, ts.Now(), timeout_ms)
		if (state == ATTEMPT_LANDED) {
			term_mem.Learn(a.key, OUTCOME_LANDED)
			logger.Log("coord: action vm=%s victim=%s epoch=%d key=%q landing=%s row=%s was=%s expectation=%s result=landed c4=%s moved=%v elapsed_ms=%d",
				a.vm_uuid, a.victim, a.epoch, a.key, a.landing, a.row, a.was, a.expect, c4, moved, elapsed)
			return
		}
		if (state == ATTEMPT_TIMED_OUT) {
			term_mem.Learn(a.key, OUTCOME_TIMED_OUT)
			logger.Log("coord: action vm=%s victim=%s epoch=%d key=%q landing=%s row=%s was=%s expectation=%s result=timed-out c4=%s moved=%v elapsed_ms=%d (the registration stays at the landing; only a newly chosen landing moves it again)",
				a.vm_uuid, a.victim, a.epoch, a.key, a.landing, a.row, a.was, a.expect, c4, moved, elapsed)
			return
		}
		time.Sleep(time.Duration(LANDING_CONFIRM_POLL_MS) * time.Millisecond)
	}
}
