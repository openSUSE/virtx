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
 * Package ha is the switch of the action surface: whether the hosts of this
 * cluster may act on a judgement, and the change of that answer.
 *
 * The switch is the cluster policy's ha_enabled, so the answer is a file on the
 * shared registration tree, and turning it on or off is a change of that file.
 * One host writes it - the one that holds the coordinator office, because the
 * office is what makes a host the one that may act - and every host
 * acknowledges the change by applying it, which changes the generation in its
 * own report (pkg/hastate). A change is therefore not a broadcast: it is a state
 * that the hosts converge on, and the writer's job is to say when they have.
 *
 * Three properties are deliberate.
 *
 * The writer is the coordinator and nobody else. A host that does not hold the
 * office refuses and says so; two writers would race on the same file, and the
 * generation rule would turn that race into one host's change silently winning.
 *
 * A change that is not acknowledged by everybody is not an error. The file says
 * what the cluster should be doing, the acks say how far it has got. A change
 * left half applied is therefore still there to be finished - by the same
 * coordinator on its next round, or by whoever takes the office next - and the
 * operator is told which host has not applied it yet, instead of being told that
 * the command failed.
 *
 * Turning the switch off clears what the change left behind on the hosts: their
 * self reports, and nothing else. The reports are the only state this cluster
 * keeps about a change of the switch, and a removal list that grows is how a
 * cleanup ends up deleting something it should not.
 */
package ha

import (
	"errors"
	"sync"
	"fmt"
	"os"
	"time"

	"suse.com/virtx/pkg/coord"
	"suse.com/virtx/pkg/hastate"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/policy"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/verdict"
)

/*
 * Err_not_coordinator is returned by a change this host may not make: the office
 * is held by another host, and only its holder writes the policy.
 */
var Err_not_coordinator = errors.New("the coordinator office is held by another host")

/* ACK_ROUNDS is how many reporting rounds an acknowledgement may take. */
const ACK_ROUNDS = 4

/*
 * State is what the cluster is doing about the action surface: the switch, the
 * generation it belongs to, and which hosts have applied that generation.
 */
type State struct {
	Enabled bool
	Generation int64
	Applied int64
	Acked []string
	Missing []string
	Pending bool
	/* Judgements is what this host concluded about its peers, for the operators. */
	Judgements []verdict.Judgement
}

/*
 * Enabled is whether the policy this host has applied has the action surface on.
 * It is the answer the decision layer uses, and the one a command that belongs
 * to the action surface has to ask for before it does anything: it is a local
 * snapshot, so it cannot be talked out of by a host that has lost the shared
 * tree - and a host that has lost it has no policy, which reads as off.
 */
func Enabled() bool {
	var (
		pub hastate.Published
	)
	pub = hastate.Published_view()
	if (pub.Policy == nil) {
		return false
	}
	return pub.Policy.Ha_enabled
}

/*
 * Get reports the switch of the policy on the shared tree, and which hosts have
 * applied that generation. It changes nothing, and what it cannot read it
 * reports as the documented defaults.
 */
func Get() State {
	var (
		p *policy.Policy
		err error
		st State
	)
	/*
	 * A cluster that has no policy yet is a cluster whose switch is off: that is
	 * the documented default, and not a failure to report (measured 2026-10-13:
	 * every report on such a cluster logged a line that read like an error).
	 */
	p, err = policy.Load(policy.PATH)
	if (err != nil && !errors.Is(err, os.ErrNotExist)) {
		logger.Log("ha: %s cannot be read: %s", policy.PATH, err.Error())
	}
	st.Enabled = p.Ha_enabled
	st.Generation = p.Generation
	return collect(st)
}

/* Enable turns the action surface on: write the policy, then wait for the acks. */
/*
 * init installs the half that writes a change of the action surface.
 *
 * Every virtxd needs it, and not only the process an operator talked to: a request
 * is served by whoever holds the office, which is another host in general.
 * Installing it when a request is made left the host that took the office because
 * of that request with no writer at all, so the request was never served and the
 * switch never changed (measured 2026-10-13: a request made on h11 moved the
 * office to h12, and h12 wrote nothing at all).
 */
func init() {
	coord.Set_change_writer(Apply_pending)
}

func Enable() (State, error) {
	return Request(true)
}

