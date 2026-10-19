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
 * Package verdict is the judgement half of the decision layer: it picks the row
 * of the decision table for every known
 * host, maps that row to the verdict values, and logs them.
 *
 * Since Slice C it can also DRIVE a takeover, on one condition and one only:
 * the switch of the action surface (action.go) must be on. That switch is
 * the cluster policy's ha_enabled, read again in every round, so a cluster that
 * has no policy, or one that cannot be read, still runs the shadow loop: it
 * logs the judgement and acts on nothing. When the switch is on, a `restart`
 * row whose evidence is complete is handed to the execution half (pkg/coord,
 * installed with Set_drive), which holds the coordinator lease and is the only
 * part that can move a registration or start a domain.
 *
 * The loop itself is still not authoritative - several hosts run it - because
 * the action is gated twice more on the way out: Gate() (this package) and the
 * coordinator term (pkg/coord). The design's rule that without positive
 * evidence nothing may be restarted is enforced per row in
 * Evidence_ok().
 */
package verdict

import (
	"encoding/xml"
	"fmt"
	"os/exec"
	"time"

	"suse.com/virtx/pkg/config"
	"suse.com/virtx/pkg/hastate"
	"suse.com/virtx/pkg/lockman"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/paths"
	"suse.com/virtx/pkg/policy"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/serfcomm"
	"suse.com/virtx/pkg/ts"

	"libvirt.org/go/libvirtxml"
)

/* the rows of the decision table */
const (
	ROW_RUNNING = "Running"
	ROW_PARTITIONED = "Partitioned"
	ROW_ISOLATED = "Isolated"
	ROW_FAILED = "Failed"
	ROW_AGENT_DOWN = "AgentDown"
	ROW_HOST_ALIVE_STORAGE_LOST = "HostAliveStorageLost"
	ROW_STORAGE_UNREADABLE = "StorageUnreadable"
	ROW_PARTITION_STORAGE_UNREADABLE = "PartitionStorageUnreadable"
	ROW_NONE = "NoRow"
)

/* the verdict values */
const (
	VERDICT_NO_OP = "no-op"
	VERDICT_HOLD = "hold"
	VERDICT_ALERT_ONLY = "alert-only"
	VERDICT_RESTART = "restart"
)

/* the lease-read states, so that "not applicable" cannot be confused with "no evidence" */
const (
	LEASES_UNKNOWN = "n/a"
	LEASES_HELD = "no"
	LEASES_FREE = "yes"
)

/*
 * T_RENEW_IO_MULTIPLIER is the named 2 of RENEW_MS = 2 x io_timeout, which
 * sanlock's timeouts.h defines at its line 19.
 */
const T_RENEW_IO_MULTIPLIER = 2

/*
 * DEAD_CONFIRM_SECONDS is the window of the second confirmation: it requires two
 * observations of a dead lease, at least RENEW_MS apart, with no successful
 * renewal in between - a single DEAD reading is not evidence that the peer is
 * dead (measured 2026-10-04: a host that had just rebooted and was already
 * renewing again read as DEAD).
 *
 * It is derived, not written out: RENEW_MS = 2 x io_timeout, and io_timeout is
 * the effective tier's, read at startup by lockman.Read_tier() (pkg/lockman,
 * sanlock_tier.go) and handed over by Set_tier (sanlock_tier_window.go). At
 * this cluster's 10/60
 * tier that is 2 x 10 = 20 s, which is what the literal used to hold; a cluster
 * on another tier gets 2 x that tier's io_timeout with no code change. This is
 * the only place RENEW_MS is defined.
 */
var DEAD_CONFIRM_SECONDS = dead_confirm_seconds(lockman.Default_tier())

/* dead_confirm_seconds is the one place RENEW_MS is computed from a tier. */
func dead_confirm_seconds(t lockman.Tier) int {
	return T_RENEW_IO_MULTIPLIER * t.Io_timeout_seconds
}

/* dead_obs remembers since when we have been seeing one peer as dead. */
type dead_obs struct {
	since int64
}

