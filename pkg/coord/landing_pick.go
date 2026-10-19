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
 * The landing rule (decided 2026-10-01):
 *
 *   six hard filters - not the victim, not disabled or in maintenance, this
 *   round's row for that host is Running with verdict no-op, the ledger we read
 *   it from is fresh (<= 30 s), it passes the capacity check, and it satisfies the
 *   concurrency limit - then a deterministic order: expected placement, then free capacity, then
 *   host_uuid. No candidate means no preemption: the VM stays pending with an
 *   alert rather than something being stopped to make room.
 *
 * The coordinator may be a landing host itself, with no privilege attached.
 *
 * This is deliberately NOT pkg/sched's Schedule_vmdef: that one takes a Vmdef
 * and serves "virtx create vm", and the design says explicitly not to
 * reuse it for takeovers.
 *
 * Everything here is pure: the caller feeds in what it read, so the whole rule
 * is testable, and a repeated run over the same inputs must pick the same host.
 */
package coord

import (
	"sort"
)

/* LEDGER_FRESH_MS: the ledger a candidate is judged from must be this fresh. */
const LEDGER_FRESH_MS = 30000

/* ROW_RUNNING is the verdict row a landing candidate must be in
 * (see verdict.Select_row). It is spelled out here rather than imported so
 * that this rule stays a pure data question. */
const ROW_RUNNING = "Running"

/* Landing_candidate is one host, with everything the six filters need. */
type Landing_candidate struct {
	Host_uuid string
	Is_victim bool		/* the host being taken over from */
	Disabled bool		/* disabled, or in maintenance (visibility: G10/B6) */
	Row string		/* this round's verdict row for that host */
	Ledger_age_ms int64	/* how old our view of that host is */
	Meets_R4 bool		/* the admission rule of the design */
	Capacity_ok bool	/* C1..C5; CAPACITY_UNCALIBRATED until calibrated */
	/* sort keys */
	Expected_placement bool	/* the VM's expected placement is that host */
	Free_capacity int64	/* remaining capacity, larger is better */
}

/* eligible applies the six hard filters. */
func eligible(c Landing_candidate) bool {
	if (c.Is_victim) {
		return false
	}
	if (c.Disabled) {
		return false
	}
	if (c.Row != ROW_RUNNING) {
		return false
	}
	if (c.Ledger_age_ms > LEDGER_FRESH_MS) {
		return false
	}
	if (!c.Meets_R4) {
		return false
	}
	return c.Capacity_ok
}

/*
 * Pick_landing returns the host to land on, or an empty string when there is
 * none (no preemption: the caller leaves the VM pending and alerts).
 *
 * The order is the decided one: expected placement first, then more free
 * capacity, then the host_uuid - the last key is what makes the outcome
 * reproducible when the first two tie.
 */
func Pick_landing(candidates []Landing_candidate) string {
	var (
		ok []Landing_candidate
	)
	for _, c := range candidates {
		if (eligible(c)) {
			ok = append(ok, c)
		}
	}
	if (len(ok) == 0) {
		return ""
	}
	sort.Slice(ok, func(i, j int) bool {
		if (ok[i].Expected_placement != ok[j].Expected_placement) {
			return ok[i].Expected_placement
		}
		if (ok[i].Free_capacity != ok[j].Free_capacity) {
			return ok[i].Free_capacity > ok[j].Free_capacity
		}
		return ok[i].Host_uuid < ok[j].Host_uuid
	})
	return ok[0].Host_uuid
}
