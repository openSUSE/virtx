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
 * Whether this host can still reach the cluster, and who holds the office.
 *
 * A host is isolated when it has lost sight of every judge address AND of the
 * cluster. The judges are probed here; the cluster is probed through the reports:
 * every host says in its report whether it holds the office, so a host that can no
 * longer reach the office holder - and, when no report claims the office any more,
 * cannot reach any peer at all - knows it is on its own.
 *
 * Nothing here reads serf, and nothing parses the sanlock record: the reports on
 * the shared storage are the source, which is also the only source that survives
 * the very cut being judged. A probe that could not run at all is not evidence of
 * a lost cluster - losing the cluster must be observed, never assumed.
 */
package hastate

import (
	"suse.com/virtx/pkg/machine"
)

var office_fn func() int64

/*
 * Set_office installs how this host learns the epoch of the office it holds, or 0
 * when it holds none. The coordinator package installs it.
 */
func Set_office(fn func() int64) {
	office_fn = fn
}

/* office_epoch is the epoch of the office this host holds, 0 when it holds none. */
func office_epoch() int64 {
	if (office_fn == nil) {
		return 0
	}
	return office_fn()
}

/*
 * cluster_unreachable answers whether this host has lost the cluster: it cannot
 * reach the host that holds the office, or - when no report claims the office - it
 * cannot reach any peer whose report gives an address.
 */

func cluster_unreachable() bool {
	var (
		pub Published
		p Peer
		holder string = ""
		holder_epoch int64
		probed int
		replied bool
		ran bool
	)
	pub = Published_view()
	/*
	 * When the office is here, "can I reach the coordinator" answers nothing: the
	 * coordinator is this host. What matters then is whether any peer can be reached
	 * at all - and being unable to come to a conclusion is not evidence that the
	 * cluster is there: with nobody to try, or with a probe that will not run, a host
	 * that holds the office has no one to answer for it, which is exactly the
	 * isolation this is about. Measured 2026-10-16: h11 held the office, could reach
	 * nobody, reported judges_silent=1, and this function said false - so isolated was
	 * zeroed and the other hosts could only read fresh-0 and hold. A host with no
	 * peers at all is not isolated: there is no cluster to lose.
	 */
	if (office_epoch() > 0) {
		if (len(pub.Peers) == 0) {
			return false
		}
		for _, p = range pub.Peers {
			if (p.Host_uuid == machine.Uuid() || (p.Mgmt_addr != "" && p.Mgmt_addr == local_mgmt_addr())) {
				/*
				 * This host's own report is in the view - by its uuid, or by the address
				 * it reports for itself when the entry carries no uuid. Reaching ourselves
				 * proves nothing: it is the one answer that is always there.
				 */
				continue
			}
			if (p.Mgmt_addr == "") {
				continue
			}
			replied, ran = ping_once(p.Mgmt_addr)
			if (ran && replied) {
				return false
			}
		}
		return true
	}
	for _, p = range pub.Peers {
		if (p.Host_uuid == machine.Uuid() || (p.Mgmt_addr != "" && p.Mgmt_addr == local_mgmt_addr())) {
			/*
			 * This host's own report is in the view - by its uuid, or by the address
			 * it reports for itself when the entry carries no uuid. Reaching ourselves
			 * proves nothing about the cluster.
			 */
			continue
		}
		if (p.Mgmt_addr == "") {
			continue
		}
		if (p.Office_epoch > holder_epoch) {
			holder_epoch = p.Office_epoch
			holder = p.Mgmt_addr
		}
	}
	if (holder != "") {
		replied, ran = ping_once(holder)
		if (!ran) {
			return false
		}
		return !replied
	}
	for _, p = range pub.Peers {
		if (p.Host_uuid == machine.Uuid() || (p.Mgmt_addr != "" && p.Mgmt_addr == local_mgmt_addr())) {
			/*
			 * This host's own report is in the view - by its uuid, or by the address
			 * it reports for itself when the entry carries no uuid. Reaching ourselves
			 * proves nothing about the cluster.
			 */
			continue
		}
		if (p.Mgmt_addr == "") {
			continue
		}
		replied, ran = ping_once(p.Mgmt_addr)
		if (!ran) {
			return false
		}
		probed++
		if (replied) {
			return false
		}
	}
	return (probed != 0)
}

var iso_self int

/*
 * Self_isolated answers whether this host concluded that it is isolated, as of the
 * last reporting round. It is the same conclusion the report carries, kept here so
 * that the part of this host that holds the office can act on it: a host that is
 * isolated is exactly the host that must not be answering for the cluster.
 */
func Self_isolated() int {
	return iso_self
}
