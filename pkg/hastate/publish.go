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

package hastate

import (
	"sync"

	"suse.com/virtx/pkg/lockman"
	"suse.com/virtx/pkg/policy"
)

/*
 * Published is the last observation this host made, for read-only consumers
 * (for example the judgement layer). It is a copy:
 * the consumer must not depend on the observation goroutine's timing.
 */
type Published struct {
	Ok bool
	Ts int64	/* when this observation was made (our clock, epoch ms) */
	Probed int
	Isolated int
	Peers []Peer
	Leases []lockman.Resource_lease
	Unreadable int
	Skipped int
	Addrs []string
	/*
	 * Policy is the cluster policy of the round this observation was
	 * made in: read-only consumers judge with the same settings the
	 * observation was made under, and never resolve the policy themselves.
	 */
	Policy *policy.Policy
	Origin policy.Origin
}

var published struct {
	mu sync.Mutex
	p Published
}

/* publish stores a copy of the current view for read-only consumers. */
func publish(v view, probed int, isolated int) {
	var (
		p Published
	)
	p.Ok = v.ok
	p.Ts = v.ts
	p.Probed = probed
	p.Isolated = isolated
	p.Peers = append(p.Peers, v.peers...)
	p.Leases = append(p.Leases, v.leases...)
	p.Unreadable = v.unreadable
	p.Skipped = v.skipped
	p.Addrs = append(p.Addrs, v.addrs...)
	p.Policy = v.policy
	p.Origin = v.origin
	published.mu.Lock()
	published.p = p
	published.mu.Unlock()
}

/*
 * Published_view returns the last published observation. Ok is false until the
 * first observation has come back.
 */
func Published_view() Published {
	var (
		p Published
	)
	published.mu.Lock()
	p = published.p
	published.mu.Unlock()
	p.Peers = append([]Peer(nil), p.Peers...)
	p.Leases = append([]lockman.Resource_lease(nil), p.Leases...)
	p.Addrs = append([]string(nil), p.Addrs...)
	return p
}
