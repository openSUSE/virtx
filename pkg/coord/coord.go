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
 * Package coord is the coordinator role of the decision layer.
 *
 * The first half was the read-only part: every patrol classified who holds the
 * __VIRTX__COORD lease and whether that holder's host_id is still renewing, and
 * logged the row of the design's table it landed on. The contending half followed
 * (actuate.go): a candidate now takes the lease, a holder publishes the lock-value block, and
 * the patrol acts on the row it classifies instead of only reporting it.
 */
package coord

import (
	"fmt"
	"sync"
	"strconv"
	"strings"
	"time"

	"suse.com/virtx/pkg/hastate"
	"suse.com/virtx/pkg/lockman"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/policy"
	"suse.com/virtx/pkg/ts"
)

/* the coordinator lease: one resource, in the lockspace the VMs also use. */
const COORD_RESOURCE = "__VIRTX__COORD"

/*
 * T_HANDOFF_DETECT_SECONDS and T_MASK_DELTA_SECONDS are the two named terms of
 * MASK_WINDOW_MS besides DEAD_CONFIRM_MS: HANDOFF_DETECT_MS is how long the
 * detecting host needs to be sure the coordinator's host_id has stopped
 * renewing, and T_MASK_DELTA_SECONDS is the 10 s margin on top of it. The third
 * term, DEAD_CONFIRM_MS, is not a constant at all - it is the tier's:
 *
 *	MASK_WINDOW_MS = HANDOFF_DETECT_MS + DEAD_CONFIRM_MS + 10 s
 *	       = 30 + (8 x io_timeout + watchdog_fire_timeout) + 10
 *	       = 30 + 140 + 10 = 180 s at the 10/60 tier
 */
const (
	T_HANDOFF_DETECT_SECONDS = 30
	T_MASK_DELTA_SECONDS = 10
)

/*
 * T_COORD_HANDOFF_MS is how long, after first observing the coordinator's
 * host_id as unreachable, we wait before taking the lease. Decision of
 * 2026-10-04: it equals MASK_WINDOW_MS.
 *
 * It is derived, not written out. The derivation is coord_handoff_ms() below,
 * and its input is the effective sanlock tier, read at startup by
 * lockman.Read_tier() (pkg/lockman, sanlock_tier.go) and handed over by Set_tier
 * (sanlock_tier_window.go). At this cluster's 10/60 tier the sum is
 * 30 + (8 x 10 + 60) + 10 = 180 s = 180000 ms, which is what the literal used
 * to hold; a cluster on another tier gets another window with no code change.
 * This is the only place MASK_WINDOW_MS is defined.
 */
var T_COORD_HANDOFF_MS = coord_handoff_ms(lockman.Default_tier())

/* coord_handoff_ms is the one place MASK_WINDOW_MS is computed from a tier. */
func coord_handoff_ms(t lockman.Tier) int64 {
	return int64(T_HANDOFF_DETECT_SECONDS + t.Dead_confirm_seconds() + T_MASK_DELTA_SECONDS) * 1000
}

/* Observed is what one patrol saw about the coordinator lease. */
type Observed int

const (
	OBS_ABSENT = iota	/* the resource does not exist yet */
	OBS_UNREADABLE		/* we could not read it: never declare ourselves */
	OBS_OURS		/* the leader says we own it and the lease is held (timestamp != 0) */
	OBS_OTHER_ALIVE		/* another host holds it and its host_id still renews */
	OBS_OTHER_DEAD		/* another host holds it and its host_id stopped renewing */
	OBS_GIVEN_BACK		/* the leader names us, but timestamp 0: we handed the lease back */
	OBS_RELEASED		/* the leader names another host, timestamp 0: the lease is free */
)

/* Role is what the rules say to do about it. */
type Role int

const (
	ROLE_OBSERVE = iota	/* not ours to take (yet) */
	ROLE_CANDIDATE		/* the lease may be taken */
	ROLE_KEEP		/* we hold it */
)

