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
 * The isolation response: what a host does with its own running VMs when it has
 * lost sight of every judge address while the action surface is on.
 *
 * Only this host may act here, and it acts on its own disks: a running VM is a VM
 * whose disk leases this host's sanlock daemon keeps renewing, so no other host
 * can take them until this host lets them go. The response is what lets them go -
 * but only when the storage says somebody else has taken the VM over, or when this
 * host cannot read the storage at all. That is the gate below, and it is what
 * keeps a host that is merely out of touch from giving away VMs nobody runs.
 *
 * The clock is this host's own: the response starts ha_isolation_seconds after the
 * first round in which this host saw itself isolated, and a round in which it is
 * not isolated takes the timer and the list back to zero. The wait is also the
 * debounce: a probe that fails once and answers the next round never acts.
 *
 * shut-down is graceful (ACPI) with the documented fallback: a VM that has not
 * stopped after ISOLATION_SHUTDOWN_GRACE_SECONDS is powered off outright.
 */
package hastate

import (
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/policy"
	"suse.com/virtx/pkg/reg"
)

/* ISOLATION_SHUTDOWN_GRACE_SECONDS is how long a graceful stop may take. */
const ISOLATION_SHUTDOWN_GRACE_SECONDS = 300

type isolation_run struct {
	since_ms int64			/* first round that saw this host isolated, 0 = not isolated */
	stopped []string		/* the VMs stopped under the response, while isolated */
	asked_ms map[string]int64	/* when each VM was asked to stop gracefully */
	note map[string]string		/* what was last said about a VM, so a steady state stays quiet */
	waiting_said bool
	offer []string			/* the running VMs this host offers while it is isolated */
	claim_seen map[string]string	/* who held each VM last round, to make a claim stick */
}

/*
 * iso_run is touched by the reporting round only: that is the round which reads
 * the judge, decides, acts, and hands the list to the report.
 */
var iso_run isolation_run

/*
 * isolation_round runs one round of the response and answers the VMs this host
 * has stopped under it, which the report carries so that the hosts that are still
 * in touch know what to take over.
 */