/*
 * confirmed_dead implements the *intent* of the second confirmation with the
 * signal that actually works: sanlock's own state for that host_id lease.
 *
 * Measured 2026-10-04 (twice, two independent read paths, 6 s apart): the
 * host_id leader's `timestamp` does NOT advance while the lease is held
 * (89/1142/1856 read exactly the same values), so the criterion as written -
 * "the timestamp age grew by about the same amount" - cannot distinguish a
 * renewing host from a dead one, and using it made live hosts read as dead.
 * That is the same class of defect as the landing criterion of 2026-10-01, and
 * it is registered as an erratum against the design.
 *
 * With the timestamp unusable, the confirmation is: the stderr state reads as
 * DEAD twice, at least RENEW_MS apart, with any other reading (LIVE, FAIL,
 * UNKNOWN, or no reading at all) resetting the window. sanlock's state is what
 * flipped correctly in every field test we have.
 */
func confirmed_dead(obs map[string]dead_obs, uuid string, state string, now int64) bool {
	var (
		o dead_obs
		have bool
	)
	if (state != "DEAD") {
		delete(obs, uuid)
		return false
	}
	o, have = obs[uuid]
	if (!have) {
		obs[uuid] = dead_obs{ since: now }
		return false
	}
	return (now - o.since) >= int64(DEAD_CONFIRM_SECONDS) * 1000
}

/* Columns is what this host observed about one other host. */
type Columns struct {
	Report_fresh bool	/* ① its own report is being written */
	Lease_alive bool	/* ② the host_id lease is LIVE */
	Lease_dead bool		/* ② the host_id lease is DEAD */
	Lease_readable bool	/* ② we could ask sanlock at all */
	Report string		/* ③ fresh-0 / fresh-1 / stale / unreadable / absent */
	Mgmt_known bool		/* ④ we know its management address */
	Mgmt_probed bool	/* ④ and we could ask it at all */
	Mgmt_reachable bool	/* ④ and it answers */
}

/*
 * Select_row implements the five priority rules of the design in order.
 * When no rule matches, ROW_NONE is returned: the design says
 * that case is "no action + alert + audit event", which is what a shadow does.
 *
 * Rule 1 (② unreadable) is deliberately the conservative side for BOTH values
 * of ①: a decision may never be based on storage reads that are themselves
 * failing, and the lease read would have been taken over the same broken path.
 */
func Select_row(c Columns) string {
	/* rule 1: the host_id lease could not be read */
	if (!c.Lease_readable) {
		return ROW_STORAGE_UNREADABLE
	}
	/* rule 2: ② dead */
	if (c.Lease_dead) {
		if (c.Report_fresh || c.Mgmt_reachable) {
			/*
			 * Alive: its report is being written, or it answers us although there
			 * is no report to read - a host that lost the whole shared tree still
			 * answers. Only a host we cannot reach at all may be taken for dead.
			 */
			return ROW_HOST_ALIVE_STORAGE_LOST
		}
		if (c.Report == hastate.REPORT_UNREADABLE) {
			return ROW_PARTITION_STORAGE_UNREADABLE
		}
		if (c.Report == hastate.REPORT_STALE || c.Report == hastate.REPORT_ABSENT) {
			/*
			 * A host whose report stopped and which does not answer is dead; one
			 * that still answers is not. What a dead row drives waits for no lease
			 * evidence, so being alive must keep a host out of it.
			 */
			if (c.Mgmt_known && c.Mgmt_probed && !c.Mgmt_reachable) {
				return ROW_FAILED
			}
			return ROW_NONE
		}
		return ROW_NONE
	}
	/* rule 3: ② alive + we cannot reach it */
	if (!c.Mgmt_reachable) {
		if (!c.Mgmt_known) {
			return ROW_NONE
		}
		if (!c.Mgmt_probed) {
			/* we could not even ask it: that is not evidence of isolation */
			return ROW_NONE
		}
		if (c.Report == hastate.REPORT_FRESH_1) {
			return ROW_ISOLATED
		}
		if (c.Report == hastate.REPORT_FRESH_0) {
			return ROW_PARTITIONED
		}
		return ROW_NONE
	}
	/* rule 4: ② alive + it answers */
	if (c.Report == hastate.REPORT_FRESH_0 || c.Report == hastate.REPORT_FRESH_1) {
		return ROW_RUNNING
	}
	if (c.Report == hastate.REPORT_STALE && c.Mgmt_known && c.Mgmt_reachable) {
		return ROW_AGENT_DOWN
	}
	return ROW_NONE
}