/*
 * Request registers a change of the action surface and answers at once.
 *
 * The change is written by whoever holds the coordinator office, and this host
 * now has a reason to hold it: a request this host cannot write yet is written in
 * the next round in which it holds the office. The answer is the state as it is, with
 * Pending set: registering a change is not waiting for it, and the caller chooses -
 * virtx's enable and disable wait for the acknowledgements and report where they have
 * got, and a caller that does not want to wait reads Pending and asks again later
 * (measured 2026-10-13: a caller that was simply cancelled after fifteen seconds read
 * that as a failure, which is why waiting is the caller's decision and not built in).
 */
func Request(enabled bool) (State, error) {
	var (
		st State
		err error
	)
	request_mu.Lock()
	request_enabled = enabled
	request_asked = true
	request_mu.Unlock()
	coord.Request_change()
	/*
	 * The shared tree carries the request, so that the office serves it whichever
	 * host it is on: asking is not the same as writing, and the host that was asked
	 * is not necessarily the host that holds the office.
	 */
	err = policy.Save_request(enabled, machine.Uuid())
	if (err != nil) {
		return State{}, fmt.Errorf("%s cannot be written (%s)", policy.REQUEST_PATH, err.Error())
	}
	st = Get()
	st.Enabled = enabled
	st.Pending = true
	return st, nil
}

/*
 * Apply_pending writes the change that was asked for. The coordinator's round
 * calls it while this host holds the office; it answers whether the request is
 * settled - a write that fails stays pending and is tried again next round.
 */
func Apply_pending() bool {
	var (
		enabled bool
		asked bool
		ok bool
		st State
		err error
		req policy.Request
	)
	request_mu.Lock()
	enabled = request_enabled
	asked = request_asked
	request_mu.Unlock()
	if (!asked) {
		/* the request may have been made on another host: it is in the shared tree */
		req, ok, err = policy.Load_request()
		if (err != nil || !ok) {
			return true
		}
		enabled = req.Enabled
	}
	st, err = change(enabled)
	if (err != nil) {
		logger.Log("ha: the change that was asked for cannot be written yet: %s", err.Error())
		return false
	}
	request_mu.Lock()
	request_asked = false
	request_mu.Unlock()
	/* the request has been served: no host has to serve it again */
	err = policy.Clear_request()
	if (err != nil) {
		logger.Log("ha: the served request in %s cannot be removed: %s", policy.REQUEST_PATH, err.Error())
	}
	if (!enabled) {
		if (!st.Pending) {
			clear_reports()
		} else {
			go settle_reports(st)
		}
	}
	return true
}

/*
 * Disable turns it off, waits for the acks, and then clears what the change left
 * behind on the hosts: their self reports. A change that is still on its way is
 * reported as such, and the reports are then left alone.
 */
func Disable() (State, error) {
	return Request(false)
}

var (
	request_mu sync.Mutex
	request_enabled bool
	request_asked bool
)

/*
 * settle_reports waits for the hosts to apply the generation that is already
 * written, and then erases their reports. It is the second half of a disable: it
 * never blocks a caller and it never fails - what it cannot do, it leaves.
 */
func settle_reports(st State) {
	var (
		s State
		err error
	)
	s, err = wait(st)
	if (err != nil || s.Pending) {
		return
	}
	clear_reports()
}

/*
 * change writes the switch and waits for the hosts to apply it. The generation
 * is one above everything this host knows about, so that a change made while a
 * host was away is never older than what the cluster has already seen.
 */
func change(enabled bool) (State, error) {
	var (
		p *policy.Policy
		applied policy.Applied
		gen int64
		err error
		st State
		mine bool
	)
	_, mine = coord.Term()
	if (!mine) {
		return State{}, fmt.Errorf("%w: %s", Err_not_coordinator, coord.Result())
	}
	p, err = policy.Load(policy.PATH)
	if (err != nil && !errors.Is(err, os.ErrNotExist)) {
		/* a change is built on the policy that is there; an unreadable one is not */
		return State{}, fmt.Errorf("%s cannot be read (%s)", policy.PATH, err.Error())
	}
	if (p == nil) {
		p = policy.Defaults()
	}
	if (p.Ha_enabled == enabled) {
		/*
		 * The change that was asked for is the change already in force: two hosts
		 * may serve the same request while the office moves, and a second
		 * generation for the same switch would only be churn.
		 */
		st = State{ Enabled: enabled, Generation: p.Generation }
		return collect(st), nil
	}
	applied.Generation = applied_generation()
	gen = policy.Next_generation(p, applied)
	err = policy.Save(policy.Render(p, enabled, gen, machine.Uuid()))
	if (err != nil) {
		return State{}, fmt.Errorf("%s cannot be written (%s)", policy.PATH, err.Error())
	}
	logger.Log("ha: the action surface is %v now, generation %d written by this host", enabled, gen)
	st = State{ Enabled: enabled, Generation: gen }
	/*
	 * The answer is given as soon as the change is written: whether every host has
	 * applied it yet is what it carries, not something it waits for. The caller is
	 * an operator on the other end of an HTTP request, and that request is given
	 * up long before ACK_ROUNDS reporting rounds are over (measured 2026-10-13:
	 * "context canceled" after fifteen seconds, on a change that had been written
	 * and acknowledged while the caller was still listening).
	 */
	st = collect(st)
	return st, nil
}