func isolation_round(me string, isolated int, probed int, now_ms int64) []string {
	var (
		p *policy.Policy
		err error
		uuids []string
		uuid string
	)
	if (isolated == 0 || probed == 0) {
		/*
		 * Not isolated, or no probe ran at all. The timer and the list go back to
		 * zero: from here on what was stopped is the storage's business, and the
		 * next isolation starts over.
		 */
		if (iso_run.since_ms != 0 || len(iso_run.stopped) != 0) {
			logger.Log("isolation: no longer isolated (probed=%d isolated=%d): the response and its list are cleared",
			           probed, isolated)
		}
		iso_run.since_ms = 0
		iso_run.stopped = nil
		iso_run.asked_ms = nil
		iso_run.note = nil
		iso_run.claim_seen = nil
		iso_run.waiting_said = false
		return nil
	}
	if (iso_run.since_ms == 0) {
		iso_run.since_ms = now_ms
		iso_run.asked_ms = make(map[string]int64)
		iso_run.note = make(map[string]string)
		iso_run.claim_seen = make(map[string]string)
	}
	/*
	 * The policy of this host, resolved the way the rest of the host resolves it:
	 * the shared file while it can be read, the applied copy otherwise. It carries
	 * the action surface switch as well as the response.
	 */
	p = Published_view().Policy
	if (p == nil) {
		note_once(me, "nopolicy", "the policy of this host cannot be read: no isolation response")
		return iso_run.offer
	}
	if (!p.Ha_enabled) {
		/*
		 * The action surface is off: this cluster does not act. A host must not
		 * stop its VMs because a policy it read earlier still names a response.
		 */
		note_once(me, "disabled", "the action surface is off: no isolation response")
		return iso_run.offer
	}
	if (p.Isolation_response == policy.RESPONSE_LEAVE_ON) {
		note_once(me, "leave-on", "the isolation response is leave-on: the VMs stay as they are")
		return iso_run.offer
	}
	/*
	 * What this host offers is what it runs: the cluster claims from that list, and
	 * this host stops a VM only once that VM has been claimed. Publishing the offer
	 * starts now, before the clock runs out, because the claim has to come first.
	 */
	/*
	 * The offer accumulates: a VM that has been offered stays offered until this
	 * isolation ends, even after a claim takes its registration away - that is the
	 * very moment this host has to act on it. Recomputed from the registrations it
	 * would shrink to nothing exactly when it is needed.
	 */
	iso_run.offer = append(iso_run.offer, offer_only(iso_run.offer, running_vms(me))...)
	offer_prune(me)
	if (now_ms - iso_run.since_ms < int64(p.Isolation_seconds) * 1000) {
		if (!iso_run.waiting_said) {
			logger.Log("isolation: this host is isolated; the response (%s) starts %d s after the first such round, and the VMs it gives up are started one at a time, %d s apart",
			           p.Isolation_response, p.Isolation_seconds, p.Isolation_start_interval())
			iso_run.waiting_said = true
		}
		return iso_run.offer
	}
	uuids, err = reg.Uuids(me)
	if (err != nil) {
		note_once(me, "novms", "the VMs of this host cannot be listed: going by what it offered")
		uuids = nil
	}
	/*
	 * The VMs to look at are the ones this host runs and offered, not only the
	 * ones still registered here. A claim moves the registration to the host that
	 * took the VM over, and that is exactly the signal this host is waiting for:
	 * the domain is still here, defined and running, and stopping it needs its
	 * uuid - not its registration.
	 */
	uuids = append(uuids, offer_only(uuids, iso_run.offer)...)
	for _, uuid = range uuids {
		iso_one(me, uuid, p, now_ms)
	}
	return iso_run.offer
}

/* iso_one decides and acts for one VM of this host. */
func iso_one(me string, uuid string, p *policy.Policy, now_ms int64) {
	var (
		owner string
		reachable bool
		err error
		asked int64
		have bool
	)
	if (iso_stopped(uuid)) {
		return
	}
	reachable = shared_tree_readable()
	owner, err = reg.Owner(uuid)
	if (!reachable || err != nil) {
		/*
		 * Nobody may stop a VM that has not been claimed, and a host that cannot
		 * read the tree cannot learn about a claim: it keeps everything and says so.
		 */
		note_once(uuid, "noclaim", "the registration tree cannot be read: keeping the VM, nobody has claimed it here")
		return
	}
	if (owner != "" && owner != me) {
		/*
		 * A claim must be seen twice before a VM is stopped: a move that is undone
		 * - a dry run, a rollback - is visible for one round, and stopping a VM
		 * over a claim that no longer exists is exactly what must not happen.
		 */
		if (iso_run.claim_seen[uuid] != owner) {
			iso_run.claim_seen[uuid] = owner
			note_once(uuid, "claiming", "the registration moved to " + owner + " once: waiting one more round before stopping it")
			return
		}
		logger.Log("isolation: %s is registered under %s now: it has been claimed, stopping it here", uuid, owner)
	} else {
		/* the registration has not moved: this VM stays ours */
		note_once(uuid, "keep", "the VM is still registered here: nobody has claimed it, keeping it")
		return
	}
	if (!vm_running(uuid)) {
		iso_run.stopped = append(iso_run.stopped, uuid)
		return
	}
	if (p.Isolation_response == policy.RESPONSE_SHUT_DOWN) {
		asked, have = iso_run.asked_ms[uuid]
		if (!have) {
			iso_run.asked_ms[uuid] = now_ms
			logger.Log("isolation: asking %s to shut down (ACPI, %d s before it is powered off outright)",
			           uuid, ISOLATION_SHUTDOWN_GRACE_SECONDS)
			err = hypervisor.Shutdown_domain(uuid, 0)
			if (err == nil) {
				return
			}
			logger.Log("isolation: the graceful stop of %s failed (%s): powering it off", uuid, err.Error())
		} else if (now_ms - asked < int64(ISOLATION_SHUTDOWN_GRACE_SECONDS) * 1000) {
			note_once(uuid, "grace", "the graceful stop is still inside its grace period")
			return
		} else {
			logger.Log("isolation: %s did not stop within %d s: powering it off outright",
			           uuid, ISOLATION_SHUTDOWN_GRACE_SECONDS)
		}
	}
	err = hypervisor.Shutdown_domain(uuid, 1)
	if (err != nil) {
		logger.Log("isolation: %s cannot be stopped (%s)", uuid, err.Error())
		return
	}
	iso_run.stopped = append(iso_run.stopped, uuid)
}