/*
 * Verdict maps a row to the verdict value. restart is only ever a label here:
 * the caller logs it and does nothing. Positive lease evidence is required
 * for the two "host alive" rows, exactly as the design prescribes.
 */
func Verdict(row string, e1 string) string {
	switch (row) {
	case ROW_RUNNING:
		return VERDICT_NO_OP
	case ROW_PARTITIONED:
		return VERDICT_HOLD
	case ROW_AGENT_DOWN:
		return VERDICT_ALERT_ONLY
	case ROW_FAILED:
		return VERDICT_RESTART
	case ROW_ISOLATED:
		/*
		 * An isolated host is alive and still holds the leases of the VMs it
		 * runs: it releases one only once it has seen the claim, so waiting for
		 * the leases here would wait for a claim this step has to make. What is
		 * left to check belongs to the landing: a live third party on the lease
		 * is refused there, and the start waits for the lease itself.
		 */
		return VERDICT_RESTART
	case ROW_HOST_ALIVE_STORAGE_LOST, ROW_PARTITION_STORAGE_UNREADABLE:
		if (e1 == LEASES_FREE) {
			return VERDICT_RESTART
		}
		return VERDICT_HOLD
	case ROW_STORAGE_UNREADABLE:
		return VERDICT_HOLD
	}
	return VERDICT_HOLD
}

/*
 * Leases_free is the landing's lease evidence: every lease of every VM of that host is free.
 *
 * "Free" is judged by `timestamp == 0` alone. The criterion originally also
 * required `owner_id == 0`, but that is not a reachable state: sanlock never
 * clears the owner it recorded when the lease was last written, so every
 * resource created through virtx (storage.Create does a check/create, which
 * writes the creating host's id) keeps a non-zero owner forever - measured on
 * a live VM, on a stopped VM, on a VM that was never started, and again on a
 * freshly rebooted cluster (2026-10-04). Keeping the clause would have closed
 * the positive-evidence path for the two "host alive" rows for good.
 *
 * `timestamp == 0` is a *state* test and it still excludes what the landing must
 * exclude: a lease that is held (including by a writer stuck in D state) has a
 * non-zero timestamp, written when it was acquired (measured: a live third
 * party holder read 43682). This is deliberately NOT the abolished v2 test
 * ("the timestamp stops advancing"), which was true for every held lease.
 *
 * It is the two-valued reading of Lease_state below, and it keeps the semantics
 * the decision layer's positive evidence has always had: only "every lease is
 * free" is evidence; a held lease and a lease we could not read are both "no".
 */
func Leases_free(leases []lockman.Resource_lease, keys []string, keys_ok bool) string {
	if (Lease_state(leases, keys, keys_ok) == LEASES_FREE) {
		return LEASES_FREE
	}
	return LEASES_HELD
}

/*
 * Lease_state is the same read as Leases_free, but three-valued: LEASES_FREE (every lease of
 * that VM is free), LEASES_HELD (at least one of them is held) and LEASES_UNKNOWN (there is
 * nothing to read it from: an unreadable registration, no lease in it, or a
 * lease that is not in the listing at all).
 *
 * The third value exists because "held" and "cannot tell" are the same thing to
 * a *gate* and are two different things to a *state question*: since
 * 2026-10-06 the Failed row uses this read as its state discriminator ("a lease
 * is held => a writer held that disk when the host died => it was running"),
 * and there "we could not read it" must never be mistaken for "held". Leases_free
 * above is what the gate keeps using, so nothing that gates on the lease read changed
 * meaning.
 */