func (o Observed) String() string {
	switch (o) {
	case OBS_ABSENT:
		return "absent"
	case OBS_UNREADABLE:
		return "unreadable"
	case OBS_OURS:
		return "ours"
	case OBS_OTHER_ALIVE:
		return "other-alive"
	case OBS_OTHER_DEAD:
		return "other-dead"
	case OBS_GIVEN_BACK:
		return "given-back"
	case OBS_RELEASED:
		return "released"
	}
	return "?"
}

func (r Role) String() string {
	switch (r) {
	case ROLE_OBSERVE:
		return "observe"
	case ROLE_CANDIDATE:
		return "candidate"
	case ROLE_KEEP:
		return "keep"
	}
	return "?"
}

/*
 * classify is the split of the design's table as a pure function, with the timestamp
 * the lease carries as part of it. read_ok/read_found say whether the lease could
 * be read at all, owner_id is the leader's owner, timestamp is that leader's
 * timestamp, and peer_renewing whether that owner's host_id was seen with a moving
 * timestamp ("unknown" must be passed as renewing: a first reading can never
 * establish that a peer is dead).
 *
 * The timestamp matters because a leader does NOT stop naming its owner when the
 * lease is given back: sanlock leaves owner_id, owner_generation and lver alone and
 * writes only timestamp 0 (paxos_lease.c:3143-3150; measured on scratch resources
 * 2026-10-07, from the controlled lease experiments). So a host that
 * just handed the office over still reads "owner == me" on disk, and without the
 * timestamp it would go on treating itself as the coordinator and keep publishing.
 * The same rule was needed - and fixed - in pkg/lockman's stale sweep for KEEP_OURS
 * (measured), where an orphan of a lease that was given back was being
 * kept forever.
 */
func classify(read_ok bool, read_found bool, owner_id uint64, my_host_id uint64,
              timestamp uint64, peer_renewing bool) Observed {
	if (!read_ok) {
		return OBS_UNREADABLE
	}
	if (!read_found) {
		return OBS_ABSENT
	}
	/*
	 * timestamp 0 (LEASE_FREE) means the lease has been given back, whoever the
	 * leader still names: ours (we resigned, or an earlier process of ours did) or
	 * somebody else's (that host resigned, or released it).
	 */
	if (timestamp == 0) {
		if (owner_id == my_host_id) {
			return OBS_GIVEN_BACK
		}
		return OBS_RELEASED
	}
	if (owner_id == my_host_id) {
		return OBS_OURS
	}
	if (peer_renewing) {
		return OBS_OTHER_ALIVE
	}
	return OBS_OTHER_DEAD
}

/*
 * decide maps what we saw to what we do. A peer that still renews is left alone
 * (row 4: observe + alert); only after the handoff window has elapsed since we
 * first saw it not renewing may we try (row 3). An unreadable lease never lets us
 * declare ourselves (row 5b).
 *
 * Two states come from a given back lease (timestamp 0, classify above):
 *
 *  - OBS_RELEASED, another host's: the lease is free, so the lease may be taken
 *    right away - that is the fast path a planned handover needs, and it is the
 *    same situation the fresh election of 2026-10-07 met when the lease file had
 *    been deleted (OBS_ABSENT, takeable at once: the winning host took office in
 *    13 s). What this decides is only whether to *try*: the attempt still has to
 *    win sanlock's own acquisition against whoever holds that lease's records, so
 *    a lease that a live owner still holds cannot be taken by this.
 *  - OBS_GIVEN_BACK, ours: we hand the office over, and until the same window a
 *    dead owner gets has passed since we resigned, we do NOT compete for it again.
 *    A process that starts fresh has no resignation of its own (resigned_ms == 0)
 *    and takes the free lease right away, which is what a virtxd restart is for.
 */
func decide(state Observed, other_dead_since_ms int64, now int64, resigned_ms int64) Role {
	switch (state) {
	case OBS_OURS:
		return ROLE_KEEP
	case OBS_ABSENT, OBS_RELEASED:
		return ROLE_CANDIDATE
	case OBS_GIVEN_BACK:
		if (resigned_ms > 0 && (now - resigned_ms) < T_COORD_HANDOFF_MS) {
			return ROLE_OBSERVE
		}
		return ROLE_CANDIDATE
	case OBS_OTHER_DEAD:
		if (other_dead_since_ms > 0 && (now - other_dead_since_ms) >= T_COORD_HANDOFF_MS) {
			return ROLE_CANDIDATE
		}
		return ROLE_OBSERVE
	}
	return ROLE_OBSERVE
}

