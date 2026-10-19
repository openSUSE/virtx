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
 * Writing the cluster policy: the one file on the shared registration tree, and
 * the generation rule that goes with writing it.
 *
 * The file is written in one step, like the local copy, so that a host reading
 * it sees either the policy before the change or the one after it - never half
 * of either. The generation is what makes a change identifiable: the writer
 * takes one above everything it knows about (the file it read, and the copy it
 * has applied), and a reader refuses anything older than what it applied, so a
 * host that comes back with an old view cannot put the cluster back.
 */
package policy

import (
	"fmt"
	"strings"

	"suse.com/virtx/pkg/ts"
)

/* Save writes one policy text to the shared file, in one step. */
func Save(text string) error {
	return write_atomic(PATH, []byte(text))
}

/*
 * Next_generation is the generation to write a change under: one above the file
 * that was read and one above the copy this host has applied, whichever is
 * higher. Taking the applied copy into account is what keeps a host that has
 * been away from writing a generation that is older than what the cluster has
 * already seen.
 */
func Next_generation(current *Policy, applied Applied) int64 {
	var (
		n int64
	)
	n = applied.Generation
	if (current != nil && current.Generation > n) {
		n = current.Generation
	}
	return n + 1
}

/*
 * Render returns the text of a policy with the switch set as asked, under a new
 * generation, with every documented setting written out.
 *
 * Writing them all out is deliberate: the file an operator looks at after an
 * enable then says what the cluster is running with, in full, instead of leaving
 * the reader to combine what the file says with the documented defaults. The
 * values come from the policy being changed, so nothing that was configured is
 * dropped by the change.
 */
func Render(p *Policy, enabled bool, generation int64, changed_by string) string {
	var (
		sb strings.Builder
	)
	if (p == nil) {
		p = Defaults()
	}
	sb.WriteString("# the cluster policy of this cluster; documented in etc/virtx/cluster_policy\n")
	sb.WriteString(fmt.Sprintf("%s = %d\n", KEY_GENERATION, generation))
	sb.WriteString(fmt.Sprintf("%s = %q\n", KEY_CHANGED_BY, changed_by))
	sb.WriteString(fmt.Sprintf("%s = %d\n", KEY_CHANGED_AT, ts.Now()))
	sb.WriteString(fmt.Sprintf("%s = %v\n", KEY_HA_ENABLED, enabled))
	sb.WriteString(fmt.Sprintf("%s = %d\n", KEY_HA_HASTATE_REFRESH_SECONDS, p.Hastate_refresh_seconds))
	sb.WriteString(fmt.Sprintf("%s = %d\n", KEY_HA_HASTATE_STALE_SECONDS, p.Hastate_stale_seconds))
	sb.WriteString(fmt.Sprintf("%s = %d\n", KEY_HA_MAX_INFLIGHT, p.Max_inflight))
	sb.WriteString(fmt.Sprintf("%s = %s\n", KEY_HA_ISOLATION_RESPONSE, p.Isolation_response))
	sb.WriteString(fmt.Sprintf("%s = %d\n", KEY_HA_ISOLATION_SECONDS, p.Isolation_seconds))
	sb.WriteString(fmt.Sprintf("%s = %d\n", KEY_HA_ISOLATION_START_INTERVAL_SECONDS, p.Isolation_start_interval_seconds))
	for i, addr := range p.Isolation_addrs {
		sb.WriteString(fmt.Sprintf("%s = %s\n", isolation_key(i), addr))
	}
	return sb.String()
}
