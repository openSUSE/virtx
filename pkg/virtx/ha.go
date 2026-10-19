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
package virtx

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"

	"suse.com/virtx/pkg/coord"
	"suse.com/virtx/pkg/ha"
	"suse.com/virtx/pkg/hastate"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/policy"
	"suse.com/virtx/pkg/verdict"
)

/* ha_state_of renders one switch state for the client. */
func ha_state_of(st ha.State) openapi.HaState {
	var (
		o openapi.HaState
	)
	o.Enabled = st.Enabled
	o.Generation = st.Generation
	o.Applied = st.Applied
	o.Acked = st.Acked
	o.Missing = st.Missing
	o.Pending = st.Pending
	o.Names = ha_names_of(append(append([]string(nil), st.Acked...), st.Missing...))
	o.Coordinator, o.Coordinator_name, o.Coordinator_epoch = coordinator_of(st)
	/*
	 * Whether the host that answered is itself cut off. The reader of the lines below
	 * needs it: an isolated host cannot reach the others at all, so it reports every one
	 * of them as partitioned from itself, and that says nothing about their health.
	 */
	o.Isolated = (hastate.Self_isolated() != 0)
	o.Applied_by = applied_by_of(st)
	o.Judgements = ha_judgements_of(st.Judgements)
	return o
}

/*
 * ha_names_of is what the inventory calls the hosts of a state, so that a client
 * can say which host is behind instead of printing a uuid and leaving the reader
 * to look it up.
 */
/*
 * applied_by_of answers the policy generation each host has applied, by host name.
 * One number for "here" cannot answer the operator's question, which is which host is
 * behind; this host's own answer is the one it reported the switch with, and the peers'
 * answers are what their reports say (0 when a peer could not read it).
 */
func applied_by_of(st ha.State) map[string]int64 {
	var (
		pub hastate.Published
		peer hastate.Peer
		names map[string]string
		out map[string]int64
		name string
	)
	names = ha_names_of(append(append([]string(nil), st.Acked...), st.Missing...))
	out = make(map[string]int64)
	name = names[machine.Uuid()]
	if (name == "") {
		name = machine.Uuid()
	}
	out[name] = st.Applied
	pub = hastate.Published_view()
	for _, peer = range pub.Peers {
		name = names[peer.Host_uuid]
		if (name == "") {
			name = peer.Name
		}
		if (name == "") {
			name = peer.Host_uuid
		}
		out[name] = peer.Policy_gen
	}
	return out
}

/*
 * coordinator_of answers who holds the coordinator office: this host when its own
 * office epoch says so, otherwise the peer whose report carries the highest one -
 * the same reading the cluster uses to decide who may act. An office nobody holds
 * is an empty answer, not a guess.
 */
func coordinator_of(st ha.State) (string, string, int64) {
	var (
		pub hastate.Published
		peer hastate.Peer
		names map[string]string
		uuid string
		name string
		best int64
	)
	names = ha_names_of(append(append([]string(nil), st.Acked...), st.Missing...))
	if epoch, ok := coord.Term(); ok {
		return machine.Uuid(), names[machine.Uuid()], epoch
	}
	pub = hastate.Published_view()
	for _, peer = range pub.Peers {
		if (peer.Office_epoch > best) {
			best = peer.Office_epoch
			uuid = peer.Host_uuid
			name = names[peer.Host_uuid]
		}
	}
	return uuid, name, best
}

func ha_names_of(uuids []string) map[string]string {
	var (
		names map[string]string = make(map[string]string)
		info inventory.HostInfo
		err error
	)
	for _, uuid := range uuids {
		info, err = inventory.Get_hostinfo(uuid)
		if (err == nil && info.Name != "") {
			names[uuid] = info.Name
		}
	}
	return names
}

/* ha_response encodes one switch state and answers with it. */
func ha_response(w http.ResponseWriter, st ha.State) {
	var (
		o openapi.HaState
		buf bytes.Buffer
		err error
	)
	o = ha_state_of(st)
	err = json.NewEncoder(&buf).Encode(&o)
	if (err != nil) {
		logger.Log("ha: could not encode the state: %s", err.Error())
		http.Error(w, "failed to encode JSON", http.StatusInternalServerError)
		return
	}
	httpx.Do_response(w, http.StatusOK, &buf)
}

/*
 * ha_get answers GET /ha: the switch of the action surface, and which hosts
 * have applied the generation it belongs to.
 */
func ha_get(w http.ResponseWriter, r *http.Request) {
	ha_response(w, ha.Get())
}