/* Published is the two item payload the lock-value block carries, and nothing else. */
type Published struct {
	Epoch int64
	Mask_deadline_ms int64
}

/* encode renders the lock-value block payload. */
func encode(p Published) string {
	return fmt.Sprintf("epoch=%d mask_deadline_ms=%d", p.Epoch, p.Mask_deadline_ms)
}

/* decode parses a lock-value block payload; ok is false when it is not a coordinator one. */
func decode(payload string) (Published, bool) {
	var (
		p Published
		err error
		ok_epoch, ok_mask bool
	)
	for _, field := range strings.Fields(payload) {
		var (
			key, value string
			parts []string
		)
		parts = strings.SplitN(field, "=", 2)
		if (len(parts) != 2) {
			continue
		}
		key = parts[0]
		value = parts[1]
		switch (key) {
		case "epoch":
			p.Epoch, err = strconv.ParseInt(value, 10, 64)
			ok_epoch = (err == nil)
		case "mask_deadline_ms":
			p.Mask_deadline_ms, err = strconv.ParseInt(value, 10, 64)
			ok_mask = (err == nil)
		}
	}
	return p, ok_epoch && ok_mask
}

/* peek finds the coordinator lease in a resource listing. */
func peek(leases []lockman.Resource_lease) (found bool, owner_id uint64) {
	for _, l := range leases {
		if (l.Name == COORD_RESOURCE) {
			return true, l.Owner_id
		}
	}
	return false, 0
}

/*
 * renewing tells whether one host's host_id lease is still being renewed, using
 * the state sanlock itself reports.
 *
 * Measured 2026-10-04 (two independent read paths, 6 s apart, values identical):
 * the host_id leader's timestamp does NOT advance while the lease is held, so
 * the "the timestamp moves" test cannot tell a renewing host
 * from a dead one - using it made live hosts read as dead and would eventually
 * have triggered spurious takeovers. The state column is the signal that
 * flipped correctly in every field test.
 *
 * Only DEAD counts as death. FAIL means the renewal is failing (not finished),
 * and UNKNOWN means the reading host has no judgement yet - and a reading host
 * right after its own reboot does report UNKNOWN for a while (measured
 * 2026-10-04: h11 misread h12 as dead right after coming back). Treating those
 * as death would start a needless handoff window; waiting costs nothing, since
 * a host that stays dead is reported DEAD.
 */
func renewing(hosts []lockman.Host_lease, host_id uint64) (is_renewing bool, seen bool) {
	for _, h := range hosts {
		if (h.Id != host_id) {
			continue
		}
		return (h.State != "DEAD"), true
	}
	/*
	 * No reading for that host at all. A lease that was given back looks like
	 * this from here: when a holder client runs its explicit release, its
	 * daemon-side registration disappears and the resource leader keeps the old
	 * owner_id, owner_generation and lver but sets timestamp to 0 (LEASE_FREE) -
	 * so the leader may still name an owner the lockspace no longer knows.
	 * Measured on scratch resources 2026-10-07 (from the controlled lease
	 * experiments; the write itself is
	 * paxos_lease.c:3143-3150). Such a lease is takeable - but only through the
	 * same handoff window a dead owner goes through, so a host that is merely
	 * not listed yet cannot be overtaken.
	 */
	return false, false
}

/*
 * current_policy is the cluster policy of the round the observation was made in.
 * The decision layer resolves the policy once per round and publishes it with
 * what it saw; the coordinator uses that same reading instead of resolving the
 * policy a second time, which would need its own generation bookkeeping.
 */
func current_policy() *policy.Policy {
	var (
		pub hastate.Published
	)
	pub = hastate.Published_view()
	return pub.Policy
}

