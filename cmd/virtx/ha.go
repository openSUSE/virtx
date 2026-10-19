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

package main

import (
	"fmt"
	"net/http"
	"net/url"
	"time"
	"sort"

	"strings"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/model"
)

func ha_get_req() {
	virtx.path = "/ha"
	virtx.method = "GET"
	virtx.arg = nil
	virtx.result = &openapi.HaState{}
}

/*
 * ha_set_req asks for a change of the switch. The parameter travels as a query
 * string, like the other parameters this tree added after the model was
 * generated: the model is not touched for it.
 */
func ha_set_req(enabled bool) {
	virtx.path = fmt.Sprintf("/ha?enabled=%v", enabled)
	virtx.method = "PUT"
	virtx.arg = nil
	virtx.result = &openapi.HaState{}
}

/*
 * HA_WAIT_DEFAULT is how long enable and disable wait for the acknowledgements of a
 * change before they report where it has got: three seconds, which is a look at the
 * progress and not a gate. A longer wait is asked for with --timeout.
 */
const HA_WAIT_DEFAULT = 3
const HA_WAIT_POLL_MS = 500

/*
 * ha_wait_for_acks waits for the hosts to acknowledge a change of the switch and says
 * where it has got when the wait runs out.
 *
 * It answers the question the operator asked - is the cluster there yet - instead of
 * leaving "PENDING yes" for them to poll by hand. Running out of time is not a
 * failure: the change is on the shared tree and every host converges on it, so the
 * command says which host has not applied it yet and exits 0, and a script that wants
 * a gate passes --timeout and reads the exit code of a later get (measured
 * 2026-10-13: a caller that was simply cancelled after fifteen seconds read that as a
 * failure, which is what this must not do).
 */
func ha_wait_for_acks(st *openapi.HaState) {
	var (
		wait int
		deadline time.Time
		fresh *openapi.HaState
		response *http.Response
		err error
	)
	if (virtx.ha_no_wait || !virtx.ha_wait || !st.Pending) {
		return
	}
	wait = virtx.ha_timeout
	if (virtx.ha_timeout <= 0) {
		wait = HA_WAIT_DEFAULT
	}
	deadline = time.Now().Add(time.Duration(wait) * time.Second)
	for (time.Now().Before(deadline)) {
		time.Sleep(HA_WAIT_POLL_MS * time.Millisecond)
		response, err = httpx.Do_request(virtx.api_server, "GET", "/ha", nil)
		if (err != nil) {
			break
		}
		fresh = &openapi.HaState{}
		_, err = httpx.Decode_response_body(response, fresh)
		if (err != nil) {
			break
		}
		if (!fresh.Pending) {
			/* a wait that succeeded needs no line: the command said what it did */
			return
		}
	}
	/*
	 * Not confirmed is its own word and not PENDING again: PENDING is a state of the
	 * cluster that the reader has just been told about above, and this line is about
	 * this command having waited and got nowhere. Running out is not a failure - the
	 * change is on the tree and every host converges on it - so this exits 0.
	 */
	fmt.Fprintf(virtx.w, "NOT CONFIRMED\tnot every host has applied generation %d yet (use --timeout to wait longer)\n",
		st.Generation)
	for _, uuid := range st.Missing {
		if (st.Names[uuid] != "") {
			fmt.Fprintf(virtx.w, "  not confirmed\t%s (%s)\n", uuid, st.Names[uuid])
			continue
		}
		fmt.Fprintf(virtx.w, "  not confirmed\t%s\n", uuid)
	}
}

/* HA_SWITCH_WAIT_DEFAULT is how long a switch waits for the office to move. */
const HA_SWITCH_WAIT_DEFAULT = 15

/* ha_switch_req asks the coordinator office to move to one host. */
func ha_switch_req(host string) {
	virtx.path = "/ha?coordinator=" + url.QueryEscape(host)
	virtx.method = "PUT"
	virtx.arg = nil
	virtx.result = &openapi.HaState{}
}

/* ha_is_host answers whether the state names that host as the coordinator. */
func ha_is_host(st *openapi.HaState, host string) bool {
	return (st.Coordinator == host || st.Coordinator_name == host)
}

/*
 * ha_wait_for_coordinator waits for the office to move to the host that was asked
 * for, and prints where it has got when the wait runs out.
 *
 * Running out is not a failure of the request: the request is on the shared tree,
 * and the holder serves it on its next patrol, so a target that is slow, or that
 * is itself being restarted, only delays it. The command prints the state as it
 * is; a caller that wants a gate passes --timeout and reads "get ha" afterwards.
 */