/*
 * wait waits for the hosts to apply the generation, up to ACK_ROUNDS reporting
 * rounds of the policy's own pace. Running out of that is not a failure: it
 * means the change is still on its way, and the state says to whom.
 */
func wait(st State) (State, error) {
	var (
		seconds int
		deadline time.Time
	)
	seconds = ack_seconds()
	deadline = time.Now().Add(time.Duration(seconds) * time.Second)
	for {
		st = collect(st)
		if (!st.Pending) {
			return st, nil
		}
		if (time.Now().After(deadline)) {
			logger.Log("ha: the action surface is %v, but %d host(s) have not applied generation %d yet",
				st.Enabled, len(st.Missing), st.Generation)
			return st, nil
		}
		time.Sleep(time.Second)
	}
}

/*
 * collect fills in which hosts have applied the generation, from the reports
 * they publish: a report we cannot read, or one that stood still for too long,
 * is not an acknowledgement - it is a host whose mind we do not know.
 */
func collect(st State) State {
	var (
		pub hastate.Published
		p hastate.Peer
	)
	pub = hastate.Published_view()
	st.Acked = nil
	st.Missing = nil
	for _, p = range pub.Peers {
		if (p.Name == "me") {
			st.Applied = p.Policy_gen
		}
		if (p.Report != hastate.REPORT_FRESH_0 && p.Report != hastate.REPORT_FRESH_1) {
			st.Missing = append(st.Missing, p.Host_uuid)
			continue
		}
		if (p.Policy_gen < st.Generation) {
			st.Missing = append(st.Missing, p.Host_uuid)
			continue
		}
		st.Acked = append(st.Acked, p.Host_uuid)
	}
	st.Pending = (len(st.Missing) != 0)
	st.Judgements = verdict.Judgements()
	return st
}

/* applied_generation is the generation this host has applied, from our own report. */
func applied_generation() int64 {
	var (
		pub hastate.Published
		p hastate.Peer
	)
	pub = hastate.Published_view()
	for _, p = range pub.Peers {
		if (p.Name == "me") {
			return p.Policy_gen
		}
	}
	return 0
}

/* ack_seconds is how long the acknowledgements may take: a few reporting rounds. */
func ack_seconds() int {
	var (
		pub hastate.Published
	)
	pub = hastate.Published_view()
	if (pub.Policy != nil && pub.Policy.Hastate_refresh_seconds > 0) {
		return ACK_ROUNDS * pub.Policy.Hastate_refresh_seconds
	}
	return ACK_ROUNDS * policy.DEFAULT_HASTATE_REFRESH_SECONDS
}

/*
 * clear_reports removes the self report of every known host. The reports are the
 * only state a change of the switch leaves behind, and they are also what a
 * reader uses to see whether a host is alive - so they are rebuilt by the hosts
 * themselves within one round: the removal is about the change, not about the
 * hosts.
 */
func clear_reports() {
	var (
		uuids []string
		err error
	)
	uuids, err = reg.Hosts()
	if (err != nil) {
		logger.Log("ha: could not list the hosts to clear their reports: %s", err.Error())
		return
	}
	for _, uuid := range uuids {
		err = os.Remove(reg.Hastate_file(uuid))
		if (err == nil) {
			logger.Log("ha: cleared the self report of %s", uuid)
			continue
		}
		if (!errors.Is(err, os.ErrNotExist)) {
			logger.Log("ha: could not clear the self report of %s: %s", uuid, err.Error())
		}
	}
}