/* max_inflight is how many takeovers may be in flight at once, from the policy. */
var (
	change_mu sync.Mutex
	change_asked bool
	change_writer func() bool
)

/*
 * Request_change records that a change of the action surface has been asked for.
 * It gives this host a reason to hold the office: writing the shared policy is
 * what the office is for, and a host that wants nothing does not take a lease on
 * the shared storage at all - an idle cluster keeps no __VIRTX__COORD (measured
 * 2026-10-13: it held one whether or not HA had ever been enabled).
 */
func Request_change() {
	change_mu.Lock()
	change_asked = true
	change_mu.Unlock()
}

/* Change_pending reports whether a change has been asked for and not written yet. */
func Change_pending() bool {
	var (
		asked bool
	)
	change_mu.Lock()
	asked = change_asked
	change_mu.Unlock()
	if (asked) {
		return true
	}
	/*
	 * A change asked for on another host is in the shared tree, where every host
	 * reads it: the office is what writes it, and the office may well be held by a
	 * host other than the one the operator talked to (measured 2026-10-13: the
	 * request lived in one process, so a change asked for on a host that never got
	 * the office was never written, and its caller saw "PENDING" for good).
	 */
	return policy.Request_pending()
}

/* Change_settled forgets the request: the change it asked for has been written. */
func Change_settled() {
	change_mu.Lock()
	change_asked = false
	change_mu.Unlock()
}

/*
 * Set_change_writer installs the half that writes a requested change. It is a
 * hook because the writer lives in pkg/ha, which reads this package: the office
 * says when, pkg/ha says what.
 */
func Set_change_writer(f func() bool) {
	change_mu.Lock()
	change_writer = f
	change_mu.Unlock()
}

/*
 * wanted is whether this host has a reason to hold the office: the action surface
 * is on, so a judgement may have to drive, or a change of it has been asked for.
 *
 * The shared file answers that, and not this host's own report of it: a host that
 * has just written a change still carries the older generation in its report for
 * a round, and asking the report made it hand the office back right after writing
 * - another host, whose report had caught up, then took it again (measured
 * 2026-10-13: one resign and one take per change, epochs 19981 to 19984 for two).
 */
func wanted() bool {
	var (
		p *policy.Policy
		err error
	)
	if (Change_pending()) {
		return true
	}
	p, err = policy.Load(policy.PATH)
	if (err != nil) {
		/* the shared tree is out of reach: this host's own copy is what it has */
		p = current_policy()
	}
	if (p == nil) {
		return false
	}
	return p.Ha_enabled
}

/* write_change runs the installed writer and settles the request it wrote. */
func write_change() {
	var (
		f func() bool
	)
	change_mu.Lock()
	f = change_writer
	change_mu.Unlock()
	if (f == nil) {
		return
	}
	if (f()) {
		Change_settled()
	}
}

func max_inflight() int {
	var (
		p *policy.Policy
	)
	p = current_policy()
	if (p != nil && p.Max_inflight > 0) {
		return p.Max_inflight
	}
	return policy.DEFAULT_MAX_INFLIGHT
}

/* Start runs the patrol in its own goroutine, and watches for an intended stop. */
func Start() {
	go Loop(0)
	/*
	 * A virtxd that is stopped on purpose hands the office over (resign.go). The
	 * watcher sits in this package so that main's shutdown path needs no change:
	 * signal.Notify only ever ADDS a receiver, so main's own channel still gets the
	 * same signal. A crash and a SIGKILL deliver nothing and resign nothing.
	 */
	go watch_shutdown_signals()
}

/*
 * Loop classifies one row of the design's table per patrol and acts on it: a
 * candidate tries to take the lease, a keeper publishes the lock-value block (and takes the
 * lease again after a restart), and any other state releases a client we still
 * hold (actuate.go, the contending half). The classification alone - what the
 * read-only half did - is still logged, so the row and the role can be watched
 * on a real cluster.
 */