func Lease_state(leases []lockman.Resource_lease, keys []string, keys_ok bool) string {
	if (!keys_ok || len(keys) == 0) {
		/* we could not read every VM's registrations: not evidence */
		return LEASES_UNKNOWN
	}
	for _, key := range keys {
		var found bool
		for _, l := range leases {
			if (l.Name != key) {
				continue
			}
			found = true
			if (l.Timestamp != 0) {
				return LEASES_HELD
			}
		}
		if (!found) {
			/* the lease we must judge was not in the listing */
			return LEASES_UNKNOWN
		}
	}
	return LEASES_FREE
}

/*
 * the piece of the domain XML we need: the leases of one VM. The key is a
 * child element (<lease><lockspace>..</lockspace><key>..</key></lease>), not
 * an attribute - parsing it by hand got this wrong and left the key set empty,
 * which made the lease read unsatisfiable for every host.
 */
type domain_leases struct {
	Devices struct {
		Leases []libvirtxml.DomainLease `xml:"lease"`
	} `xml:"devices"`
}

/*
 * Lease_keys returns the resource keys of one VM, read from its registration
 * XML, and whether the reading worked. "No leases" and "could not read" are
 * kept apart on purpose: the lease read must not be satisfied by a VM we could not read.
 */
func Lease_keys(host_uuid string, vm_uuid string) ([]string, bool) {
	var (
		err error
		xml_str string
	)
	xml_str, err = reg.Load(host_uuid, vm_uuid)
	if (err != nil) {
		logger.Debug("verdict: %s/%s: cannot read the registration: %s", host_uuid, vm_uuid, err.Error())
		return nil, false
	}
	return parse_lease_keys(xml_str)
}

/* parse_lease_keys pulls the lease keys out of one registration XML. */
func parse_lease_keys(xml_str string) ([]string, bool) {
	var (
		err error
		d domain_leases
		keys []string
	)
	err = xml.Unmarshal([]byte(xml_str), &d)
	if (err != nil) {
		logger.Debug("verdict: cannot parse a registration XML: %s", err.Error())
		return nil, false
	}
	for _, l := range d.Devices.Leases {
		if (l.Key != "") {
			keys = append(keys, l.Key)
		}
	}
	return keys, true
}

/* Host_keys returns the lease keys of every VM registered on one host. */
func Host_keys(host_uuid string) ([]string, bool) {
	var (
		err error
		vm_uuids []string
		keys []string
	)
	vm_uuids, err = reg.Uuids(host_uuid)
	if (err != nil) {
		logger.Debug("verdict: %s: cannot list its VMs: %s", host_uuid, err.Error())
		return nil, false
	}
	for _, vm_uuid := range vm_uuids {
		var (
			vm_keys []string
			vm_ok bool
		)
		vm_keys, vm_ok = Lease_keys(host_uuid, vm_uuid)
		if (!vm_ok) {
			return nil, false
		}
		keys = append(keys, vm_keys...)
	}
	return keys, true
}

/*
 * mgmt_reaches pings the management address of a host, like the judge probe does,
 * and answers three things: whether we know the address at all, whether we could
 * ask it, and whether it answered.
 *
 * "Could not ask" is kept apart from "asked and got nothing" for the same reason
 * the judge probe keeps probed apart from isolated: a ping that cannot run on
 * THIS host is not evidence that the peer is unreachable, and unreachable is what
 * selects the isolation and partition rows.
 */
func mgmt_reaches(addr string) (known bool, probed bool, reachable bool) {
	var (
		err error
		replied bool
		ran bool
	)
	if (addr == "") {
		return false, false, false
	}
	err = exec.Command(paths.Get("PING"), "-c", "1", "-W", "1", addr).Run()
	replied, ran = hastate.Ping_result(err)
	return true, ran, replied
}

