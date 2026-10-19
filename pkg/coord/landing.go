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
 * The landing side of an execution instruction: the six
 * re-checks a landing host performs before it executes anything, and the state
 * discriminator that says what "executing" means for one VM.
 *
 * Everything here is a pure function of already observed inputs: the caller
 * feeds it what it read (the lease, the forward evidence, the domain
 * state) and gets back either "ok" or the refusal code to report, and - for the
 * Failed row - the answer to "was that VM running when its owner died". Keeping
 * it pure is what makes every refusal constructible in a unit test, which is the
 * acceptance the slice plan asks for.
 */
package coord

import (
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/verdict"
)

/*
 * The mask deadline: while a takeover is in flight nobody may decide that VM on
 * their own. Decision of 2026-10-04: it is MASK_WINDOW_MS, and the handover window equals
 * MASK_WINDOW_MS too (MASK_WINDOW_MS = HANDOFF_DETECT_MS + DEAD_CONFIRM_MS + 10 s), so
 * both use the one derived value T_COORD_HANDOFF_MS - 30 + 140 + 10 = 180 s at
 * the 10/60 tier, where DEAD_CONFIRM_MS = 8 x io_timeout + watchdog_fire_timeout comes
 * from the effective tier read at startup (coord.go, sanlock_tier_window.go).
 */
func Mask_deadline_ms(now_ms int64) int64 {
	return now_ms + T_COORD_HANDOFF_MS
}

/* Refusal codes of the design, plus the two this slice adds. */
const (
	REFUSE_STALE_EPOCH = "stale-epoch"
	REFUSE_STALE_MASK = "stale-mask"
	REFUSE_GATE_HELD = "gate-held"
	REFUSE_NO_FORWARD_EVIDENCE = "no-forward-evidence"
	REFUSE_CAPACITY = "capacity"
	REFUSE_ALREADY_RUNNING = "already-running"
	/* not a landing re-check but the same shape: a third party holds the lease */
	REFUSE_THIRD_PARTY = "third-party-holds"
)

/*
 * ROW_FAILED is the one row of the decision table the lease-side re-checks do
 * not apply to, and it is spelled out here rather than imported for the same
 * reason landing_pick.go spells ROW_RUNNING out: this file stays a pure data
 * question and does not depend on the judgement half's vocabulary.
 *
 * The design says so in its own words (decided 2026-10-01): of the six
 * re-checks, the landing's lease one is "only for the host-is-still-alive path;
 * the Failed row does not do it", because the second confirmation (two DEAD
 * readings, applied by the
 * judgement loop) is that row's authoritative evidence - the victim's leases
 * will not be renewed any more, so asking whether they are free asks the wrong
 * question.
 *
 * What happens when the row is missing (a manual takeover, an instruction from
 * an older coordinator) is deliberately NOT "assume Failed": an unknown input
 * keeps the strict reading, so a gate that is not known to be obsolete stays
 * closed.
 */
const ROW_FAILED = "Failed"

/* lease_evidence_applies says whether the landing's lease re-checks bind for that row. */
/*
 * lease_evidence_applies says whether a row needs the disk leases of the victim's
 * VMs to be free before this host may act. A dead row does not: that host is gone
 * and its leases died with it.
 *
 * An isolated row does not either, and that is what the isolation response is:
 * the victim is alive and still holds the leases of the VMs it runs, and it lets
 * one go only after it has seen the claim. Waiting for those leases here would
 * wait for a claim this very step has to make, and the two would wait for each
 * other for ever. What remains is checked elsewhere and is not weakened: a live
 * third party holding a lease is refused before this test, and starting a domain
 * whose disk lease is still held waits for that lease inside libvirt - which is
 * exactly the order this is meant to have.
 */
func lease_evidence_applies(row string) bool {
	return (row != ROW_FAILED && row != verdict.ROW_ISOLATED)
}

/* Landing_input is everything the landing side observed for one instruction. */
type Landing_input struct {
	/* the instruction carries these two */
	Epoch int64
	Mask_deadline_ms int64
	/* and the row it was issued under, which decides whether the lease checks bind here */
	Row string
	/* what we know ourselves */
	Epoch_known int64
	Now_ms int64
	/* the six re-checks, as observed */
	Lease_free bool		/* every lease of that VM is free */
	Forward_evidence bool	/* the landing's lease evidence: the leases are free, and once it exists the second check - not the landing confirmation; filled in by evidence_from_leases */
	Lease_state string	/* the same lease read, three-valued (verdict.Lease_state): the Failed row's state discriminator */
	Capacity_ok bool	/* the design's capacity check; false until calibrated - today it is CAPACITY_UNCALIBRATED, i.e. vacuously true */
	Already_running bool	/* a domain for it is already running here */
	/* fail fast, do not wait */
	Third_party_holds bool
}

