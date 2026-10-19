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
package virtx

import (
	"net/http"
	"os"
	"strconv"

	"suse.com/virtx/pkg/coord"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/lockman"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/ts"
	"suse.com/virtx/pkg/verdict"
	"suse.com/virtx/pkg/vmlog"
	"time"
)

/*
 * landing_idem is the landing host's dedup window: the
 * coordinator keeps an idempotency key for its term, and this host remembers
 * the key for one mask window, in memory, never on disk.
 */
var landing_idem = coord.New_idem_window()

/*
 * vm_takeover is the actuator of a MANUAL takeover: it moves a VM that belongs
 * to a host that is no longer active to the requested host, and starts it there.
 * It is also the executor of a decision-layer instruction (below), and that path
 * starts the VM only when it was running when its owner died.
 *
 * It deliberately makes no judgement about whether it is safe to take over:
 *   - the operator is expected to have checked the disk leases first
 *     (for example with virtx-ha-probe.sh can-boot);
 *   - the storage and libvirt remain the arbiters:
 *     if the previous owner's process is gone while its leases have not expired
 *     yet, the boot below does not fail: it waits for the lease to expire
 *     (host_dead) and only then starts the VM. If another LIVE writer still
 *     holds the disk image, the boot fails on qemu's image write lock and the
 *     takeover rolls back (measured 2026-10-03: "Failed to get "write" lock",
 *     the registration is moved back, and no second writer appears either way).
 *     In particular a host that is merely network partitioned still renews its
 *     leases on the shared storage, so the takeover cannot start a second
 *     writer.
 *
 * A VM whose owning host is still active in the cluster is refused, and a
 * takeover is only ever executed by the host that is going to run the VM.
 *
 * What a decision-layer takeover does with the VM is decided by what the VM was
 * doing when its owner died (2026-10-06): a VM that was RUNNING is moved,
 * defined and started - the rescue - and a VM that was POWERED OFF is moved and
 * defined on a live host but never started, because the user's rule is to
 * re-register it, not to power it on. The two observations behind that answer
 * are the cluster's own record of the VM and the VM's own disk leases; the rule
 * and its conflict case are written down in takeover_state_of and
 * coord.Takeover_state. A manual takeover is untouched by this: it starts the
 * VM, as it always did.
 *
 * It is also the executor of a decision-layer instruction:
 * when the instruction carries an epoch, a mask deadline and an attempt number,
 * the six landing re-checks run before anything is touched, a repeat of the
 * same idempotency key inside one mask window is refused without an action
 * (the idempotency row of the design), and a failure then leaves the registration
 * where the move put it instead of moving it back - the landing is
 * the expected placement now. A manual takeover keeps the old behaviour.
 *
 * Because the boot can wait for the old owner's leases (tens of seconds), the
 * request returns 202 as soon as the checks passed, like vm_migrate does, and
 * the outcome is recorded in the VM's log: "virtx log vm <uuid>".
 */