/*
 * offer_prune drops the VMs this host has finished with: it acted on a claim (the VM is
 * stopped here) and the registration is no longer its own. A VM that was claimed and is
 * still running here stays offered - that is the moment this host has to stop it, and its
 * uuid must not be lost. A VM that stopped before any claim stays offered too: nobody has
 * taken it over, so it is not finished business - and the offer would otherwise shrink to
 * nothing exactly when it is needed (measured 2026-10-16: an isolated host went on saying
 * it ran a VM that had been taken over and stopped here).
 */
func offer_prune(me string) {
	var (
		kept []string
		uuid string
		owner string
		err error
	)
	for _, uuid = range iso_run.offer {
		if (!iso_stopped(uuid)) {
			kept = append(kept, uuid)
			continue
		}
		owner, err = reg.Owner(uuid)
		if (err != nil || owner == me) {
			kept = append(kept, uuid)
			continue
		}
	}
	iso_run.offer = kept
}

/* offer_only keeps the VMs this host offered that the list does not hold yet. */
func offer_only(known []string, offer []string) []string {
	var (
		uuid string
		seen string
		found bool
		out []string
	)
	for _, uuid = range offer {
		found = false
		for _, seen = range known {
			if (seen == uuid) {
				found = true
				break
			}
		}
		if (!found) {
			out = append(out, uuid)
		}
	}
	return out
}

/* iso_stopped reports whether one VM is already on the list. */
func iso_stopped(uuid string) bool {
	var (
		u string
	)
	for _, u = range iso_run.stopped {
		if (u == uuid) {
			return true
		}
	}
	return false
}

/*
 * running_vms is the list of VMs this host is running right now: what it offers to
 * the cluster while it is isolated, and the only list a claim may be taken from.
 */
func running_vms(me string) []string {
	var (
		uuids []string
		uuid string
		running []string
		err error
	)
	uuids, err = reg.Uuids(me)
	if (err != nil) {
		return nil
	}
	for _, uuid = range uuids {
		if (vm_running(uuid)) {
			running = append(running, uuid)
		}
	}
	return running
}

/* shared_tree_readable answers whether this host can read the registration tree. */
func shared_tree_readable() bool {
	var (
		hosts []string
		err error
	)
	hosts, err = reg.Hosts()
	_ = hosts
	return (err == nil)
}

/* vm_running answers whether the VM runs here; a VM we cannot ask about counts as running. */
func vm_running(uuid string) bool {
	var (
		state openapi.Vmrunstate
		err error
	)
	state, err = hypervisor.Get_runstate(uuid)
	if (err != nil) {
		return true
	}
	return (state == openapi.RUNSTATE_RUNNING)
}

/* note_once says one thing about one VM once, so that a steady state stays quiet. */
func note_once(key string, note string, text string) {
	if (iso_run.note == nil) {
		iso_run.note = make(map[string]string)
	}
	if (iso_run.note[key] == note) {
		return
	}
	iso_run.note[key] = note
	logger.Log("isolation: %s: %s", key, text)
}
