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
 * Observe_landing gathers the inputs Check_landing needs, from the sources the
 * design names. Each field below says where it comes from and, where the design
 * left room, which reading was chosen.
 *
 * What is deliberately NOT wired yet:
 *   - capacity: the design's coefficients are not calibrated in this
 *     environment, so Capacity_ok is set from CAPACITY_UNCALIBRATED, a named
 *     constant with a loud comment. It must be revisited before Slice C, when
 *     an instruction would actually start something.
 *   - the forward evidence it reads beyond the lease state: see evidence_from_leases.
 */
package coord

import (
	"time"

	"suse.com/virtx/pkg/config"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/lockman"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/verdict"
)

/*
 * CAPACITY_UNCALIBRATED: this environment cannot calibrate the admission
 * coefficients the design gives, and the design says so honestly. It is
 * true - "do not block on a number nobody has measured" - which was acceptable
 * while nothing was started. Slice C IS the first real action, so this constant
 * is now a DECLARED BOUNDARY with a consequence chain, not a placeholder:
 *
 *	a landing host with no free capacity still passes both the candidate
 *	filter (landing_pick.go's Meets_R4 / Capacity_ok) and the landing
 *	re-check (Check_landing's capacity), so the `capacity` refusal code is
 *	unreachable
 *	=> the boot at that landing fails
 *	=> keep_reg leaves the registration at that landing
 *	=> this slice has no ladder that would choose another landing
 *	=> the VM does not run, and only a human moves it on.
 *
 * The decision of 2026-10-04 is to accept that for the first action and to make
 * it an explicit precondition of opening the arm switch, not an oversight: the
 * register is the slice C acceptance report of 2026-10-04, and the
 * honest wording of the landing re-checks is "six, of which capacity is
 * vacuously true".
 */
const CAPACITY_UNCALIBRATED = true

/*
 * evidence_from_leases maps the lease read onto the two lease-side re-checks of the rows whose
 * victim may still be alive, avoiding the overlap the design's wording leaves
 * open:
 *
 *	LEASES_FREE - every lease of that VM is free: the gate is open and we have
 *	         evidence we can act on.
 *	LEASES_HELD  - at least one lease is held by a live writer: gate-held, refuse.
 *	         (This is the difference from a *third party* below: here the
 *	         holder may be the old owner still writing.)
 *	LEASES_UNKNOWN  - we cannot read the lease state at all: that is precisely "no
 *	         forward evidence" - we would be guessing, so we refuse.
 *
 * These two answers are read by Check_landing only for the rows where the owner
 * may still be alive. The Failed row does not gate on them at all: since
 * 2026-10-06 the same read is its state discriminator (Takeover_state), and
 * there the three values are kept apart - "held" means it was running, "free"
 * means it was shut off, and "cannot tell" means neither.
 */
func evidence_from_leases(e1 string) (lease_free bool, forward_evidence bool) {
	switch (e1) {
	case verdict.LEASES_FREE:
		return true, true
	case verdict.LEASES_HELD:
		return false, true
	}
	return false, false
}

/*
 * Observe_landing reads everything Check_landing needs for one instruction.
 *
 * The two values the instruction itself carries (epoch and mask deadline) come
 * in from the caller; Epoch_known is read from the coordinator lease's lock-value block,
 * which is the only authoritative place for it (the lock-value block carries
 * the epoch and the mask deadline, nothing else).
 *
 * old_host is where the VM's registration still lives at the time of the check;
 * the lease keys are read from there, because that is where the XML that names
 * them is.
 */
func Observe_landing(vm_uuid string, old_host string, epoch int64, mask_deadline_ms int64) (Landing_input, error) {
	var (
		in Landing_input
		keys []string
		keys_ok bool
		leases []lockman.Resource_lease
		hosts []lockman.Host_lease
		e1 string
		payload string
		pub Published
		decoded bool
		victim_id uint16
		victim_id_ok bool
		err error
	)
	in = Landing_input{
		Epoch: epoch,
		Mask_deadline_ms: mask_deadline_ms,
		Now_ms: time.Now().UnixMilli(),
		Capacity_ok: CAPACITY_UNCALIBRATED,
	}
	/* the authoritative epoch: the coordinator lease's lock-value block */
	payload, err = lockman.Read_lvb(lockman.Get_coord_path(COORD_RESOURCE))
	if (err != nil) {
		return in, err
	}
	pub, decoded = decode(payload)
	if (decoded) {
		in.Epoch_known = pub.Epoch
	}
	/* the lease state of that VM, and whether its disks are free, out of it */
	keys, keys_ok = verdict.Lease_keys(old_host, vm_uuid)
	leases, _, _, err = lockman.Inq_leases(time.Now().Add(
		time.Duration(config.C.Sanlock_cmd_timeout_seconds) * time.Second))
	if (err != nil) {
		return in, err
	}
	e1 = verdict.Lease_state(leases, keys, keys_ok)
	in.Lease_state = e1
	in.Lease_free, in.Forward_evidence = evidence_from_leases(e1)
	/*
	 * A third party: a lease of that VM which some host other than this one and
	 * other than the victim is still renewing. The victim's own hold is not a
	 * third party - a host that lost its judges keeps its VMs and their leases
	 * until it has seen the claim, and handing them over is exactly what this
	 * take-over is for. A live holder that is neither of the two is the
	 * dangerous case, and then we fail fast instead of waiting.
	 */
	if (keys_ok) {
		hosts, err = lockman.Inq_hosts()
		if (err != nil) {
			return in, err
		}
		err = reg.Load_lockid(old_host, &victim_id)
		victim_id_ok = (err == nil)
		in.Third_party_holds = lease_owner_is_live(leases, hosts, keys, victim_id, victim_id_ok)
	}
	/*
	 * already-running: a domain for that VM exists here in a state that is not
	 * poweroff. "Here" is this host, and the cluster's own inventory is the
	 * cheapest trustworthy view of it.
	 */
	in.Already_running = domain_not_poweroff(vm_uuid)
	return in, nil
}