func vm_takeover(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		uuid string
		o openapi.VmRegisterOptions
		vr httpx.Request
		vminfo inventory.VmInfo
		hostinfo inventory.HostInfo
		old_host string
		old_state openapi.Vmrunstate
		record_ok bool
		was string
		lease_state string
		client_ip string
		msg string
		ts_start int64
		dry_run bool
		decision_layer bool
		keep_reg bool
		executing bool
		refusal string
		source_refusal string
		key string
		inv_host string
		inv_ok bool
		registered_under_victim bool
		victim_active bool
		old_host_name string
		epoch_arg, mask_arg, attempt_arg, victim_arg, row_arg string
		epoch, mask int64
		attempt int
	)
	vr, err = httpx.Decode_request_body(r, &o)
	if (err != nil) {
		logger.Log("%s", err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	uuid = r.PathValue("uuid")
	if (uuid == "") {
		http.Error(w, "could not get uuid", http.StatusBadRequest)
		return
	}
	if (o.Host == "") {
		http.Error(w, "no target host given", http.StatusBadRequest)
		return
	}
	/*
	 * A decision-layer instruction carries the epoch, the mask
	 * deadline and the tier's attempt number, which is the third field of the
	 * idempotency key. All of them are read here, before anything else: what the
	 * landing host may do with them - the six re-checks, the idempotency window
	 * and the refusal codes - has to happen before a registration is touched.
	 * A plain manual takeover carries none of them and behaves as it always did.
	 */
	epoch_arg = r.URL.Query().Get("epoch")
	mask_arg = r.URL.Query().Get("mask_deadline_ms")
	attempt_arg = r.URL.Query().Get("attempt")
	/*
	 * victim is the host the VM is being taken from. It is a decision-layer
	 * parameter: only an instruction from the decision layer needs it, because
	 * only that path has to work when the VM's host is dead and therefore
	 * invisible to every live host's inventory (measured 2026-10-05). A
	 * manual takeover does not carry it and is resolved exactly as before.
	 */
	victim_arg = r.URL.Query().Get("victim")
	/*
	 * row is the decision table's row this instruction was issued under. It is
	 * a decision-layer parameter for the same reason epoch and mask are: it
	 * decides whether the landing's lease-side re-checks bind here at all
	 * (pkg/coord.Check_landing). A manual takeover carries none and is treated
	 * as "row unknown", which keeps the strict reading; the design's rule is
	 * that the landing's lease evidence is "only for the host-is-still-alive
	 * path; the Failed row does not do it".
	 */
	row_arg = r.URL.Query().Get("row")
	dry_run = (r.URL.Query().Get("dry_run") == "1")
	decision_layer = (epoch_arg != "" && mask_arg != "")
	if (decision_layer) {
		epoch, err = strconv.ParseInt(epoch_arg, 10, 64)
		if (err != nil) {
			http.Error(w, "bad epoch", http.StatusBadRequest)
			return
		}
		mask, err = strconv.ParseInt(mask_arg, 10, 64)
		if (err != nil) {
			http.Error(w, "bad mask_deadline_ms", http.StatusBadRequest)
			return
		}
	}
	if (attempt_arg != "") {
		var (
			n int64
		)
		n, err = strconv.ParseInt(attempt_arg, 10, 64)
		if (err != nil || n <= 0) {
			http.Error(w, "bad attempt", http.StatusBadRequest)
			return
		}
		attempt = int(n)
	}
	/*
	 * The landing side of the idempotency key: one mask window
	 * of memory, and only a real instruction is deduplicated - a drill leaves
	 * nothing behind and must stay repeatable. The check is deliberately the
	 * first thing after the parameters are parsed: a repeat of a key this host
	 * has already executed must not move a registration, define a domain or
	 * start anything, and it must not depend on the later checks either (after a
	 * successful takeover the registration is no longer under the old host, so
	 * the later checks would fail with an unrelated error).
	 */
	if (decision_layer && !dry_run && attempt > 0) {
		key = coord.Idempotency_key(epoch, victim_arg, uuid, attempt)
		refusal = landing_idem.Decide(key, mask, ts.Now(), domain_running_here(uuid))
		if (refusal != "") {
			logger.Log("vm_takeover: idempotency vm=%s epoch=%d attempt=%d refusal=%q (no action)",
				uuid, epoch, attempt, refusal)
			http.Error(w, refusal, http.StatusUnprocessableEntity)
			return
		}
		/*
		 * The key is held from here to the moment this host really starts to
		 * execute; every other way out - a refusal, or being proxied on - gives
		 * it back, because the window is about a key that was executed.
		 */
		defer func() {
			if (!executing) {
				landing_idem.Release(key)
			}
		}()
	}
	if (http_host_is_remote(o.Host)) {
		/* a takeover has to be executed by the host that will run the VM */
		http_proxy_request(o.Host, w, vr)
		return
	}
	vminfo, err = inventory.Get_vminfo(uuid)
	inv_ok = (err == nil)
	if (inv_ok) {
		inv_host = vminfo.Host
	}
	/*
	 * Where the landing host learns about the VM.
	 *
	 * A manual takeover (no epoch/mask) uses the cluster's own view: the VM has
	 * to be in this host's inventory, because a human is asking for something
	 * the cluster knows about.
	 *
	 * A decision-layer instruction may not rely on that. The VM belongs to a
	 * host that is dead, and a dead host publishes nothing, so no live host has
	 * it in its inventory at all. Measured 2026-10-05 (the third attempt): an
	 * instruction that carried everything the landing needed was answered
	 * "404 unknown uuid" 24 ms later for exactly that reason. The registration
	 * tree is shared (/vms/reg) and the instruction names the victim, so this
	 * path reads the VM from /vms/reg/<victim>/<uuid> instead - and checks that
	 * the victim is a host the instruction may legitimately name.
	 */
	if (decision_layer && victim_arg != "") {
		var (
			hostinfo_victim inventory.HostInfo
		)
		hostinfo_victim, err = inventory.Get_hostinfo(victim_arg)
		if (err == nil) {
			victim_active = (hostinfo_victim.Cstate == openapi.CSTATE_ACTIVE)
		}
		registered_under_victim = (reg.Access(victim_arg, uuid) == nil)
	}
	old_host, source_refusal = placement_of(decision_layer, victim_arg, machine.Uuid(),
		inv_host, inv_ok, registered_under_victim,
		(reg.Access(machine.Uuid(), uuid) == nil), victim_active)
	if (source_refusal != "") {
		logger.Log("vm_takeover: vm=%s victim=%q decision_layer=%v refused: %s",
			uuid, victim_arg, decision_layer, source_refusal)
		if (source_refusal == REFUSE_UNKNOWN_UUID) {
			/* the manual path's answer, word for word as it always was */
			http.Error(w, "unknown uuid", http.StatusNotFound)
			return
		}
		http.Error(w, source_refusal, http.StatusUnprocessableEntity)
		return
	}
	old_state = openapi.RUNSTATE_POWEROFF
	if (inv_ok && inv_host == old_host) {
		/*
		 * The cluster view agrees with the placement we resolved, so keep the
		 * run state it remembers: the failure path restores it, and - since
		 * 2026-10-06 - it is also the trustworthy half of the state
		 * discriminator (takeover_state_of below): a state read from a host
		 * that still owns the VM is the state the victim last published.
		 */
		record_ok = true
		old_state = vminfo.Runstate
	}
	if (old_host == machine.Uuid()) {
		/* the VM is already registered here: nothing to take over */
		httpx.Do_response(w, http.StatusNoContent, nil)
		return
	}
	/*
	 * never touch a VM whose owning host is still alive: it is either still
	 * writing to its disks (then the boot below would wait for it), or we would
	 * be guessing.
	 *
	 * A decision-layer instruction may not demand that this host knows the
	 * victim at all. A host that has been gone long enough is not in any live
	 * host's inventory either - that is exactly the situation measured
	 * in, and refusing there would put the autonomous path back to square one.
	 * What the design relies on is the six re-checks (the gate and the third
	 * party among them), and those read the shared registration tree, not the
	 * inventory. So: refuse when the victim is known and alive, refuse for a
	 * manual takeover when it is unknown (behaviour unchanged), and for an
	 * instruction continue to the re-checks.
	 */
	old_host_name = old_host
	hostinfo, err = inventory.Get_hostinfo(old_host)
	if (err == nil) {
		old_host_name = hostinfo.Name
		if (hostinfo.Cstate == openapi.CSTATE_ACTIVE && !decision_layer) {
		/*
		 * Same rule as the placement guard below: a person taking a VM over by
		 * hand must not do it while the host that owns it is still there. A
		 * decision-layer instruction carries the row that was selected for it,
		 * and those rows exist because that host is not a member to work with -
		 * a gossip view that still reports it alive (the mesh relays it through
		 * the hosts that are still in touch) must not veto the action.
		 */
			http.Error(w, "the owning host is still active", http.StatusUnprocessableEntity)
			return
		}
	} else if (!decision_layer) {
		logger.Log("vm_takeover: no information about the owning host: %s", err.Error())
		http.Error(w, "unknown owning host", http.StatusFailedDependency)
		return
	} else {
		logger.Log("vm_takeover: victim %s is not in this host's inventory (a host that is gone); the six re-checks are the evidence", old_host)
	}
	/* the registration must be where the cluster view says it is */
	err = reg.Access(old_host, uuid)
	if (err != nil) {
		if (os.IsNotExist(err)) {
			http.Error(w, "no registration under the owning host", http.StatusFailedDependency)
		} else {
			logger.Log("vm_takeover: reg.Access failed: %s", err.Error())
			http.Error(w, "failed to check registration", http.StatusInternalServerError)
		}
		return
	}
	client_ip = httpx.Client_ip(r)
	/*
	 * A decision-layer instruction carries the epoch and the
	 * mask deadline it was issued under. When both are present, the landing
	 * host re-checks the six conditions before executing anything and refuses
	 * with the code Check_landing returns; when they are absent this is a plain
	 * manual takeover and behaves exactly as it always did.
	 *
	 * The check runs before the move below, which is what makes the ordering in
	 * the slice plan possible: refuse first, so that a refused instruction never
	 * moves a registration.
	 */
	if (decision_layer) {
		var (
			in coord.Landing_input
		)
		in, err = coord.Observe_landing(uuid, old_host, epoch, mask)
		if (err != nil) {
			logger.Log("vm_takeover: landing check could not read its inputs: %s", err.Error())
			http.Error(w, "landing check failed", http.StatusFailedDependency)
			return
		}
		/*
		 * The row is the instruction's, and it decides whether the lease read is a gate
		 * (the two "host may still be alive" rows) or the state discriminator
		 * (the Failed row). Everything the re-check needs is observed here, so
		 * the row is set on the same struct rather than passed separately.
		 */
		in.Row = row_arg
		lease_state = in.Lease_state
		refusal = coord.Check_landing(in)
		logger.Log("vm_takeover: landing vm=%s row=%s epoch=%d known=%d mask=%d now=%d lease=%s lease_free=%v forward=%v already_running=%v third_party=%v capacity=%v refusal=%q",
			uuid, in.Row, in.Epoch, in.Epoch_known, in.Mask_deadline_ms, in.Now_ms, in.Lease_state,
			in.Lease_free, in.Forward_evidence, in.Already_running, in.Third_party_holds, in.Capacity_ok, refusal)
		if (refusal != "") {
			http.Error(w, refusal, http.StatusUnprocessableEntity)
			return
		}
	}
	/*
	 * The state discriminator (2026-10-06): what this takeover may do with the
	 * VM is decided from what the VM was doing when its owner died, and the
	 * authority for "start it" is a positive reading only - a lease that is
	 * held, or the cluster's own record saying RUNNING with no lease evidence
	 * against it. Everything else ends in a registration move without a start,
	 * and "we could not tell" is logged as such instead of being read as
	 * "powered off": neither a lost rescue nor a spurious start may be silent.
	 *
	 * A manual takeover (no epoch/mask) does not go through this: it is an
	 * operator asking for that VM to be brought up, which is what it has always
	 * done (see takeover_state_of).
	 */
	was = takeover_state_of(decision_layer, old_state, record_ok, lease_state)
	logger.Log("vm_takeover: landing vm=%s victim=%s row=%s record=%s record_ok=%v lease=%s was=%s starts=%v dry_run=%v",
		uuid, old_host, row_arg, old_state.String(), record_ok, lease_state, was,
		takeover_starts(was), dry_run)
	/*
	 * A drill (Slice B of the action plan) is requested with dry_run=1: the
	 * landing does the move and undoes it again, and never defines or starts
	 * anything. It is a query parameter on purpose - the generated model is
	 * not touched.
	 */
	/*
	 * What a failure does with the registration is not the same for the two
	 * kinds of takeover: a decision-layer instruction leaves
	 * it where the move put it - the landing is the expected placement now, and
	 * only a newly chosen landing moves it again - while a manual takeover puts
	 * everything back the way it always did, because nobody has taken
	 * responsibility for that VM's placement.
	 */
	keep_reg = keep_reg_on_failure(decision_layer, dry_run)
	msg = "takeover from " + old_host_name
	if (dry_run) {
		msg = "dry-run takeover from " + old_host_name
	}
	ts_start = ts.Now()
	executing = true
	go func() {
		rescue := decision_layer && r.URL.Query().Get("row") == verdict.ROW_ISOLATED
		vm_takeover_run(uuid, old_host, old_state, old_host_name, client_ip, msg, ts_start, dry_run, keep_reg, was, rescue)
	}()
	httpx.Do_response(w, http.StatusAccepted, nil)
}

/*
 * takeover_state_of is the whole state discriminator of a takeover in one pure
 * function, so that "what did this instruction do with that VM" is a unit test
 * rather than a reading exercise (2026-10-06).
 *
 * A decision-layer instruction (epoch/mask present) asks the two observations of
 * coord.Takeover_state - the cluster's own record of the VM, and the VM's own
 * leases. The answer decides the action: only "it was running" starts a domain.
 *
 * A manual takeover (no instruction) does not ask anything: it is an operator
 * asking for that VM to be brought up, it is executed by the host that will run
 * the VM, and it has always started it. Nothing here changes that path - which
 * is also why the manual path never reads the leases: there is no decision to
 * take from them.
 */
func takeover_state_of(decision_layer bool, record openapi.Vmrunstate, record_ok bool, lease_state string) string {
	if (!decision_layer) {
		return coord.WAS_RUNNING
	}
	return coord.Takeover_state(record, record_ok, lease_state)
}

/*
 * takeover_starts is the one place that turns the state discriminator's answer
 * into the action of the takeover: a VM is started only when it was running.
 * WAS_UNKNOWN is deliberately not a start - a VM nobody could classify is
 * registered on a live host and left alone, and the coordinator's own
 * expectation (which never shortcuts an unknown state) ends in an alert rather
 * than in a silently missed rescue.
 */
func takeover_starts(was string) bool {
	return (was == coord.WAS_RUNNING)
}

/*
 * keep_reg_on_failure is the design's failure rule as one function: a failed
 * decision-layer takeover leaves the registration where the move put it (the
 * landing is the expected placement now, and only a newly chosen landing moves
 * it again), while a manual takeover - and every drill - puts everything back.
 */
func keep_reg_on_failure(decision_layer bool, dry_run bool) bool {
	return (decision_layer && !dry_run)
}

/*
 * The failure codes of the takeover path: they name the step that failed in the
 * audit line and in the oplog. One measured failure left the audit line with a bare "no such VM"
 * from the inventory, which told a reader nothing about which step died.
 */
const (
	TAKEOVER_FAIL_INVENTORY = "inventory-fill-failed"
	TAKEOVER_FAIL_REGISTRATION = "registration-unreadable"
	TAKEOVER_FAIL_DEFINE = "define-failed"
	TAKEOVER_FAIL_BOOT = "boot-failed"
	TAKEOVER_FAIL_ROLLBACK = "rollback-failed"
)

/* takeover_failure is what a failed step reports: a code, and the error after it. */
func takeover_failure(code string, err error) string {
	if (err == nil) {
		return code
	}
	return code + ": " + err.Error()
}

/* the refusal codes of the placement rule, as the landing reports them */
const (
	REFUSE_UNKNOWN_UUID = "unknown-uuid"
	REFUSE_NO_VICTIM = "no-victim"
	REFUSE_VICTIM_IS_ME = "victim-is-me"
	REFUSE_NOT_REGISTERED_THERE = "not-registered-there"
	REFUSE_VICTIM_ACTIVE = "victim-active"
)

/*
 * placement_of is where the landing host decides which host the VM is to be
 * taken from, and it is the whole decision as a pure function: the caller feeds
 * it what it read, and it answers with the victim's uuid or a refusal code.
 *
 * The manual path (decision_layer false) is exactly what it always was: the
 * cluster's inventory has to know the VM, and the host it names is the one we
 * take it from. The decision-layer path may not depend on that - the VM's host
 * is dead, and a dead host publishes nothing, so no live host has it in its
 * inventory (that is the 404 unknown uuid of 2026-10-05) - so it resolves the
 * VM from the victim's own directory in the shared registration tree, and the
 * instruction has to name that victim. Naming one is not enough: the VM has to
 * be registered there (which is what stops an instruction from pointing at a
 * host the VM has nothing to do with), the landing itself cannot be the victim,
 * and a victim the cluster still sees as alive is refused - an instructed
 * takeover may never take a VM away from a host that is running. A victim that
 * is not in the inventory at all is NOT refused here: being gone from every
 * live host's view is the situation this path exists for, and the six
 * re-checks (the gate and the third party among them) are the evidence that
 * matters; the caller logs it.
 */
/* claim_of names the host this VM was claimed from, for the log. */
func claim_of(uuid string) string {
	var (
		from string
		err error
	)
	from, err = reg.Load_claim(machine.Uuid(), uuid)
	if (err != nil) {
		return "an earlier claim"
	}
	return from
}

func placement_of(decision_layer bool, victim string, me string, inv_host string, inv_known bool,
	registered_under_victim bool, registered_here bool, victim_active bool) (string, string) {
	if (!decision_layer) {
		if (!inv_known) {
			return "", REFUSE_UNKNOWN_UUID
		}
		return inv_host, ""
	}
	if (victim == "") {
		return "", REFUSE_NO_VICTIM
	}
	if (victim == me) {
		return "", REFUSE_VICTIM_IS_ME
	}
	if (registered_here && !registered_under_victim) {
		/*
		 * The registration already sits here: this is the second half of a claim
		 * that was made earlier, and there is nothing left to move. The other
		 * gates - the epoch, the mask window, a live third party, the capacity -
		 * stay exactly as they are.
		 *
		 * Read from the shared registration tree and not from the inventory: a
		 * claim moves a directory in that tree and the tree can be read at once,
		 * while the inventory is fed by gossip and may never carry the record at
		 * all - which is what refused a retry with not-registered-there after its
		 * own first attempt had already claimed the VM (measured 2026-10-15).
		 */
		return me, ""
	}
	if (!registered_under_victim) {
		return "", REFUSE_NOT_REGISTERED_THERE
	}
	if (victim_active && !decision_layer) {
		/*
		 * A person taking a VM over by hand must not do it while its host is
		 * still there: that is the guard this is. A decision-layer instruction
		 * is different: the row that produced it is what establishes that this
		 * host is not a member we can work with - it is isolated, it lost its
		 * flows, or it is gone - and a serf view that still calls it alive (the
		 * gossip mesh relays it through the hosts that are still in touch) must
		 * not veto the action the row was selected for.
		 */
		return "", REFUSE_VICTIM_ACTIVE
	}
	return victim, ""
}

/*
 * domain_running_here reports whether THIS host has a RUNNING domain for that
 * VM. It is the landing host's own confirmation, read from the same inventory the
 * takeover handler already uses, and it is what the landing side needs for the
 * idempotency row's case ② ("the key was executed here and the domain is
 * running" = in place).
 */
func domain_running_here(uuid string) bool {
	var (
		vminfo inventory.VmInfo
		err error
	)
	vminfo, err = inventory.Get_vminfo(uuid)
	if (err != nil) {
		return false
	}
	if (vminfo.Host != machine.Uuid()) {
		return false
	}
	return (vminfo.Runstate == openapi.RUNSTATE_RUNNING)
}

/*
 * vm_takeover_run does the actual work, off the request, so that a boot waiting
 * for the old owner's disk leases cannot make the client give up on a takeover
 * that is still going to succeed.
 *
 * was is the state discriminator's answer (takeover_state_of), and it is the one
 * thing that decides whether this is a rescue or a registration:
 *
 *	WAS_RUNNING  - move, define and start, as every takeover always did;
 *	WAS_POWEROFF - move and define, and do NOT start: the VM was shut off when
 *	               its owner died, and the user's rule is "re-register it on a
 *	               live host, do not power it on" (2026-10-06). The domain is
 *	               defined for the same reason the registration is moved - so
 *	               that the VM is registered on a live host in the full sense
 *	               (libvirt knows it, `virsh list --all` shows it shut off, its
 *	               record stays in the cluster's view) - and defining starts
 *	               nothing: Define_domain does not set autostart, and libvirt
 *	               only ever starts a domain on an explicit request;
 *	WAS_UNKNOWN  - the same as WAS_POWEROFF. Nothing is started on a question
 *	               nobody could answer.
 *
 * rescue says the VMs come from a host that is isolated, not dead: that host shut
 * them down itself, as the isolation response it was configured with, and the only
 * reason it did so is that they are to run again here. The rule that a VM found
 * shut off is only registered and not started belongs to an owner that is gone -
 * nobody there decided anything - and it must not keep a rescue from finishing.
 *
 * keep_reg decides what a failure does with the registration: false (a manual
 * takeover, and every drill) puts it back where it was, true (a decision-layer
 * instruction) leaves it here - this host is the expected
 * placement now, and only a newly chosen landing moves it again.
 */
/*
 * LEASE_WAIT_MS bounds the wait for the disk leases of a VM to lose their holder,
 * and LEASE_POLL_MS is how often that is read. The wait is bounded because the
 * attempt above it is bounded too: when it runs out the boot is tried anyway and
 * fails as it always did, which leaves the ladder in charge of the retry.
 */
const LEASE_WAIT_MS = 90000
const LEASE_POLL_MS = 2000

/*
 * LEASE_WAIT_UNKNOWN_MS is how long the start waits when the leases of the VM cannot
 * be read at all: the keys are taken from the registration of the host that ran it,
 * and on a resume that host is not the one named here. Waiting is the safe side - the
 * host that still runs the VM needs a couple of rounds to stop it, and starting
 * before that fails on the disk lock every single time.
 *
 * It has to be at least as long as those rounds take: it was 30 s, and a VM whose
 * host stopped it 32 s after the claim was started one second too early and died on
 * the lock (measured 2026-10-15, two VMs on one isolated host, one of them lost that
 * way while the other succeeded). It is the same bound as the readable case now.
 */
const LEASE_WAIT_UNKNOWN_MS = 90000

/*
 * wait_for_free_leases waits for every disk lease of that VM to have no live
 * holder. It answers whether that happened, and it never starts anything.
 */
func wait_for_free_leases(uuid string, old_host string) bool {
	var (
		deadline int64
		keys []string
		keys_ok bool
		leases []lockman.Resource_lease
		err error
	)
	/*
	 * The keys of a VM's disk leases are derived from its registration, and this
	 * host holds that registration - the claim moved it here. Asking the host that
	 * used to own the VM reads nothing, because its registration directory is gone,
	 * and a wait that reads nothing is blind: it waited its full window while the
	 * VM had already been stopped, and started late (measured 2026-10-15/16). The
	 * keys are the same for whoever holds them, so this asks about itself.
	 */
	keys, keys_ok = verdict.Lease_keys(machine.Uuid(), uuid)
	if (!keys_ok) {
		/*
		 * Nothing to read, so nothing to judge: wait, then let the boot decide. The
		 * claim record this host used to name the other host can be gone by now (a
		 * failed attempt clears it), and answering "not free" at once put the boot
		 * one second after the claim and into the disk lock of a VM its host was
		 * still running (measured 2026-10-15/16, twice).
		 */
		logger.Log("vm_takeover: %s: the leases of the VM cannot be read: waiting %d ms before the boot",
			uuid, LEASE_WAIT_UNKNOWN_MS)
		time.Sleep(LEASE_WAIT_UNKNOWN_MS * time.Millisecond)
		return false
	}
	deadline = ts.Now() + LEASE_WAIT_MS
	for (ts.Now() < deadline) {
		leases, _, _, err = lockman.Inq_leases(time.Now().Add(30 * time.Second))
		if (err == nil && verdict.Lease_state(leases, keys, keys_ok) == verdict.LEASES_FREE) {
			return true
		}
		time.Sleep(LEASE_POLL_MS * time.Millisecond)
	}
	return false
}

func vm_takeover_run(uuid string, old_host string, old_state openapi.Vmrunstate, old_host_name string, client_ip string, msg string, ts_start int64, dry_run bool, keep_reg bool, was string, rescue bool) {
	var (
		err error
		xml, canonical_xml string
		defined bool
	)
	if (old_host == machine.Uuid()) {
		/*
		 * Resuming a claim made earlier: the registration is already here and the
		 * record of it is in the VM's own directory. A second move would fail, and
		 * it would destroy the only evidence that this VM is on its way.
		 */
		logger.Log("vm_takeover: %s is already registered here: resuming the claim from %s", uuid, claim_of(uuid))
	} else {
		/* move the whole per-VM registration directory to this host */
		err = reg.Move(machine.Uuid(), old_host, uuid)
		if (err != nil) {
			logger.Log("vm_takeover: reg.Move failed: %s", err.Error())
			return
		}
		err = reg.Save_claim(machine.Uuid(), uuid, old_host)
		if (err != nil) {
			logger.Log("vm_takeover: cannot record the claim: %s", err.Error())
		}
	}
	if (dry_run) {
		/*
		 * Drill mode (Slice B): the move itself is real - it is the step the
		 * whole ordering exists for - and it is undone again, so a drill
		 * leaves the cluster exactly as it found it. Nothing past this point
		 * may run: no inventory update, no domain definition, no start.
		 */
		/*
		 * The oplog entry is written while the registration is still on this
		 * host: after the move back the directory it writes into belongs to the
		 * old host again, and the write fails (measured 2026-10-04: the dry run
		 * left no oplog line, with "no such file or directory").
		 */
		err = vmlog.StartEnd(uuid, openapi.OpVmRegister, openapi.OPERATION_COMPLETED,
			client_ip, msg, "Dry run: landing accepted, registration moved back.", ts_start)
		if (err != nil) {
			logger.Log("vm_takeover: dry-run: vmlog failed: %s", err.Error())
		}
		err = reg.Move(old_host, machine.Uuid(), uuid)
		if (err != nil) {
			logger.Log("vm_takeover: dry-run: moving the registration back failed: %s", err.Error())
			vm_takeover_fail(uuid, old_host, old_state, defined, client_ip, msg, ts_start,
				takeover_failure(TAKEOVER_FAIL_ROLLBACK, err), keep_reg)
			return
		}
		logger.Log("vm_takeover: dry-run: %s: landing accepted, registration moved back", uuid)
		return
	}
	/*
	 * Bring our own view of the cluster up to date: the VM is here now and it is
	 * not running. Without this, vm_boot() would refuse: it only starts domains
	 * that the inventory reports as POWEROFF or CRASHED, and it would proxy the
	 * request to the old (gone) host.
	 *
	 * This has to work for a VM this host has never seen. The registration was
	 * moved from a host that is gone, and a host that is gone publishes nothing,
	 * so the VM can be in no live host's inventory at all - measured 2026-10-05
	 * (one measured failure): "inventory update failed: no such VM" aborted the takeover after
	 * the move and before the domain was ever defined, which left the cluster
	 * view inconsistent and the autonomous path short of the start.
	 *
	 * Update_vm inserts the VM when it is unknown and updates it otherwise
	 * (inventory.Update_vm), so the entry the boot below needs - this host,
	 * POWEROFF - exists either way. The details (name, custom fields) stay empty
	 * until this host's next snapshot publishes them; nothing on this path reads
	 * them, and the snapshot overwrites them.
	 */
	err = inventory.Update_vm(&inventory.VmInfo{
		VmEvent: inventory.VmEvent{
			Uuid: uuid,
			Host: machine.Uuid(),
			Runstate: openapi.RUNSTATE_POWEROFF,
			Ts: ts.Now(),
		},
	})
	if (err != nil) {
		logger.Log("vm_takeover: inventory fill failed: %s", err.Error())
		vm_takeover_fail(uuid, old_host, old_state, defined, client_ip, msg, ts_start,
			takeover_failure(TAKEOVER_FAIL_INVENTORY, err), keep_reg)
		return
	}
	/* define the domain here, from the registration we just moved */
	xml, err = reg.Load(machine.Uuid(), uuid)
	if (err != nil) {
		logger.Log("vm_takeover: reg.Load failed: %s", err.Error())
		vm_takeover_fail(uuid, old_host, old_state, defined, client_ip, msg, ts_start,
			takeover_failure(TAKEOVER_FAIL_REGISTRATION, err), keep_reg)
		return
	}
	canonical_xml, err = hypervisor.Define_domain(xml)
	if (err != nil) {
		logger.Log("vm_takeover: Define_domain failed: %s", err.Error())
		vm_takeover_fail(uuid, old_host, old_state, defined, client_ip, msg, ts_start,
			takeover_failure(TAKEOVER_FAIL_DEFINE, err), keep_reg)
		return
	}
	defined = true
	if (canonical_xml != xml) {
		/*
		 * libvirt may canonicalize the XML differently from what is stored in
		 * the registry; save it back so the registry matches libvirt. A
		 * failure here is non-fatal, as in vm_register.
		 */
		err = reg.Save(machine.Uuid(), uuid, canonical_xml)
		if (err != nil) {
			logger.Log("vm_takeover: reg.Save failed: %s", err.Error())
		}
	}
	if (!takeover_starts(was) && !rescue) {
		/*
		 * The VM was powered off when its owner died (or nobody could tell):
		 * the instruction was to re-register it on a live host, not to power it
		 * on. The domain is defined above - that is what "registered on a live
		 * host" means here - and the boot below is what must not happen.
		 *
		 * Measured 2026-10-06 11:16: the landing accepted the powered-off
		 * ha-tiny-01 and started it, which is exactly this step being taken; it
		 * is recorded as a completed registration now, so the landing host's own
		 * confirmation can finish on "moved and shut off at the landing" instead
		 * of waiting out the 180 s window.
		 */
		err = vmlog.StartEnd(uuid, openapi.OpVmRegister, openapi.OPERATION_COMPLETED, client_ip, msg,
			"Taken over: registered on this host, not started (" + was + ").", ts_start)
		if (err != nil) {
			logger.Log("vm_takeover: oplog: %s", err.Error())
		}
		logger.Log("vm_takeover: %s: registered on this host and left shut off (%s, action=register-only)",
			uuid, was)
		return
	}
	/*
	 * start it. This is where sanlock and qemu take the disk leases: if the
	 * previous owner still holds them, the boot waits for it to be dead.
	 */
	/*
	 * The leases of this VM are what has to be free before it starts. The host
	 * that ran it keeps them until it has seen the claim and stopped the domain,
	 * and the start does not wait for that by itself: it fails on the disk lock
	 * (measured 2026-10-15 - Boot_domain failed with "Failed to get a write lock"
	 * one second after the claim, on every attempt, because the isolated host
	 * needs a couple of rounds to stop the VM it still runs). Waiting here is the
	 * order the response is defined in: claimed, then stopped by its host, and
	 * only then started.
	 */
	/*
	 * The leases belong to the host that has been running the VM. On a resume the
	 * old_host parameter is this host, so the host to ask about is the one the
	 * claim came from: with the wrong host there are no keys to read, the wait
	 * answers at once and the boot runs into the disk lock as before (measured
	 * 2026-10-15: "free=false after waiting" in the same second, then the lock).
	 */
	lease_free := wait_for_free_leases(uuid, old_host)
	logger.Log("vm_takeover: %s: the leases of the VM are free=%v after waiting", uuid, lease_free)
	_, err = hypervisor.Boot_domain(uuid, &openapi.VmBootOptions{})
	if (err != nil) {
		logger.Log("vm_takeover: Boot_domain failed: %s", err.Error())
		vm_takeover_fail(uuid, old_host, old_state, defined, client_ip, msg, ts_start,
			takeover_failure(TAKEOVER_FAIL_BOOT, err), keep_reg)
		return
	}
	err = reg.Clear_claim(machine.Uuid(), uuid)
	if (err != nil) {
		logger.Log("vm_takeover: cannot clear the claim record: %s", err.Error())
	}
	err = vmlog.StartEnd(uuid, openapi.OpVmRegister, openapi.OPERATION_COMPLETED, client_ip, msg, "Taken over.", ts_start)
	if (err != nil) {
		logger.Log("vm_takeover: oplog: %s", err.Error())
	}
	logger.Debug("takeover of domain %s from %s successful", uuid, old_host_name)
}

/*
 * vm_takeover_fail records a failed takeover in the VM's log while the VM
 * directory is still on this host, and then puts everything back the way it
 * was: the domain definition (only if we created it here), the per-VM
 * registration directory, and our view of the cluster.
 *
 * keep_reg=true is the decision-layer failure path of the design, and it
 * is deliberately NOT a rollback of the registration: after the move the record
 * IS at the expected placement, the old host may be unreachable, and moving it
 * back would either fail or re-create the two-record state the design forbids.
 * The domain definition is still removed (nothing may half-exist here), and the
 * failure is recorded in the VM's own log, which travelled with the record.
 */
func vm_takeover_fail(uuid string, old_host string, old_state openapi.Vmrunstate, defined bool, client_ip string, msg string, ts_start int64, reason string, keep_reg bool) {
	var err error
	err = vmlog.StartEnd(uuid, openapi.OpVmRegister, openapi.OPERATION_FAILED, client_ip, msg, reason, ts_start)
	if (err != nil) {
		logger.Log("vm_takeover: oplog: %s", err.Error())
	}
	if (defined) {
		err = hypervisor.Undefine_domain(uuid)
		if (err != nil) {
			logger.Log("vm_takeover: rollback: Undefine_domain failed: %s", err.Error())
		}
	}
	if (keep_reg) {
		logger.Log("vm_takeover: %s: the registration stays on this host (the expected placement); only a newly chosen landing moves it again", uuid)
		return
	}
	err = reg.Move(old_host, machine.Uuid(), uuid)
	if (err != nil) {
		logger.Log("vm_takeover: rollback: reg.Move failed: %s", err.Error())
	}
	err = inventory.Update_vm_state(&inventory.VmEvent{
		Uuid: uuid,
		Host: old_host,
		Runstate: old_state,
		Ts: ts.Now(),
	})
	if (err != nil) {
		logger.Log("vm_takeover: rollback: inventory update failed: %s", err.Error())
	}
}