/* columns_of turns the published observation of one peer into Columns. */
func columns_of(p hastate.Peer) Columns {
	var (
		c Columns
			addr string
		err error
	)
	/*
	 * ④ ping the host's management address. The address the host reports for
	 * itself in its self report is used first: it is on the shared storage, so it
	 * is there for a host that is out of touch, while the serf view of such a host
	 * is exactly what is missing. Only when the report carries no address do we
	 * fall back to our own serf view of the membership, and that is a lookup, not
	 * a judgement: the serf view decides nothing.
	 *
	 * If neither has an address, the column stays unknown and Select_row refuses
	 * to select a row that needs it.
	 */
	addr = p.Mgmt_addr
	if (addr == "") {
		/*
		 * Transitional: a host that still runs a build which does not report an
		 * address of its own is found through its serf member tag. That is a
		 * lookup and never a judgement: the serf view decides nothing here.
		 */
		addr, err = serfcomm.Management_addr(p.Host_uuid)
		if (err != nil) {
			addr = ""
		}
	}
	if (addr != "") {
		c.Mgmt_known, c.Mgmt_probed, c.Mgmt_reachable = mgmt_reaches(addr)
	}
	c.Lease_readable = (p.Host_state != "unknown")
	c.Lease_alive = (p.Host_state == "LIVE")
	c.Lease_dead = (p.Host_state == "DEAD")
	c.Report = p.Report
	c.Report_fresh = (c.Report == hastate.REPORT_FRESH_0 || c.Report == hastate.REPORT_FRESH_1)
	return c
}

/* line_of renders one judgement for the log. */
func line_of(p hastate.Peer, c Columns, row string, verdict string, e1 string, armed bool) string {
	return fmt.Sprintf("host=%s name=%s row=%s verdict=%s armed=%v admission=%s report_fresh=%v lease=%s report=%s mgmt_known=%v mgmt_probed=%v mgmt_reachable=%v",
		p.Host_uuid, p.Name, row, verdict, armed, e1, c.Report_fresh, p.Host_state,
		c.Report, c.Mgmt_known, c.Mgmt_probed, c.Mgmt_reachable)
}

/* Start runs the judgement loop in its own goroutine. */
func Start() {
	go Loop(0)
}

/*
 * switch_on is the switch of the action surface, from the cluster policy of the
 * round the observation was made in. A host that has no policy, or one that
 * could not be read, has no switch: it is off.
 */
func switch_on(pub *hastate.Published) bool {
	if (pub.Policy == nil) {
		return false
	}
	return pub.Policy.Ha_enabled
}

/* armed_text spells out what the switch means, so that the log needs no slice. */
func armed_text(armed bool) string {
	if (armed) {
		return "ON - a restart judgement may drive a takeover"
	}
	return "OFF - a restart judgement is a log line and nothing else"
}

/*
 * Loop logs one judgement per known host per period and, when the cluster
 * policy has the action surface on, drives a takeover for every `restart`
 * judgement whose evidence is complete.
 *
 * With the switch off - the default, and the only setting this slice is
 * deployed with - the loop is what it was: it prints the verdict and changes
 * nothing.
 */