func Loop(seconds int) {
	var (
		ticker *time.Ticker
		other_dead_since int64
		last string
	)
	if (seconds < 1) {
		seconds = policy.DEFAULT_HASTATE_REFRESH_SECONDS
	}
	logger.Debug("coord: patrol starting, period %d s, handoff %d ms", seconds, T_COORD_HANDOFF_MS)
	ticker = time.NewTicker(time.Duration(seconds) * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		var (
			leader lockman.Resource_leader
			hosts []lockman.Host_lease
			leader_err, status_err error
			found bool
			owner_id uint64
			read_ok bool
			is_renewing, seen bool
			me_alive bool
			state Observed
			role Role
			now int64
			me uint64
			line string
			p *policy.Policy
		)
		now = ts.Now()
		me = uint64(lockman.Lockid())
		/*
		 * Read the coordinator lease's leader directly: it is a flat file, so
		 * the directory scan of Inq_leases does not see it.
		 */
		leader, leader_err = lockman.Read_resource_leader(COORD_RESOURCE, resource_path())
		read_ok = (leader_err == nil)
		if (read_ok) {
			found = leader.Found
			owner_id = leader.Owner_id
		}
		/* the read the design requires, to tell row 3 from row 4 */
		hosts, status_err = lockman.Inq_hosts()
		if (status_err != nil) {
			hosts = nil
		}
		is_renewing, seen = renewing(hosts, owner_id)
		_ = seen
		/*
		 * Our own host_id must be renewing before we may claim anything: with a
		 * dead sanlock the lease still reads "owner == me", and treating that
		 * as ours would be a false claim (measured 2026-10-04).
		 */
		me_alive = false
		for _, h := range hosts {
			if (h.Id == me) {
				me_alive = (h.State == "LIVE")
			}
		}
		state = classify(read_ok, found, owner_id, me, leader.Timestamp, is_renewing)
		/*
		 * Remember since when we have been seeing another host's lease with a
		 * host_id that is not renewing; that window is what decide() waits for.
		 */
		if (state == OBS_OTHER_DEAD) {
			if (other_dead_since == 0) {
				other_dead_since = now
			}
		} else {
			other_dead_since = 0
		}
		role = decide(state, other_dead_since, now, resigned_ms())
		if (!me_alive) {
			/* we cannot hold anything: our own lease is not being renewed */
			if (holder != nil || pending != nil) {
				release()
			}
			role = ROLE_OBSERVE
		}
		if (hastate.Self_isolated() != 0) {
			/*
			 * This host cannot reach the cluster, so it cannot claim anything for
			 * anybody, and while it holds the office nobody else may. Give it up: the
			 * office is what has to move for the VMs to be rescued. This is asked
			 * first, and not inside the "switch is off" arm below, because an isolated
			 * host has no business holding the office whatever the switch says - with
			 * the action surface on, the office is exactly what has to move for the
			 * response to happen at all (measured 2026-10-16: isolated=1, role=keep,
			 * and no host ever saw the office free).
			 */
			if (holder != nil || pending != nil) {
				Resign("this host is isolated: a host that cannot reach the cluster must not hold the office")
			}
			role = ROLE_OBSERVE
		} else if (!wanted()) {
			/*
			 * There is nothing for the office to do: the action surface is off and
			 * no change has been asked for. This host does not compete for the
			 * lease, and a holder among us gives it back the way a stopped virtxd
			 * does - one release path, not two.
			 */
			if (holder != nil || pending != nil) {
				Resign("the action surface is off, and no change has been asked for")
			}
			role = ROLE_OBSERVE
		}
		/* settle an attempt that is in flight (it may take tens of seconds) */
		adopt(me, hosts)
		/*
		 * A handle on a client that provably holds nothing must not keep this host
		 * out of the contest. adopt() can leave exactly such a handle: it promotes
		 * the client whose acquire is in flight as soon as the leader names us, and
		 * the leader names us for a lease an *earlier* client of ours still holds -
		 * measured 2026-10-07 on h13, which then sat with holder(epoch=6) for
		 * 3 min 47 s while the daemon's registration was the old client's, and
		 * never competed again because take() refuses to start while a holder is
		 * set. Dropping it here means the role branches below can decide
		 * afresh, and a host that holds nothing goes back to being a candidate.
		 */
		drop_losing_holder()
		/*
		 * A switch request that names another host keeps this host out of the
		 * contest until that host takes the office or the request runs out: the
		 * operator asked for that host, and a race would hand the office to
		 * whoever happened to be quicker. The request has a deadline, so a target
		 * that never comes cannot keep the others out of it for long.
		 */
		if (role == ROLE_CANDIDATE) {
			if _, elsewhere := switch_elsewhere(machine.Uuid()); elsewhere {
				role = ROLE_OBSERVE
			}
		}
		/*
		 * The contending half: an absent lease (row 5a) or a peer that
		 * stopped renewing and whose handoff window has elapsed (row 3) lets us
		 * try. take() is bounded, so the patrol never blocks (5.3.1).
		 */
		if (role == ROLE_CANDIDATE) {
			var take_err error
			take_err = take()
			if (take_err != nil) {
				logger.Debug("coord: take: %s", take_err.Error())
			}
		} else if (role == ROLE_KEEP) {
			keep_holder := office_holder_get()
			if (keep_holder == nil) {
				/*
				 * We own the lease but have no handle on it. First ask whether
				 * this host already has a registration of the resource: that is
				 * what a virtxd restart looks like (the holder client of the
				 * previous process keeps it alive - which is exactly why there is
				 * no gap in the role), and taking THAT client over is the only way
				 * to end up with a handle on the client that really holds the
				 * lease. Spawning a second client instead can only lose to the
				 * first one, and that loss is what left h13 holding a stale handle
				 * for 3 min 47 s and h12 spawning a doomed client on
				 * every patrol with the epoch moving with it. Only when
				 * there is no such registration - the lease is held by a client
				 * that is gone, or only an orphan the daemon still lists - do we
				 * take it the usual way, which is also how a takeover from a dead
				 * owner is executed.
				 */
				if (!take_over_registration()) {
					var adopt_err error
					adopt_err = take()
					if (adopt_err != nil) {
						logger.Debug("coord: adopt: %s", adopt_err.Error())
					}
				}
			}
			/*
			 * The mask deadline is refreshed from the attempts that are in
			 * flight, not reset to zero: while a takeover is being executed the
			 * window must stay published, so that the deadline sharing its origin
			 * with the epoch in the lock-value block is true.
			 * With nothing in flight this is the same zero it always was.
			 */
			publish(mask_now())
		} else if (holder != nil) {
			/* the lease is not ours any more: do not keep a client around */
			release()
		}
		/*
		 * A change that has been asked for is written here, once this host holds
		 * the office: that is what wanting it was for.
		 */
		if (office_holder_get() != nil && Change_pending()) {
			write_change()
		}
		/*
		 * The office has been asked to move: if it is ours, give it back now that
		 * any change this host was asked to write has been written, and let the
		 * target take it on its next patrol. A request that names this host is
		 * already satisfied, and one that has run out is removed.
		 */
		if (office_holder_get() != nil) {
			target, live := Switch_target(ts.Now())
			switch {
			case (!live):
				Clear_switch()
			case (target == machine.Uuid()):
				Clear_switch()
			default:
				Resign("the coordinator office is being switched to " + target)
			}
		}
		line = fmt.Sprintf("row=%s role=%s owner=%d me=%d renewing=%v since=%d lease_read_ok=%v status_read_ok=%v holder=%s",
			state.String(), role.String(), owner_id, me, is_renewing, other_dead_since,
			read_ok, status_err == nil, Result())
		logger.Debug("coord: %s", line)
		/*
		 * Only a change of row is worth a Log line: the patrol runs every few
		 * seconds and must not drown the audit trail - "absent" in particular
		 * is a steady state until the contending half exists.
		 */
		if (last != line) {
			logger.Log("coord: %s", line)
			last = line
		}
		/* the cluster may have changed the pace: follow it */
		p = current_policy()
		if (p != nil && p.Hastate_refresh_seconds > 0 && p.Hastate_refresh_seconds != seconds) {
			seconds = p.Hastate_refresh_seconds
			ticker.Reset(time.Duration(seconds) * time.Second)
		}
	}
}