/*
 * observe_old_state answers, for ONE VM of one victim, the question the Failed
 * row turns on since 2026-10-06: was it running when its owner died?
 *
 * The coordinator asks it for the same reason the landing does, and with the
 * same rule (Takeover_state): the answer is what the landing host's own confirmation has to expect for
 * that instruction, and having both sides compute it from the same pure
 * function is what keeps "what we wait for" and "what the landing does" one
 * rule instead of two.
 *
 * The two observations are the two there are, and they are read exactly as the
 * landing reads them (Observe_landing):
 *   - the cluster's own record of the VM: THIS host's inventory, trusted only
 *     when it knows the VM and places it at the victim, because then the state
 *     it remembers is the state the victim last published;
 *   - the VM's own leases, read from the victim's registration (the shared
 *     tree) with the lease read: a lease that is held means a writer held that
 *     disk and never let it go.
 */
func observe_old_state(victim_uuid string, vm_uuid string) string {
	var (
		vminfo inventory.VmInfo
		err error
		record openapi.Vmrunstate
		record_ok bool
		keys []string
		keys_ok bool
		leases []lockman.Resource_lease
		lease string
	)
	vminfo, err = inventory.Get_vminfo(vm_uuid)
	if (err == nil && vminfo.Host == victim_uuid) {
		record = vminfo.Runstate
		record_ok = true
	}
	lease = verdict.LEASES_UNKNOWN
	keys, keys_ok = verdict.Lease_keys(victim_uuid, vm_uuid)
	leases, _, _, err = lockman.Inq_leases(time.Now().Add(
		time.Duration(config.C.Sanlock_cmd_timeout_seconds) * time.Second))
	if (err == nil) {
		lease = verdict.Lease_state(leases, keys, keys_ok)
	}
	logger.Debug("coord: old state vm=%s victim=%s record=%s record_ok=%v lease=%s => %s",
		vm_uuid, victim_uuid, record.String(), record_ok, lease,
		Takeover_state(record, record_ok, lease))
	return Takeover_state(record, record_ok, lease)
}

/*
 * lease_owner_is_live reports whether any resource of that VM is owned by a
 * host_id that is still renewing.
 */
func lease_owner_is_live(leases []lockman.Resource_lease, hosts []lockman.Host_lease, keys []string, victim_id uint16, victim_id_ok bool) bool {
	for _, l := range leases {
		if (!key_in(keys, l.Name) || l.Owner_id == 0) {
			continue
		}
		if (victim_id_ok && l.Owner_id == uint64(victim_id)) {
			/* the host the VM is being taken from: it holds it on purpose */
			continue
		}
		for _, h := range hosts {
			if (h.Id == l.Owner_id && h.State != "DEAD") {
				return true
			}
		}
	}
	return false
}

/* key_in is a small membership test, kept separate so it can be tested. */
func key_in(keys []string, key string) bool {
	for _, k := range keys {
		if (k == key) {
			return true
		}
	}
	return false
}

/*
 * domain_not_poweroff reports whether the cluster's view has a domain for that
 * VM on this host in any state other than poweroff: the already-running
 * re-check. It uses the same source the takeover handler
 * itself uses, so a drill and a real takeover agree on what "already running"
 * means.
 */
func domain_not_poweroff(vm_uuid string) bool {
	var (
		vminfo inventory.VmInfo
		err error
	)
	vminfo, err = inventory.Get_vminfo(vm_uuid)
	if (err != nil) {
		return false
	}
	if (vminfo.Host != machine.Uuid()) {
		return false
	}
	return (vminfo.Runstate != openapi.RUNSTATE_POWEROFF)
}

/* coord_self is used by callers that need this host's own uuid. */
func coord_self() string {
	return machine.Uuid()
}
