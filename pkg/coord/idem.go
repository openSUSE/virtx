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
 * The landing side of the idempotency key: the coordinator
 * keeps a key for its term, and the landing host remembers it for ONE mask
 * window - in memory, never on disk. The key itself is built by
 * coord.Idempotency_key from what the instruction carries (epoch, vm_uuid, attempt
 * number); the window below only decides what a repeat of that key means.
 */
package coord

import (
	"sync"
)

/*
 * REFUSE_DUPLICATE is the code this slice adds for "a key that this host has
 * already executed, arriving again inside the same mask window". The design's table
 * has a code for case ② of the idempotency row (already-running) but none for
 * case ③; it says a resend of the same key is allowed there, and this slice
 * does not resend (see Decide), so the arrival has to be named. It is a refusal
 * and not a new action, and it is logged, so it cannot hide a problem.
 */
const REFUSE_DUPLICATE = "duplicate"

/* idem_entry is one key this host has executed, with the window it is valid
 * in; the window is the mask deadline the instruction carried. */
type idem_entry struct {
	deadline_ms int64
}

/* Idempotency_window is the landing host's memory for one mask window. It is safe for
 * concurrent use: the API server handles requests in parallel. */
type Idempotency_window struct {
	mu sync.Mutex
	seen map[string]idem_entry
}

/* New_idem_window returns an empty window. */
func New_idem_window() *Idempotency_window {
	return &Idempotency_window{ seen: make(map[string]idem_entry) }
}

/*
 * Decide applies the design's idempotency row to one arriving key and
 * records it. It returns "" when the instruction may be executed, or the
 * refusal code.
 *
 *	key never seen (or its window is over) - proceed;
 *	key seen and the domain IS running here  - `already-running`: that is
 *	    case ②, "in place", and it must not be executed a second time;
 *	key seen and nothing is running        - `duplicate`: the design's case ③ allows
 *	    one resend of the same key; this slice refuses instead. Refusing is the
 *	    conservative side of that rule (never a second execution), and the
 *	    ladder that would judge whether a resend is warranted is not part of this slice.
 *	    The plan for this slice records it as work for "after the first action".
 *
 * The window is the deadline the instruction carried: past it the entry is
 * forgotten, so a new attempt with a new mask window starts clean.
 */
func (w *Idempotency_window) Decide(key string, mask_deadline_ms int64, now_ms int64, running bool) string {
	var (
		seen bool
	)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prune(now_ms)
	_, seen = w.seen[key]
	if (!seen) {
		w.seen[key] = idem_entry{ deadline_ms: mask_deadline_ms }
		return ""
	}
	if (running) {
		return REFUSE_ALREADY_RUNNING
	}
	return REFUSE_DUPLICATE
}

/* prune forgets the keys whose mask window is over. */
func (w *Idempotency_window) prune(now_ms int64) {
	for key, e := range w.seen {
		if (now_ms > e.deadline_ms) {
			delete(w.seen, key)
		}
	}
}

/*
 * Release forgets a key this host recorded but did not execute - the
 * instruction was refused, proxied on, or otherwise never reached the move.
 *
 * It exists because the record has to be taken BEFORE the checks (that is what
 * makes the dedup race-free against a second request), while the design's idempotency
 * row is about a key that was *executed*: a refused instruction must not burn
 * the window, or a legitimate retry after a re-judged `stale-epoch` would be
 * refused as a duplicate of something that never ran.
 */
func (w *Idempotency_window) Release(key string) {
	w.mu.Lock()
	delete(w.seen, key)
	w.mu.Unlock()
}