func Loop(seconds int) {
	var (
		me string = machine.Uuid()
		ticker *time.Ticker
		last map[string]string = make(map[string]string)
		last_action map[string]string = make(map[string]string)
		dead map[string]dead_obs = make(map[string]dead_obs)
		armed bool
		was_armed bool
	)
	if (seconds < 1) {
		seconds = policy.DEFAULT_HASTATE_REFRESH_SECONDS
	}
	logger.Debug("verdict: loop starting, period %d s", seconds)
	ticker = time.NewTicker(time.Duration(seconds) * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		var (
			pub hastate.Published
			rows []Peer_row
			restarts []restart_line
			now_ms int64
		)
		pub = hastate.Published_view()
		if (!pub.Ok) {
			continue
		}
		/*
		 * The switch comes from the cluster policy of the round the observation
		 * was made in, and is read again in every round: a cluster that turns
		 * the action surface off does not have to wait for a restart of this
		 * process, and one that cannot read its policy fails closed.
		 */
		armed = switch_on(&pub)
		if (armed != was_armed) {
			logger.Log("verdict: action surface %s", armed_text(armed))
			was_armed = armed
		}
		now_ms = ts.Now()
		for _, peer := range pub.Peers {
			var (
				c Columns
				row string
				verdict string
				e1 string = LEASES_UNKNOWN
				line string
				key string
			)
			c = columns_of(peer)
			row = Select_row(c)
			if (row == ROW_ISOLATED || row == ROW_HOST_ALIVE_STORAGE_LOST || row == ROW_PARTITION_STORAGE_UNREADABLE) {
				var (
					keys []string
					ok bool
					leases []lockman.Resource_lease
					unreadable, skipped int
					err error
					reason string
				)
				/*
				 * The landing reads the leases on demand, not from the
				 * observation snapshot: the design says this read belongs to the
				 * takeover flow, and a gate must never open on a reading that
				 * can be up to hastate_stale_seconds old.
				 */
				leases, unreadable, skipped, err = lockman.Inq_leases(time.Now().Add(
					time.Duration(config.C.Sanlock_cmd_timeout_seconds) * time.Second))
				if (err != nil) {
					e1 = LEASES_HELD
					reason = "lease read failed: " + err.Error()
				} else {
					keys, ok = Host_keys(peer.Host_uuid)
					e1 = Leases_free(leases, keys, ok)
					if (ok && len(keys) == 0) {
						/*
						 * Nothing to judge: the registrations of that host's VMs are not
						 * under it any more (a claim moved them), so its disk leases are
						 * not ours to read. Saying "no" here would put "it still holds the
						 * disks" in front of an operator long after its VMs run elsewhere.
						 */
						e1 = LEASES_UNKNOWN
					}
					if (!ok) {
						reason = "cannot read every registration"
					} else if (len(keys) == 0) {
						reason = "no lease key to judge"
					} else if (e1 != LEASES_FREE) {
						reason = "at least one lease is not free"
					} else {
						reason = "every lease is free"
					}
				}
				/*
				 * Say why, in the log: this branch is the only one that can
				 * authorise a restart, so its evidence must be auditable.
				 */
				logger.Debug("verdict: e1 eval host=%s keys=%d keys_ok=%v e1=%s leases=%d unreadable=%d skipped=%d reason=%s",
					peer.Host_uuid, len(keys), ok, e1, len(leases), unreadable, skipped, reason)
			}
			verdict = Verdict(row, e1)
			/*
			 * a row that leans on "the peer is dead" needs the second
			 * confirmation before it may authorise anything. Read the peer's
			 * host_id state on demand (not from the up-to-30s-old snapshot)
			 * and require it to read DEAD twice; see confirmed_dead() for why
			 * the state, and not the timestamp, is the signal.
			 */
			if ((row == ROW_FAILED || row == ROW_HOST_ALIVE_STORAGE_LOST ||
			     row == ROW_PARTITION_STORAGE_UNREADABLE) && verdict == VERDICT_RESTART) {
				var (
					hosts []lockman.Host_lease
					peer_state string
					state_ok bool
					herr error
				)
				hosts, herr = lockman.Inq_hosts()
				if (herr == nil) {
					for _, h := range hosts {
						if (h.Id == peer.Host_id) {
							peer_state = h.State
							state_ok = true
							break
						}
					}
				}
				if (!state_ok) {
					logger.Debug("verdict: %s: the second confirmation cannot read the peer's host lease state", peer.Host_uuid)
					verdict = VERDICT_HOLD
				} else if (!confirmed_dead(dead, peer.Host_uuid, peer_state, ts.Now())) {
					logger.Debug("verdict: %s: the second confirmation is not there yet (host lease state %s, window open)", peer.Host_uuid, peer_state)
					verdict = VERDICT_HOLD
				}
			}
			/*
			 * Every peer's row is kept for this round: the landing rule needs
			 * the other hosts' rows to decide who may take a VM over, and a row
			 * that was judged before the
			 * restart row we are acting on is just as valid.
			 */
			rows = append(rows, Peer_row{ Host_uuid: peer.Host_uuid, Name: peer.Name, Row: row })
			line = line_of(peer, c, row, verdict, e1, armed)
			publish_judgement(peer, c, row, verdict, e1, armed)
			key = fmt.Sprintf("%s/%s/%v/%s/%v/%v/%s", row, verdict, c.Report_fresh,
				peer.Host_state, c.Mgmt_known, c.Mgmt_reachable, e1)
			/*
			 * Running must not produce audit noise;
			 * everything else is logged when its judgement changes.
			 */
			if (row == ROW_RUNNING) {
				logger.Debug("verdict: %s", line)
				delete(last, peer.Host_uuid)
				continue
			}
			if (last[peer.Host_uuid] != key) {
				logger.Log("verdict: %s", line)
				last[peer.Host_uuid] = key
			} else {
				logger.Debug("verdict: %s", line)
			}
			if (verdict == VERDICT_RESTART) {
				restarts = append(restarts, restart_line{
					peer: peer,
					row: row,
					verdict: verdict,
					e1: e1,
					dead_since_ms: dead[peer.Host_uuid].since,
				})
			}
		}
		/*
		 * Second pass: the actions. Nothing above this point acted, and with
		 * the switch of the action surface off nothing below it does either.
		 */
		drive_rows(armed, restarts, rows, now_ms, last_action)
		/* the cluster may have changed the pace: follow it */
		if (pub.Policy != nil && pub.Policy.Hastate_refresh_seconds > 0 && pub.Policy.Hastate_refresh_seconds != seconds) {
			seconds = pub.Policy.Hastate_refresh_seconds
			ticker.Reset(time.Duration(seconds) * time.Second)
		}
	}
	_ = me
}

