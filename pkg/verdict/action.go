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
 * The action half of the judgement half: Slice C of the action plan, the first
 * real action.
 *
 * Everything in this file is the DECISION to act and nothing else. It is
 * deliberately a pure function of what the loop already read (Gate), so that
 * every reason not to act - above all "the arm switch is off" - is
 * constructible in a unit test without a cluster, a lease or a VM.
 *
 * The execution half is pkg/coord and is installed with Set_drive(). It is
 * injected rather than imported because pkg/coord already imports this package
 * (it reads the leases and the lease keys here): a direct call would be an import
 * cycle. A nil drive means "nothing may act", which is also what an
 * unwired deployment gets.
 */
package verdict

import (
	"fmt"
)

/*
 * T_RESET_MARGIN_MS is the reset margin: how long the dead host's storage paths are
 * given to be reset after its watchdog has fired, plus a margin. The measured
 * reset latency is about 4 s (measured 2026-09-30), so 15 s leaves room for a slow
 * reset without holding a rescue back for a whole sanlock tier.
 */
const T_RESET_MARGIN_MS = 15000

/*
 * T_TAKEOVER_MS is the wait the design demands before a `Failed`
 * host may be taken over, measured from the clock the code actually uses: the
 * first DEAD reading this host saw. That is what t_takeover_elapsed() below is
 * handed - dead_since_ms is confirmed_dead's `since`, the moment we first read
 * the peer DEAD - and never the moment the peer died.
 *
 * On that clock the wait reduces to the reset margin and nothing else:
 *
 *	sanlock reports a peer DEAD only once its host_id lease has gone
 *	unrenewed for DEAD_CONFIRM_MS = 8 x io_timeout + watchdog_fire_timeout, and
 *	watchdog_fire_timeout sits inside that sum, so the watchdog has already
 *	fired by the time any observer can read DEAD. What is left to wait for
 *	is the peer's reset completing:
 *
 *	TAKEOVER_MS = the reset margin = T_RESET_MARGIN_MS = 15000 ms
 *
 * It is therefore deliberately NOT a function of the tier, and it is not
 * "DEAD_CONFIRM_MS + 10 s" either: that sum counts from the death, and no host can
 * observe a death before it happens. The value this constant held until
 * 2026-10-05, 80 s, was DEAD_CONFIRM_MS + 10 s of the *old* 5/30 tier (70 + 10) - a
 * number from a tier the cluster had left, counted on a clock the code does
 * not use. This is the one behaviour change of the 2026-10-05 window work, and
 * it is reported as such.
 *
 * The `Failed` row is the only row with this wait: the two "host alive" rows
 * get their timing from the lease read itself, which is re-read on demand.
 */
const T_TAKEOVER_MS = T_RESET_MARGIN_MS

/*
 * Decision is what the loop may do with one judgement:
 * Act false means "log only", and Reason says why, so that the audit trail
 * answers "why did nothing happen" without reading the code.
 */
type Decision struct {
	Act bool
	Reason string
}

/*
 * Evidence_ok applies the per-row evidence rule of the design. The rows
 * must not be mixed up:
 *
 *	Failed - the second confirmation (applied by the loop before this is asked),
 *	         TAKEOVER_MS and the fence chain. The landing's lease evidence does
 *	         NOT apply to this row: the host is gone, so there is nobody to ask.
 *	         Demanding the lease read here would demand evidence the design says is not
 *	         part of this path, and would freeze the only row that works when
 *	         the storage is healthy but the host is dead.
 *	HostAliveStorageLost / PartitionStorageUnreadable - the lease read, or the
 *	         second lease check. The second check is not implemented, so the lease
 *	         read is the only usable evidence; "held" and "not applicable" both
 *	         mean "no positive evidence", and both stop the action ("no lease
 *	         evidence, no automatic restart").
 */
func Evidence_ok(row string, e1 string) bool {
	switch (row) {
	case ROW_FAILED:
		return true
	case ROW_ISOLATED:
		/* the claim comes first: the leases are the landing's business */
		return true
	case ROW_HOST_ALIVE_STORAGE_LOST, ROW_PARTITION_STORAGE_UNREADABLE:
		return (e1 == LEASES_FREE)
	}
	/* every other row is not a restart row at all; do not invent evidence */
	return false
}

/* t_takeover_elapsed reports whether the Failed row's wait is over. */
func t_takeover_elapsed(dead_since_ms int64, now_ms int64) bool {
	if (dead_since_ms <= 0) {
		/* we never saw the peer DEAD: no clock to measure TAKEOVER_MS on */
		return false
	}
	return (now_ms - dead_since_ms) >= T_TAKEOVER_MS
}

/*
 * Gate is the whole decision to act on one judgement, in the order the safety
 * argument needs:
 *
 *   1. only `restart` is actionable at all. This is
 *      a statement about the verdict, not a reason for refusing an action;
 *   2. the arm switch must be on. It is the safety bottom line of this slice:
 *      while it is off a restart verdict is a log line and nothing else. It is
 *      checked before every other reason that could refuse an action, so for
 *      every actionable verdict "off" wins - the `not-restart` test above is
 *      not a competing reason, it is what keeps non-actions out of here;
 *   3. the row's own evidence must be complete (per row, see Evidence_ok);
 *   4. the Failed row must have served TAKEOVER_MS.
 *
 * The coordinator term is NOT part of this function: "only the coordinator may
 * authorise" is enforced by the execution half, which
 * is the only part that holds the lease.
 */
func Gate(armed bool, verdict string, row string, e1 string, dead_since_ms int64, now_ms int64) Decision {
	if (verdict != VERDICT_RESTART) {
		return Decision{ false, "not-restart" }
	}
	if (!armed) {
		return Decision{ false, "armed-off" }
	}
	if (!Evidence_ok(row, e1)) {
		return Decision{ false, "no-evidence" }
	}
	if (row == ROW_FAILED && !t_takeover_elapsed(dead_since_ms, now_ms)) {
		return Decision{ false, "t-takeover" }
	}
	return Decision{ true, "restart" }
}

/*
 * Peer_row is one other host's judgement of this round, as the landing rule
 * needs it: the victim's own VMs are not taken from here (a host that is dead
 * cannot be asked), and a landing candidate must be a host this round judged
 * `Running`.
 */
type Peer_row struct {
	Host_uuid string
	Name string
	Row string
}

/* Round is everything the judgement loop knows when it drives one restart row. */
type Round struct {
	Victim_uuid string
	Victim_name string
	Row string
	Verdict string
	E1 string
	Now_ms int64
	Peers []Peer_row
}

/*
 * Drive is the execution half (pkg/coord.Drive_round), installed by the wiring
 * in cmd/virtxd before Start(). It is a function value and not an interface so
 * that the dependency stays one-way and visible in main().
 *
 * It returns a one line summary for the loop's log; the per-VM audit lines
 * (vm_uuid, epoch, landing, result, duration) are the execution half's.
 */
var Drive func(r Round) string

/* Set_drive installs the execution half; nil means "nothing may act". */
func Set_drive(f func(r Round) string) {
	Drive = f
}

/* summary_of renders the one line the loop logs after driving a restart row. */
func summary_of(r Round, summary string) string {
	return fmt.Sprintf("host=%s name=%s row=%s verdict=%s %s",
		r.Victim_uuid, r.Victim_name, r.Row, r.Verdict, summary)
}