func ha_wait_for_coordinator(host string, st *openapi.HaState) {
	var (
		wait int
		deadline time.Time
		response *http.Response
		fresh *openapi.HaState
		err error
	)
	if (virtx.ha_no_wait) {
		ha_show(st)
		fmt.Fprintf(virtx.w, "REQUESTED\t%s: the office is asked to move; use \"virtx get ha\" to watch it\n", host)
		return
	}
	wait = virtx.ha_timeout
	if (wait <= 0) {
		wait = HA_SWITCH_WAIT_DEFAULT
	}
	deadline = time.Now().Add(time.Duration(wait) * time.Second)
	fresh = nil
	for (time.Now().Before(deadline)) {
		time.Sleep(HA_WAIT_POLL_MS * time.Millisecond)
		response, err = httpx.Do_request(virtx.api_server, "GET", "/ha", nil)
		if (err != nil) {
			break
		}
		fresh = &openapi.HaState{}
		_, err = httpx.Decode_response_body(response, fresh)
		if (err != nil) {
			break
		}
		if (ha_is_host(fresh, host)) {
			break
		}
	}
	if (fresh == nil) {
		ha_show(st)
		return
	}
	ha_show(fresh)
	if (!ha_is_host(fresh, host)) {
		fmt.Fprintf(virtx.w, "NOT CONFIRMED\t%s is not the coordinator yet: the request stays on the shared tree until it runs out\n", host)
	}
}