/*
 * ha_put answers PUT /ha?enabled=0|1. It writes the switch and waits for the
 * hosts to apply it.
 *
 * Only the coordinator writes: a host that does not hold the office answers 409
 * and says what it sees about the office, so that the operator learns where the
 * command has to go instead of getting a change that silently lost a race.
 *
 * A change that not everybody has applied yet is not an error: the answer says
 * which host is behind, and the change is still there to be finished - by the
 * coordinator on its next round, or by whoever takes the office next.
 */
func ha_put(w http.ResponseWriter, r *http.Request) {
	if (r.URL.Query().Get("coordinator") != "") {
		ha_switch(w, r.URL.Query().Get("coordinator"))
		return
	}
	var (
		arg string
		enable bool
		st ha.State
		err error
	)
	arg = r.URL.Query().Get("enabled")
	if (arg == "") {
		http.Error(w, "no enabled=0|1 given", http.StatusBadRequest)
		return
	}
	enable, err = strconv.ParseBool(arg)
	if (err != nil) {
		http.Error(w, "enabled must be 0|1", http.StatusBadRequest)
		return
	}
	if (enable) {
		st, err = ha.Enable()
	} else {
		st, err = ha.Disable()
	}
	if (err != nil) {
		if (errors.Is(err, ha.Err_not_coordinator)) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		logger.Log("ha: %s", err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ha_response(w, st)
}

/*
 * ha_switch answers PUT /ha?coordinator=<host>: ask the coordinator office to
 * move to that host.
 *
 * Any host may receive the request: it lives on the shared registration tree, and
 * the current holder is what serves it - it gives the office back, and the host
 * that was named takes it on its next patrol. The other hosts stay out of the
 * contest until the request has been served or has run out (pkg/coord).
 *
 * The action surface has to be on, because an office that moves is an office that
 * acts. A change of the surface that is still on its way counts as on, because the
 * request that turns it on is exactly what the office serves - an operator who
 * enables HA and moves the office in a row must not be told that HA is off. A
 * change that is turning the surface off is refused: moving the office of a
 * cluster that is stopping is not a request to honour.
 */
func ha_switch(w http.ResponseWriter, host string) {
	var (
		p *policy.Policy
		req policy.Request
		pending bool
		info inventory.HostInfo
		ok bool
		err error
	)
	p, err = policy.Load(policy.PATH)
	if (err != nil && !errors.Is(err, os.ErrNotExist)) {
		http.Error(w, "the cluster policy cannot be read", http.StatusInternalServerError)
		return
	}
	req, pending, _ = policy.Load_request()
	if (pending && !req.Enabled) {
		http.Error(w, "the action surface is being turned off: the office is not moved", http.StatusConflict)
		return
	}
	if (!(pending && req.Enabled) && (p == nil || !p.Ha_enabled)) {
		http.Error(w, "the action surface is off: enable it with \"virtx enable ha\" first", http.StatusConflict)
		return
	}
	info, ok = inventory.Find_host(host)
	if (!ok) {
		http.Error(w, "no such host: " + host, http.StatusNotFound)
		return
	}
	if (info.Uuid == machine.Uuid()) {
		if _, mine := coord.Term(); mine {
			ha_response(w, ha.Get())
			return
		}
	}
	err = coord.Save_switch(info.Uuid, machine.Uuid())
	if (err != nil) {
		logger.Log("ha: %s cannot be written: %s", coord.SWITCH_PATH, err.Error())
		http.Error(w, "the switch request cannot be written", http.StatusInternalServerError)
		return
	}
	logger.Log("ha: the coordinator office is asked to move to %s (%s)", info.Name, info.Uuid)
	ha_response(w, ha.Get())
}

/* ha_judgements_of turns the judgements of this host into what the API carries. */
func ha_judgements_of(in []verdict.Judgement) []openapi.HaJudgement {
	var (
		out []openapi.HaJudgement
		j verdict.Judgement
	)
	out = make([]openapi.HaJudgement, 0, len(in))
	for _, j = range in {
		out = append(out, openapi.HaJudgement{
			Name: j.Name,
			Uuid: j.Host_uuid,
			Row: j.Row,
			Verdict: j.Verdict,
			E1: j.E1,
			Armed: j.Armed,
			Report_fresh: j.Report_fresh,
			Lease: j.Lease,
			Report: j.Report,
			Mgmt_known: j.Mgmt_known,
			Mgmt_probed: j.Mgmt_probed,
			Mgmt_reachable: j.Mgmt_reachable,
			Mgmt_addr: j.Mgmt_addr,
			Age_ms: j.Age_ms,
			Isolation_running_vms: j.Isolation_running_vms,
			Round_ms: j.Round_ms,
		})
	}
	return out
}
