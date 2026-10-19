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
 * What this host concluded about its peers, kept for the operators who ask.
 *
 * The judgement loop decides, logs, and moves on; until now the only way to see
 * a conclusion was to read the log. This is the read-only copy of the last round
 * per peer, so that a request can answer "what does this host think, and on what
 * evidence" without a log reader.
 *
 * The copy carries the round it was made in, because a peer that disappears stops
 * being judged and its last judgement would otherwise look as fresh as the round
 * being served. A reader that cares tells the ages apart.
 */
package verdict

import (
	"sync"

	"suse.com/virtx/pkg/hastate"
	"suse.com/virtx/pkg/ts"
)

/* Judgement is one round of judgement about one peer. */
type Judgement struct {
	Name string
	Host_uuid string
	Row string
	Verdict string
	/* this is the lease evidence the row rests on when it needs one: yes, no, or n/a. */
	E1 string
	Armed bool
	Report_fresh bool
	Lease string
	Report string
	Mgmt_known bool
	Mgmt_probed bool
	Mgmt_reachable bool
	/* Mgmt_addr is the management address this judgement probed; "" when none known. */
	Mgmt_addr string
	Age_ms int64
	Isolation_running_vms []string
	/* Round_ms is when this judgement was made, so a stale one can be told apart. */
	Round_ms int64
}

var (
	judgements_mu sync.Mutex
	judgements []Judgement
)

/*
 * publish_judgement records what the round concluded about one peer, replacing
 * what an earlier round concluded about the same peer.
 */
func publish_judgement(p hastate.Peer, c Columns, row string, verdict string, e1 string, armed bool) {
	var (
		one Judgement
		i int
	)
	one = Judgement{
		Name: p.Name,
		Host_uuid: p.Host_uuid,
		Row: row,
		Verdict: verdict,
		E1: e1,
		Armed: armed,
		Report_fresh: c.Report_fresh,
		Lease: p.Host_state,
		Report: c.Report,
		Mgmt_known: c.Mgmt_known,
		Mgmt_probed: c.Mgmt_probed,
		Mgmt_reachable: c.Mgmt_reachable,
		Mgmt_addr: p.Mgmt_addr,
		Age_ms: p.Age_ms,
		Isolation_running_vms: p.Isolation_running_vms,
		Round_ms: ts.Now(),
	}
	judgements_mu.Lock()
	defer judgements_mu.Unlock()
	for i = range judgements {
		if (judgements[i].Host_uuid == p.Host_uuid) {
			judgements[i] = one
			return
		}
	}
	judgements = append(judgements, one)
}

/* Judgements answers a copy of the last judgements, oldest name first. */
func Judgements() []Judgement {
	var (
		out []Judgement
	)
	judgements_mu.Lock()
	defer judgements_mu.Unlock()
	out = make([]Judgement, len(judgements))
	copy(out, judgements)
	return out
}
