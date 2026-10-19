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
 * Resigning the coordinator's office on an intended stop.
 *
 * A virtxd that is stopped on purpose can give its lease back cleanly: the holder
 * client's child is signalled and the client releases the lease itself (a measured round,
 * wired into release() in a later round). A virtxd that crashes or is killed cannot do
 * anything, and is not asked to: SIGKILL cannot even be caught, so the old path -
 * judge the owner dead, then wait out the handoff window - stays exactly as it was.
 *
 * Two things make the handover real rather than decorative:
 *
 *  1. the resignation is recorded before the ending runs, and classify()/decide()
 *     (coord.go) treat a leader that still names us with timestamp 0 as given back,
 *     so nothing in this process publishes or renews while the ending runs, and the
 *     patrol will not compete for that lease again until the handoff window has
 *     passed (design point 2: a host that resigns does not snatch the office back);
 *  2. it is deliberately local: the question is only whether THIS process still has
 *     a holder client. Reading the on-disk leader to decide would be wrong here -
 *     another host may have taken the office already, and this process would then be
 *     resigning something it does not hold.
 *
 * Fail soft: every failure only logs. The shutdown path must never be blocked by the
 * handover, and both waits it can do are bounded (the graceful one by the operations
 * it is given, the fallback by the 2 s release() always had).
 */
package coord

import (
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/ts"
)

/* when this process handed its office over, 0 = never */
var resigned_at_ms atomic.Int64

/* resigned_ms is when this process last resigned, 0 if it never did. The patrol
 * reads it (coord.go) to keep a resigned host out of the contest for one window. */
func resigned_ms() int64 {
	return resigned_at_ms.Load()
}

/* Resigned_ms is resigned_ms for callers outside this package. */
func Resigned_ms() int64 {
	return resigned_at_ms.Load()
}

/*
 * RESIGN_WAIT is how long the resignation lets the holder client take to exit on its
 * own. The default bound is ten seconds; a shutdown should not sit that long when the
 * client normally releases in a fifth of a second, and if it does not exit within this
 * shorter bound the fallback path runs exactly as usual.
 */
const RESIGN_WAIT = 3 * time.Second

/* resign_ops are the real operations with the shorter graceful wait. */
func resign_ops() Holder_ops {
	var (
		ops Holder_ops = default_holder_ops()
	)
	ops.Max_wait = RESIGN_WAIT
	return ops
}

/*
 * Resign gives back the office this process holds, once. It returns true when it
 * held something and ended it, false when there was nothing to give back.
 */
func Resign(reason string) bool {
	return resign_with(resign_ops(), reason)
}

/*
 * resign_with is Resign with the outside world passed in, so that the decision path
 * can be tested without processes or a sanlock daemon.
 */
func resign_with(ops Holder_ops, reason string) bool {
	var (
		held bool
	)
	p, h := office_snapshot()
	held = (h != nil || p != nil)
	if (!held) {
		logger.Debug("coord: nothing to resign (%s)", reason)
		return false
	}
	/*
	 * From here on a resignation is in flight, which is what Wait_resign() waits
	 * for: the shutdown path must not walk out from under the ending.
	 */
	resign_inflight.Add(1)
	defer resign_inflight.Add(-1)
	/*
	 * Record the resignation first: from this instant a leader that still names us
	 * with timestamp 0 counts as given back, and the patrol keeps this host out of
	 * the contest for one handoff window.
	 */
	resigned_at_ms.Store(ts.Now())
	logger.Log("coord: resigning %s (%s): handing the office back instead of leaving an orphan",
	           COORD_RESOURCE, reason)
	release_with_ops(ops)
	resigned_p, resigned_h := office_snapshot()
	logger.Log("coord: resigned %s: holder=%v pending=%v", COORD_RESOURCE, resigned_h != nil, resigned_p != nil)
	return true
}

/*
 * RESIGN_WAIT_BUDGET is how long a caller may wait for a resignation to finish. The
 * graceful ending signals the holder's child first and that is what gives the lease
 * back, so the part that matters happens in the first few hundred milliseconds; the
 * bound only has to outlast a slow ending. It is deliberately smaller than main's own
 * 5 s shutdown budget, and in the worst case a stop takes that budget plus this.
 */
const RESIGN_WAIT_BUDGET = 3 * time.Second

/* RESIGN_POLL is how often Wait_resign looks whether the ending is done. */
const RESIGN_POLL = 50 * time.Millisecond

/* resign_inflight is how many resignations are running right now. */
var resign_inflight atomic.Int32

/*
 * Wait_resign waits, bounded and fail-soft, for the resignation a shutdown signal
 * started to finish, and reports whether it finished.
 *
 * Why this is needed: the signal is delivered to this package's watcher and to main's
 * own channel (signal.Notify only adds a receiver), the watcher hands the office over
 * in its goroutine, and main used to walk straight out of the process after its
 * deferred shutdown - so the ending was usually cut off before the holder's child was
 * signalled. Measured 2026-10-07: that race lost two real handovers out of three
 * each time leaving the lease held by a client the exiting process never
 * touched, while the same code won it once. It waits for the resigning, never
 * performs one: the signal is handled exactly once, by the watcher.
 *
 * It returns at once when there is nothing to give back (not the coordinator, or the
 * ending already finished) and gives up at the timeout otherwise - a shutdown must not
 * be held up by a handover that cannot complete. Callers should only wait when they
 * know a shutdown signal was seen; nothing else starts a resignation.
 */
func Wait_resign(timeout time.Duration) bool {
	return wait_resign(timeout, resign_settled)
}

/* resign_settled is true when no ending is running and nothing is left to give back. */
func resign_settled() bool {
	if (resign_inflight.Load() > 0) {
		return false
	}
	/* the snapshot is taken under office_mu: the resigning goroutine clears both
	 * pointers through office_clear(), and reading them here was the data race
	 * the race detector reported (release_with_ops against resign_settled) */
	_, h := office_snapshot()
	if (h != nil) {
		return false
	}
	return true
}

/* wait_resign is Wait_resign on an arbitrary predicate, so the waiting itself can be
 * tested without processes or a sanlock daemon. */
func wait_resign(timeout time.Duration, settled func() bool) bool {
	var (
		deadline time.Time = time.Now().Add(timeout)
	)
	for {
		if (settled()) {
			return true
		}
		if (!time.Now().Before(deadline)) {
			return false
		}
		time.Sleep(RESIGN_POLL)
	}
}

/*
 * Resign_on_signal answers one shutdown signal. SIGINT and SIGTERM are the intended
 * stops - main registers the same two signals, and signal.Notify only ever adds a
 * receiver, so both channels see them. Anything else does nothing, and a SIGKILL
 * cannot be delivered to a handler at all: that is the crash path, where no action
 * is possible and none is taken.
 */
func Resign_on_signal(sig os.Signal) bool {
	if (sig == nil) {
		return false
	}
	if (sig == syscall.SIGINT || sig == syscall.SIGTERM) {
		return Resign(fmt.Sprintf("signal %d", int(sig.(syscall.Signal))))
	}
	logger.Debug("coord: signal %v is not an intended stop: not resigning", sig)
	return false
}

/*
 * watch_shutdown_signals lets this package hand the office over without main's
 * shutdown path changing: it receives the same SIGINT/SIGTERM main does.
 */
func watch_shutdown_signals() {
	var (
		c chan os.Signal = make(chan os.Signal, 1)
		sig os.Signal
	)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	for sig = range c {
		Resign_on_signal(sig)
	}
}