/*
 * Check_landing applies the re-checks in the order the design lists them and
 * returns the first refusal code, or "" when the instruction may be executed.
 * The order matters for the audit trail: an old epoch must be refused before
 * anything else is even looked at.
 *
 * Since 2026-10-06 two of them bind per row, and that is not an accident of the
 * code but the fix of a measured defect. The lease-side pair (gate-held,
 * no-forward-evidence) is the landing's lease evidence for the rows whose owner may
 * still be alive; on the Failed row the lease read is not a gate but the state discriminator
 * (Takeover_state below), so it must not refuse anything there.
 *
 * Measured 2026-10-06 11:16 (h13 was killed while ha-tiny-01 was powered off and
 * leap-15.6 was running): the landing used the lease read as a gate for both - the running
 * VM held its own leases and was refused "gate-held", while the powered-off VM
 * had free leases, was accepted and was started. Both directions were wrong, and
 * the reason is exactly this: a *running* VM is supposed to hold its leases.
 *
 * Refusing on the host-alive rows is unchanged, and the four re-checks that are
 * not lease-side (stale-epoch, stale-mask, third-party-holds, already-running,
 * capacity) bind on every row, the Failed row included: third-party-holds above
 * all, because a live owner is the one thing a dead victim cannot explain away.
 */
func Check_landing(in Landing_input) string {
	if (in.Epoch < in.Epoch_known) {
		return REFUSE_STALE_EPOCH
	}
	if (in.Mask_deadline_ms < in.Now_ms) {
		return REFUSE_STALE_MASK
	}
	if (in.Third_party_holds) {
		/* a live third party holds the lease: fail fast, do not wait */
		return REFUSE_THIRD_PARTY
	}
	if (lease_evidence_applies(in.Row) && !in.Lease_free) {
		return REFUSE_GATE_HELD
	}
	if (in.Already_running) {
		return REFUSE_ALREADY_RUNNING
	}
	if (lease_evidence_applies(in.Row) && !in.Forward_evidence) {
		return REFUSE_NO_FORWARD_EVIDENCE
	}
	if (!in.Capacity_ok) {
		return REFUSE_CAPACITY
	}
	return ""
}

/*
 * What a takeover may do with one VM, as the state discriminator's three
 * answers. Only WAS_RUNNING starts anything.
 */
const (
	WAS_RUNNING = "was-running"
	WAS_POWEROFF = "was-poweroff"
	WAS_UNKNOWN = "was-unknown"
)

/*
 * Takeover_state answers the Failed row's question - was the VM running when its
 * owner died? - from the two independent observations there are:
 *
 *	record - the cluster's own memory of the VM (the inventory's runstate) and
 *	         whether that memory is trustworthy, i.e. whether the inventory knows
 *	         the VM and places it at the victim. If it does, the state it
 *	         remembers is the state the victim last published, which is the state
 *	         it was in when it died.
 *	lease  - the read of that VM's own leases (verdict.Lease_state), which is
 *	         the physical observation: a lease with a non-zero timestamp is held,
 *	         and the only holder a legitimate takeover can still see is the dead
 *	         owner's writer (a live third party was refused before this is
 *	         asked). "Every lease free" therefore means nothing was writing.
 *
 * The rule is "start it only on positive evidence, and let one source veto":
 *
 *	record RUNNING  + lease held or unreadable  => WAS_RUNNING
 *	lease held      + record POWEROFF?          => WAS_POWEROFF  (veto)
 *	record RUNNING  + every lease free          => WAS_POWEROFF  (veto)
 *	record POWEROFF + lease unreadable          => WAS_POWEROFF
 *	neither says anything                       => WAS_UNKNOWN
 *
 * The veto is the deliberate part. A record can be stale (a clean shutdown
 * whose event had not been broadcast when the host died), and so can a held
 * lease (a qemu that was hard-killed leaves its lease held forever - measured
 * 2026-10-06: leap-15.6's resource still read timestamp 399, owner_generation
 * 51, hours after the host was destroyed and after the VM was gone). With two
 * sources disagreeing, the safe direction is the one the user's rule names:
 * never start a VM that was not running - and the coordinator's own
 * expectation, computed by this same function, then ends in a timed-out alert
 * rather than in a silently missed rescue. The two sources agreeing is the
 * normal case, and it is the case the field test of 2026-10-06 is built on.
 */
func Takeover_state(record openapi.Vmrunstate, record_ok bool, lease string) string {
	var (
		rec string = WAS_UNKNOWN
		lea string = WAS_UNKNOWN
	)
	if (record_ok) {
		switch (record) {
		case openapi.RUNSTATE_RUNNING:
			rec = WAS_RUNNING
		case openapi.RUNSTATE_POWEROFF:
			rec = WAS_POWEROFF
		}		/*
		 * Every other remembered state (startup, paused, migrating, crashed,
		 * deleted, none) is neither "it was running" nor "it was off": it is
		 * not trusted as positive evidence, and it is not used as a veto
		 * either, because it does not say the VM was shut off either.
		 */
	}
	switch (lease) {
	case verdict.LEASES_HELD:
		lea = WAS_RUNNING
	case verdict.LEASES_FREE:
		lea = WAS_POWEROFF
	}
	if (rec == WAS_RUNNING && lea != WAS_POWEROFF) {
		return WAS_RUNNING
	}
	if (lea == WAS_RUNNING && rec != WAS_POWEROFF) {
		return WAS_RUNNING
	}
	if (rec == WAS_POWEROFF || lea == WAS_POWEROFF) {
		return WAS_POWEROFF
	}
	return WAS_UNKNOWN
}