/* ha_show prints one switch state: the answer to get, enable and disable alike. */
func ha_show(st *openapi.HaState) {
	var (
		surface string = "off"
	)
	if (st.Enabled) {
		surface = "on"
	}
	fmt.Fprintf(virtx.w, "HA RESPONSE\t%s\n", surface)
	/*
	 * The office epoch is the lease generation of the office: how many times it has
	 * changed hands. That answers nothing a reader of this line asked - the holder is
	 * the answer - so it is kept for -D, where "did the office just move" is the
	 * question being asked.
	 */
	if (st.Coordinator != "") {
		if (st.Coordinator_name != "") {
			fmt.Fprintf(virtx.w, "COORDINATOR\t%s (%s)", st.Coordinator_name, st.Coordinator)
		} else {
			fmt.Fprintf(virtx.w, "COORDINATOR\t%s", st.Coordinator)
		}
		if (virtx.debug) {
			fmt.Fprintf(virtx.w, ", office epoch %d", st.Coordinator_epoch)
		}
		fmt.Fprintf(virtx.w, "\n")
	} else {
		fmt.Fprintf(virtx.w, "COORDINATOR\tnone\n")
	}
	if (st.Isolated) {
		/*
		 * Said before the judgements, because a reader who does not know this will read
		 * the partitioned lines below as those hosts' fault. This host cannot reach them;
		 * what it says about them is what the shared ledger says, and nothing more.
		 */
		fmt.Fprintf(virtx.w, "THIS HOST\t|isolated: it cannot reach the management network or its judges; its view of the others is the shared ledger only\n")
	}
	fmt.Fprintf(virtx.w, "GENERATION\t%d\n", st.Generation)
	/*
	 * Which host has applied which generation. "here" answered a question nobody
	 * asked while hiding the one that matters - which host is behind - so the answer
	 * is one line per host, by name.
	 */
	if (len(st.Applied_by) != 0) {
		var (
			names []string
			parts []string
			name string
		)
		for name = range st.Applied_by {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name = range names {
			parts = append(parts, fmt.Sprintf("%s: %d", name, st.Applied_by[name]))
		}
		fmt.Fprintf(virtx.w, "APPLIED\t|%s\n", strings.Join(parts, ", "))
	}
	fmt.Fprintf(virtx.w, "ACKED\t%d\n", len(st.Acked))
	/*
	 * Two different things read as one word before: a generation of 0 means no policy
	 * has been agreed yet, so what is missing is the baseline itself and not a change
	 * somebody asked for. PENDING is kept for a change that really is on its way.
	 */
	if (st.Generation == 0 && st.Pending) {
		fmt.Fprintf(virtx.w, "BASELINE\tyes: waiting for the first reports\n")
	} else if (st.Generation > 0 && st.Pending) {
		fmt.Fprintf(virtx.w, "PENDING\tyes: waiting for generation %d\n", st.Generation)
	}
	fmt.Fprintf(virtx.w, "NOT YET\t%d\n", len(st.Missing))
	for _, uuid := range st.Missing {
		if (st.Names[uuid] != "") {
			fmt.Fprintf(virtx.w, "  waiting for\t%s (%s)\n", uuid, st.Names[uuid])
			continue
		}
		fmt.Fprintf(virtx.w, "  waiting for\t%s\n", uuid)
	}
	/*
	 * What this host concluded about its peers. Only what asks for something is
	 * printed: a cluster where every judgement is a no-op stays as quiet as it
	 * was before this line existed.
	 */
	for _, j := range st.Judgements {
		if (j.Verdict == "" || j.Verdict == "no-op") {
			continue
		}
		fmt.Fprintf(virtx.w, "%s\n", ha_judgement_line(j))
	}
}

/*
 * ha_judgement_line is the one line an operator reads about one judgement, in
 * the words of what it asks for: an alert is for a person, a hold is a decision
 * the cluster is not taking, and an action is one it did take.
 */
func ha_judgement_line(j openapi.HaJudgement) string {
	var (
		what string = "hold"
		text string
	)
	if (j.Verdict == "alert-only") {
		what = "alert"
	}
	if (j.Verdict == "restart") {
		what = "action"
	}
	text = fmt.Sprintf("%s\t%s: %s", what, j.Name, j.Row)
	if (j.Row == "StorageUnreadable" || j.Row == "PartitionStorageUnreadable") {
		/*
		 * A row that says a record could not be read needs one more clause, or a reader
		 * takes it for a verdict and goes looking at the storage: what it means is that
		 * nothing is known about that host and nothing is done, which is the same
		 * stance as report unreadable - and it is usually one round of a directory
		 * entry that is momentarily not there.
		 */
		text += " - its lease record could not be read: nothing is known about it, nothing is done"
	}
	/*
	 * The four columns below are the evidence a judgement rests on, not the answer:
	 * what it asks for is in the row and in the words that follow ("needs an
	 * operator", "offers N vm(s) it runs"). They are kept for -D, where the question
	 * is why the judgement came out that way - and they are in the log too, where the
	 * row change prints them.
	 */
	if (virtx.debug && j.Report != "") {
		text += fmt.Sprintf(", report %s", j.Report)
	}
	if (virtx.debug && j.Lease != "") {
		/* "host" lease: this is the sanlock lease of the host itself, not the VM's */
		text += fmt.Sprintf(", host lease %s", j.Lease)
	}
	/*
	 * The management-network evidence, like the other evidence columns, waits for -D:
	 * what was measured is the path to that host's management address and not the host
	 * itself - it may be alive with a broken path, or alive with a service that does
	 * not answer - and the address that was probed matters when someone goes to fix
	 * that path. The row is what the reader acts on; the evidence is what they ask for.
	 */
	if (virtx.debug) {
		if (!j.Mgmt_known) {
			text += ", mgmt unknown"
		} else if (j.Mgmt_reachable) {
			text += ", mgmt reachable"
		} else if (j.Mgmt_addr != "") {
			text += fmt.Sprintf(", mgmt network unreachable: no reply from %s", j.Mgmt_addr)
		} else {
			text += ", mgmt network unreachable"
		}
	}
	if (virtx.debug) {
		text += fmt.Sprintf(", age %ds", j.Age_ms / 1000)
	}
	if (j.E1 != "" && j.E1 != "n/a") {
		/*
		 * The lease evidence in words: the field e1 says whether every
		 * disk lease of the VMs that host runs is free. It reads as the question a
		 * person asks - does anything still hold those disks - and the raw value
		 * stays available in the fields the API returns.
		 */
		/*
		 * "virtual disk" leases: the cooperative leases libvirt holds on the managed
		 * disks of the VMs that host runs - a different thing from its host lease.
		 */
		if (j.E1 == "yes") {
			text += ", virtual disk leases free - nothing holds the disks of its VMs"
		} else if (j.E1 == "no") {
			text += ", virtual disk leases held - that host still holds the disks of its VMs"
		} else if (j.E1 != "") {
			text += fmt.Sprintf(", virtual disk leases %s", j.E1)
		}
	} else if (j.E1 == "n/a" && (j.Row == "Isolated" || j.Verdict == "restart")) {
		/*
		 * Nothing to judge: the registrations of that host's VMs are not under it any more
		 * (a claim moved them), so "held" would be a lie and silence would leave the reader
		 * wondering. A row that asks for nothing says nothing.
		 */
		text += ", virtual disk leases unknown - that host no longer holds the registrations (claimed)"
	}
	if (len(j.Isolation_running_vms) != 0) {
		text += fmt.Sprintf(", offers %d vm(s) it runs", len(j.Isolation_running_vms))
	}
	if (j.Row == "Partitioned") {
		text += " - needs an operator"
	}
	return text
}