/*
 * drive_rows is the action pass of one round: it applies Gate() to every
 * restart judgement and hands the ones that may act to the execution half. It
 * returns how often the execution half was called.
 *
 * It is a function of its arguments (and of the injected Drive) on purpose: the
 * safety bottom line of the slice - with the arm switch off the execution half
 * is called exactly zero times - is then something a test asserts, instead of
 * something a reader has to believe.
 */
func drive_rows(armed bool, restarts []restart_line, rows []Peer_row, now_ms int64, last map[string]string) int {
	var (
		driven int
	)
	for _, r := range restarts {
		if (drive_row(armed, r, rows, now_ms, last)) {
			driven++
		}
	}
	return driven
}

/* restart_line is one restart judgement carried from the first pass to the
 * second, so that the landing candidates are known for the whole round. */
type restart_line struct {
	peer hastate.Peer
	row string
	verdict string
	e1 string
	dead_since_ms int64
}

/*
 * drive_row applies Gate() to one restart judgement and, if it says so, hands
 * the row to the execution half; it reports whether the execution half was
 * called. Two things must be visible in the log here: the judgement that would
 * have driven an action anyway (with armed and the reason), and the result of
 * the action when there is one.
 */
func drive_row(armed bool, r restart_line, rows []Peer_row, now_ms int64, last map[string]string) bool {
	var (
		d Decision
		line string
		round Round
		summary string
	)
	d = Gate(armed, r.verdict, r.row, r.e1, r.dead_since_ms, now_ms)
	line = fmt.Sprintf("host=%s name=%s row=%s verdict=%s armed=%v action=%s",
		r.peer.Host_uuid, r.peer.Name, r.row, r.verdict, armed, d.Reason)
	/*
	 * A restart judgement that persists (the peer stays dead) must not fill
	 * the audit trail: log it when it changes, keep it at debug otherwise.
	 */
	if (last[r.peer.Host_uuid] != line) {
		logger.Log("verdict: %s", line)
		last[r.peer.Host_uuid] = line
	} else {
		logger.Debug("verdict: %s", line)
	}
	if (!d.Act) {
		return false
	}
	if (Drive == nil) {
		logger.Log("verdict: %s armed=%v action=restart but no execution half is wired: no action",
			r.peer.Host_uuid, armed)
		return false
	}
	round = Round{
		Victim_uuid: r.peer.Host_uuid,
		Victim_name: r.peer.Name,
		Row: r.row,
		Verdict: r.verdict,
		E1: r.e1,
		Now_ms: now_ms,
		Peers: rows,
	}
	summary = Drive(round)
	logger.Log("verdict: driven %s", summary_of(round, summary))
	return true
}
