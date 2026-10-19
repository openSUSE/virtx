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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"suse.com/virtx/pkg/config"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/lockman"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/paths"
	"suse.com/virtx/pkg/policy"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/ts"
)

/*
 * This is the read-only half of the decision layer: every host writes its own
 * self report (hastate) and only reads the reports of the others.
 * Its whole purpose is to tell "my own network is broken" (isolated) apart from
 * "the peer is unreachable, but my network is fine" (partitioned), by probing a
 * judge that is not a cluster member and does not live on the storage network.
 *
 * It takes no action at all: no fencing, no takeover, no change to the sanlock
 * configuration, and it writes nothing but its own hastate file. Acting on what
 * is seen here is the decision layer's job, not this one's.
 *
 * Freshness is judged only by what THIS host has watched change: the window
 * opens when we see a report change and never when the report says it was
 * written. A peer's clock is never part of the verdict - a clock that lags ours
 * would make a peer look stale right after it wrote, and one that runs ahead
 * would make it look fresh forever.
 *
 * The observation runs in its own goroutine so that a stalled read can not
 * delay this host's own self report. Writing that report needs the storage
 * itself, so a host that has lost the storage still can not report it: the file
 * just stops being refreshed and the others see it as stale. This is why the design
 * keeps "unreadable" and "stale" apart, and why neither of them may be
 * read as "it is dead". The report is also on the same medium as the sanlock
 * leases, so it is not an independent source of evidence.
 *
 * Two declared deviations from the design are noted where they are implemented: the
 * "probed" field, and reporting "read it but it is unusable" as unreadable.
 */

/* the values of the reason field defined in the design */
const (
	REASON_RUNNING  = "running"
	REASON_ISOLATED = "isolated"
	/* reserved: not produced by the read-only version */
	REASON_SHUTTING_DOWN    = "shutting-down"
	REASON_STORAGE_DETACHED = "storage-detached"
	REASON_MAINTENANCE      = "maintenance"
)

/* how the others' self report is classified (five states) */
const (
	REPORT_FRESH_0    = "fresh-0"
	REPORT_FRESH_1    = "fresh-1"
	REPORT_STALE      = "stale"
	REPORT_UNREADABLE = "unreadable"
	REPORT_ABSENT     = "absent"
)

/* REPORT_MAX is the largest self report we are willing to parse, in bytes. */
const REPORT_MAX = 4096

/* how reading a peer's report went */
const (
	read_ok      = iota	/* we have a usable report */
	read_missing		/* there is no file (absent) */
	read_denied		/* the file exists but we can not read it (unreadable) */
	read_broken		/* we read it, but it is not a usable report (unreadable) */
)

/*
 * State is one host's self report: one JSON object with the fields of the design.
 * Boot and heartbeat are not spelled out there; this version defines them
 * as "boot time in ms, 0 while unknown" and "uptime in seconds, 0 while
 * unknown", so that a reader can see a reboot (boot grows, heartbeat falls)
 * as opposed to a stall (neither moves).
 *
 * probed is a declared addition to the field list: without it, "the
 * judge was reachable" and "no judge was ever probed" both look like
 * isolated=0, and a reader would draw a conclusion this host never reached.
 */
type State struct {
	Uuid      string `json:"uuid"`
	Boot      int64  `json:"boot"`
	Ts        int64  `json:"ts"`
	Isolated  int    `json:"isolated"`
	Probed    int    `json:"probed"`
	/* Isolation_running_vms are the VMs this host runs while it is isolated: what
	   the cluster may claim, and the only VMs it may then stop. */
	Isolation_running_vms []string `json:"isolation_running_vms,omitempty"`
	/* Policy_gen is the generation of the cluster policy this host applied. */
	Policy_gen int64 `json:"policy_gen"`
	/* Mgmt_addr is this host's own management address, so that a peer can reach it
	   even when the peer's serf view has no address for it. */
	Mgmt_addr string `json:"mgmt_addr,omitempty"`
	/* Judges_silent is the raw fact: a probe ran and none of the judge addresses
	   answered. Isolated is the conclusion, and it needs the cluster to be lost as
	   well - the two must not be read as the same thing. */
	Judges_silent int `json:"judges_silent"`
	/* Office_epoch is the epoch of the office this host holds, 0 for none. */
	Office_epoch int64 `json:"office_epoch,omitempty"`
	Reason    string `json:"reason"`
	Heartbeat int64  `json:"heartbeat"`
}

/*
 * Peer is what this host sees of another host. Age_ms is how long WE have
 * watched this report stand still; it is 0 whenever there is no usable report
 * to watch (absent, or unreadable).
 */
type Peer struct {
	Name       string
	Host_uuid  string
	Host_id    uint64
	Host_state string /* our sanlock view: LIVE/UNKNOWN/UNKNOWN_OLD/FAIL/FREE/DEAD, "unknown" if we could not ask */
	Report     string /* fresh-0/fresh-1/stale/unreadable/absent */
	Reason     string
	Probed     int    /* the peer's own probed flag: 0 means it never probed a judge */
	/* Policy_gen is the generation the peer says it has applied, unreadable as 0. */
	Policy_gen int64
	/* Mgmt_addr is the management address the peer reports for itself, "" if none. */
	Mgmt_addr string
	/* Office_epoch is the office the peer says it holds, 0 when it says none. */
	Office_epoch int64
	Ts         int64
	Age_ms     int64
	Note       string
	/* Isolation_running_vms are the VMs the peer says it runs while isolated. */
	Isolation_running_vms []string
}

/* seen is our own memory of one peer's report: we judge freshness by watching it change. */
type seen struct {
	fingerprint string
	since       int64
}

/* view is what the observation goroutine last saw; the report writer never blocks on it. */
type view struct {
	ok         bool
	ts         int64	/* when this observation was made (our clock, epoch ms) */
	addrs      []string
	addrs_err  error
	peers      []Peer
	peers_err  error
	leases     []lockman.Resource_lease
	unreadable int
	skipped    int
	lease_err  error
	policy     *policy.Policy
	origin     policy.Origin
	policy_gen int64
}

type reader struct {
	me   string
	seen map[string]seen
	/* the reads, as fields so that a test can replace them */
	hosts        func() ([]string, error)
	hastate_path func(host_uuid string) string
	inq_hosts    func() ([]lockman.Host_lease, error)
	inq_leases   func(deadline time.Time) ([]lockman.Resource_lease, int, int, error)
	/*
	 * stale is how long a peer report counts as evidence, taken from the
	 * cluster policy of the round; zero means the documented default.
	 */
	stale        int
}

/* set_policy remembers the cluster policy of this round. */
func (r *reader) set_policy(p *policy.Policy) {
	if (p == nil) {
		return
	}
	r.stale = p.Hastate_stale_seconds
}

/* stale_seconds is how long a peer report counts as evidence. */
func (r *reader) stale_seconds() int {
	if (r.stale > 0) {
		return r.stale
	}
	return policy.DEFAULT_HASTATE_STALE_SECONDS
}

func new_reader(me string) *reader {
	return &reader{
		me:           me,
		seen:         make(map[string]seen),
		hosts:        reg.Hosts,
		hastate_path: reg.Hastate_file,
		inq_hosts:    lockman.Inq_hosts,
		inq_leases:   lockman.Inq_leases,
	}
}

/* boot_time returns this host's boot time in milliseconds since the epoch, or 0. */
func boot_time() int64 {
	var (
		err error
		data []byte
		fields []string
		secs int64
	)
	data, err = os.ReadFile("/proc/stat")
	if (err != nil) {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if (!strings.HasPrefix(line, "btime ")) {
			continue
		}
		fields = strings.Fields(line)
		if (len(fields) != 2) {
			break
		}
		secs, err = strconv.ParseInt(fields[1], 10, 64)
		if (err != nil) {
			return 0
		}
		return secs * 1000
	}
	return 0
}

/* uptime_seconds returns this host's uptime in seconds, or 0. */
func uptime_seconds() int64 {
	var (
		err error
		data []byte
		fields []string
		secs float64
	)
	data, err = os.ReadFile("/proc/uptime")
	if (err != nil) {
		return 0
	}
	fields = strings.Fields(string(data))
	if (len(fields) < 1) {
		return 0
	}
	secs, err = strconv.ParseFloat(fields[0], 64)
	if (err != nil) {
		return 0
	}
	return int64(secs)
}

/* addrs_text names the judge addresses of this host, or says there are none. */
func addrs_text(addrs []string) string {
	if (len(addrs) == 0) {
		return "none"
	}
	return strings.Join(addrs, ", ")
}

/* Judge_addrs parses the configured judge addresses: one address per line. */
/*
 * Ping_result turns the outcome of a ping run into "did it answer" and "did it
 * run at all". iputils ping exits 1 when nothing answered and 2 for a usage or
 * permission problem (for instance when it may not open a raw socket): the
 * second case means we learned nothing about the address and must not be
 * reported as "isolated".
 */
func Ping_result(err error) (replied bool, ran bool) {
	var exit *exec.ExitError
	if (err == nil) {
		return true, true
	}
	if (errors.As(err, &exit)) {
		if (exit.ExitCode() == 1) {
			return false, true
		}
		return false, false
	}
	return false, false
}

/* Probe is what Judge uses to ask one address; it is a parameter so that a test can replace it. */
type Probe func(addr string) (replied bool, ran bool)

func ping_once(addr string) (replied bool, ran bool) {
	var (
		err error
		out []byte
	)
	out, err = exec.Command(paths.Get("PING"), "-c", "1", "-W",
		strconv.Itoa(config.C.Isolation_ping_timeout_seconds), addr).CombinedOutput()
	replied, ran = Ping_result(err)
	if (!ran) {
		logger.Debug("hastate: ping %s: %s (%s)", addr, err.Error(), strings.TrimSpace(string(out)))
	}
	return replied, ran
}

/*
 * Judge returns (isolated, probed):
 *   probed = 1 only if at least one configured judge address was actually probed,
 *   isolated = 1 only if a probe ran and none of the addresses answered.
 * No judge configured and "could not probe" both leave probed = 0: a reader
 * must be able to tell "reachable" apart from "never asked", and an address
 * that is simply not configured is not an isolation.
 */
func Judge(addrs []string) (int, int, error) {
	return judge_with(addrs, ping_once)
}

func judge_with(addrs []string, probe Probe) (int, int, error) {
	var (
		ran_any bool
		failed []string
	)
	if (len(addrs) == 0) {
		return 0, 0, nil
	}
	for _, addr := range addrs {
		replied, ran := probe(addr)
		if (!ran) {
			failed = append(failed, addr)
			continue
		}
		ran_any = true
		if (replied) {
			return 0, 1, nil
		}
	}
	if (len(failed) != 0) {
		logger.Log("hastate: judge: could not probe %s", strings.Join(failed, ", "))
	}
	if (!ran_any) {
		return 0, 0, errors.New("none of the judge addresses could be probed")
	}
	return 1, 1, nil
}

/*
 * read_state_path reads one report file and says how it went. "Read but not
 * usable" (garbage, oversized, no uuid) is kept apart from "could not read it"
 * in the note, so that the log says which one happened. Both are reported as
 * unreadable: that is a declared deviation, because in the design unreadable means
 * "the storage could not be read"; for a reader the two mean the same thing -
 * no usable conclusion from that host - and the note keeps them distinguishable.
 */
func read_state_path(path string) (State, int, string) {
	var (
		err error
		data []byte
		fi os.FileInfo
		s State
	)
	fi, err = os.Stat(path)
	if (err != nil) {
		if (errors.Is(err, os.ErrNotExist)) {
			return s, read_missing, ""
		}
		return s, read_denied, err.Error()
	}
	if (fi.Size() > REPORT_MAX) {
		return s, read_broken, fmt.Sprintf("%d bytes is more than %d", fi.Size(), REPORT_MAX)
	}
	data, err = os.ReadFile(path)
	if (err != nil) {
		if (errors.Is(err, os.ErrNotExist)) {
			return s, read_missing, ""
		}
		return s, read_denied, err.Error()
	}
	err = json.Unmarshal(data, &s)
	if (err != nil) {
		return s, read_broken, err.Error()
	}
	if (s.Uuid == "") {
		return s, read_broken, "no uuid"
	}
	return s, read_ok, ""
}

/*
 * Classify turns one peer's report into one of the five states the design names,
 * using our own observation of it: the window opens when we see the report
 * change, and the report only becomes stale after we have watched it stand
 * still for the whole staleness window. The peer's ts is never used here: it is
 * a diagnostic value, not a clock we depend on.
 */
func (r *reader) Classify(host_uuid string, path string, now int64) (State, string, int64, string) {
	var (
		s State
		kind int
		note string
		fp string
		prev seen
		known bool
		age int64
	)
	s, kind, note = read_state_path(path)
	switch (kind) {
	case read_missing:
		/*
		 * Keep the window: a report that disappears and comes back must not
		 * reset our patience every time it flaps.
		 */
		return s, REPORT_ABSENT, 0, ""
	case read_denied, read_broken:
		/* no usable report: do not leak fields of a report we rejected */
		return State{}, REPORT_UNREADABLE, 0, note
	}
	fp = fmt.Sprintf("%d/%d/%d/%d", s.Ts, s.Heartbeat, s.Isolated, s.Probed)
	prev, known = r.seen[host_uuid]
	if (!known || prev.fingerprint != fp) {
		prev.fingerprint = fp
		prev.since = now
	}
	r.seen[host_uuid] = prev
	age = now - prev.since
	if (age < 0) {
		age = 0
	}
	if (age > int64(r.stale_seconds()) * 1000) {
		return s, REPORT_STALE, age, ""
	}
	if (s.Isolated != 0) {
		return s, REPORT_FRESH_1, age, ""
	}
	return s, REPORT_FRESH_0, age, ""
}

/* Read_peers reads every host's report, including our own, and pairs it with our sanlock view. */
func (r *reader) Read_peers(now int64) ([]Peer, error) {
	var (
		err error
		uuids []string
		hosts []lockman.Host_lease
		by_id map[uint64]lockman.Host_lease = make(map[uint64]lockman.Host_lease)
		peers []Peer
	)
	uuids, err = r.hosts()
	if (err != nil) {
		return nil, err
	}
	hosts, err = r.inq_hosts()
	if (err != nil) {
		logger.Log("hastate: sanlock host_id view unavailable: %s", err.Error())
		hosts = nil
	}
	for _, h := range hosts {
		by_id[h.Id] = h
	}
	for _, uuid := range uuids {
		var (
			p Peer
			s State
			id uint16
			lease lockman.Host_lease
			ok bool
			report string
			age int64
			note string
		)
		p.Host_uuid = uuid
		p.Host_state = "unknown"
		if (uuid == r.me) {
			p.Name = "me"
		} else {
			var info inventory.HostInfo
			info, err = inventory.Get_hostinfo(uuid)
			if (err == nil) {
				p.Name = info.Name
			}
		}
		err = reg.Load_lockid(uuid, &id)
		if (err == nil) {
			p.Host_id = uint64(id)
			lease, ok = by_id[p.Host_id]
			if (ok) {
				p.Host_state = lease.State
			}
		}
		s, report, age, note = r.Classify(uuid, r.hastate_path(uuid), now)
		p.Report = report
		p.Reason = s.Reason
		p.Probed = s.Probed
		p.Isolation_running_vms = s.Isolation_running_vms
		p.Mgmt_addr = s.Mgmt_addr
		p.Office_epoch = s.Office_epoch
		p.Policy_gen = s.Policy_gen
		p.Ts = s.Ts
		p.Age_ms = age
		p.Note = note
		peers = append(peers, p)
	}
	return peers, nil
}

/* describe renders one peer as a short token for the log. */
func describe(p Peer) string {
	var (
		name string = p.Name
		reason string = p.Reason
		note string = ""
	)
	if (name == "") {
		name = p.Host_uuid
		if (len(name) > 8) {
			name = name[:8]
		}
	}
	if (reason == "") {
		reason = "-"
	}
	if (p.Note != "") {
		note = " (" + p.Note + ")"
	}
	return fmt.Sprintf("%s(%d) %s %s %s probed=%d age=%ds%s%s",
		name, p.Host_id, p.Host_state, p.Report, reason, p.Probed, p.Age_ms / 1000, note,
		iso_stopped_note(p))
}

/*
 * iso_stopped_note names the VMs a peer says it stopped under its isolation
 * response, and says nothing at all when it stopped none - an empty list is the
 * normal state and must not lengthen every line.
 */
func iso_stopped_note(p Peer) string {
	if (len(p.Isolation_running_vms) == 0) {
		return ""
	}
	return fmt.Sprintf(" iso-runs=%s", strings.Join(p.Isolation_running_vms, ","))
}

/*
 * local_mgmt_addr is this host's own management address, taken from the member
 * tags serf publishes for it: the same address a peer would reach it on.
 */
func local_mgmt_addr() string {
	var (
		hostdata inventory.Hostdata
		err error
	)
	hostdata, err = inventory.Get_hostdata(machine.Uuid())
	if (err != nil) {
		return ""
	}
	return hostdata.Man_ip
}

/* change_key is what we compare between runs to decide whether something is
 * worth a log line: the ages move on every run, so they must not be part of it.
 */
func change_key(probed int, isolated int, v view) string {
	var (
		parts []string
	)
	parts = append(parts, fmt.Sprintf("probed=%d isolated=%d", probed, isolated))
	for _, p := range v.peers {
		parts = append(parts, fmt.Sprintf("%s/%d/%s/%s/%s/%d/%s/%s",
			p.Host_uuid, p.Host_id, p.Host_state, p.Report, p.Reason, p.Probed, p.Note,
			strings.Join(p.Isolation_running_vms, ",")))
	}
	for _, l := range v.leases {
		parts = append(parts, fmt.Sprintf("%s/%d/%d/%d", l.Name, l.Timestamp, l.Owner_id, l.Owner_gen))
	}
	parts = append(parts, fmt.Sprintf("addrs_err=%v peers_err=%v unreadable=%d skipped=%d lease_err=%v",
		v.addrs_err != nil, v.peers_err != nil, v.unreadable, v.skipped, v.lease_err != nil))
	return strings.Join(parts, " ")
}

/* render_line is the one line we log: the judge, every peer, and the raw lease records. */
func render_line(probed int, isolated int, v view) string {
	var (
		parts []string
		peers string
	)
	for _, p := range v.peers {
		parts = append(parts, describe(p))
	}
	peers = strings.Join(parts, " | ")
	if (v.peers_err != nil) {
		/* not "there are no peers": we could not read them */
		peers = fmt.Sprintf("peers: unreadable (%s)", v.peers_err.Error())
	}
	return fmt.Sprintf("probed=%d isolated=%d %s | %s",
		probed, isolated, peers, describe_leases(v.leases, v.unreadable, v.skipped, v.lease_err))
}

/*
 * describe_leases renders the resource leader records we could read. It prints
 * the raw triple and never concludes anything: "free" is the decision layer's
 * judgement (the free-lease test wants timestamp and owner_id both zero), and deciding it here
 * once said "FREE" about a record whose owner_id was not zero.
 */
func describe_leases(leases []lockman.Resource_lease, unreadable int, skipped int, err error) string {
	var (
		parts []string
	)
	if (err != nil) {
		return fmt.Sprintf("leases: resource list unreadable (%s)", err.Error())
	}
	for _, l := range leases {
		var name string = l.Name
		if (len(name) > 8) {
			name = name[:8]
		}
		parts = append(parts, fmt.Sprintf("%s ts=%d owner=%d gen=%d", name, l.Timestamp, l.Owner_id, l.Owner_gen))
	}
	if (unreadable != 0) {
		parts = append(parts, fmt.Sprintf("%d unreadable", unreadable))
	}
	if (skipped != 0) {
		parts = append(parts, fmt.Sprintf("%d skipped", skipped))
	}
	if (len(parts) == 0) {
		return "leases: none"
	}
	return "leases: " + strings.Join(parts, " | ")
}

/*
 * observe watches the other hosts and the resource leaders, and publishes what
 * it sees. It runs in its own goroutine on purpose: every read below goes to
 * the shared storage, whose mount is hard (timeo=600, retrans=2,
 * fatal_neterrors=none), so a stalled read would otherwise block this host's
 * own self report - and a broken monitor must not be able to make a healthy
 * host look dead to the others.
 *
 * The judge address is read here as well, for the same reason: it is a read of
 * the shared storage, and the report writer must not wait for it.
 */
func observe(r *reader, seconds int, mu *sync.Mutex, latest *view) {
	var (
		ticker *time.Ticker
		last_origin policy.Origin
		last_addrs string
	)
	if (seconds < 1) {
		seconds = policy.DEFAULT_HASTATE_REFRESH_SECONDS
	}
	ticker = time.NewTicker(time.Duration(seconds) * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		var (
			v view
			w string
			deadline time.Time
		)
		/*
		 * The whole round shares one budget: what the peer phase spends, the
		 * lease phase no longer has, and the rest is reported as skipped.
		 */
		deadline = time.Now().Add(time.Duration(config.C.Hastate_round_budget_seconds) * time.Second)
		/*
		 * The cluster policy is read with the other shared storage reads, and
		 * for the same reason: it decides the judge addresses and the switch of
		 * the action surface, and the self report below must not wait for the
		 * shared tree.
		 */
		v.policy, v.origin = policy.R.Resolve()
		if (v.policy != nil) {
			r.set_policy(v.policy)
		}
		/*
		 * The generation this host has applied is what the other hosts see as
		 * this host's acknowledgement of a policy change: it is written into the
		 * report below, with the policy itself.
		 */
		v.policy_gen = policy.R.Applied().Generation
		v.addrs = judge_addresses(v.policy)
		/*
		 * Say which addresses this host judges itself by, and where they came
		 * from, once per change: an operator who never set the keys in the policy
		 * has no other place to see what is actually probed.
		 */
		if (strings.Join(v.addrs, ",") != last_addrs) {
			last_addrs = strings.Join(v.addrs, ",")
			logger.Log("hastate: judge addresses: %s (source=%v)", addrs_text(v.addrs), v.origin)
		}
		for _, w = range policy.R.Warnings() {
			logger.Log("hastate: policy: %s", w)
		}
		if (v.origin != last_origin) {
			last_origin = v.origin
			if (v.origin != policy.ORIGIN_SHARED) {
				logger.Log("hastate: policy: %s is not in force, using the %s policy",
					policy.PATH, v.origin)
			}
		}
		v.peers, v.peers_err = r.Read_peers(ts.Now())
		v.leases, v.unreadable, v.skipped, v.lease_err = r.inq_leases(deadline)
		v.ok = true
		v.ts = ts.Now()
		/* publish before logging: a blocked stderr must not hold back the view */
		mu.Lock()
		*latest = v
		mu.Unlock()
		if (v.peers_err != nil) {
			logger.Log("hastate: could not read the reports: %s", v.peers_err.Error())
		}
		if (v.lease_err != nil) {
			logger.Log("hastate: could not list the resources: %s", v.lease_err.Error())
		}
		if (v.unreadable != 0) {
			logger.Log("hastate: %d resource leader(s) could not be read", v.unreadable)
		}
		if (v.skipped != 0) {
			logger.Log("hastate: %d resource leader(s) skipped, read budget spent", v.skipped)
		}
		if (len(v.addrs) == 0) {
			logger.Log("hastate: no judge address: %s configures none, and this host could not derive one",
				policy.PATH)
		}
		/* the cluster may have changed the pace: follow it */
		if (v.policy != nil && v.policy.Hastate_refresh_seconds > 0 && v.policy.Hastate_refresh_seconds != seconds) {
			seconds = v.policy.Hastate_refresh_seconds
			ticker.Reset(time.Duration(seconds) * time.Second)
		}
	}
}

/* Start runs the loop in its own goroutine, with the period from the
 * configuration. */
func Start() {
	go Loop(0)
}

/*
 * Loop writes this host's self report, logs what it sees, and never acts.
 *
 * The refresh follows the design ("write on change, and roughly every five
 * seconds"): the probe runs once per period, so writing once per period covers
 * both. The observation happens in
 * another goroutine (see observe). Log lines are emitted only when the view
 * (not the ages) changes, to keep them readable.
 */
func Loop(seconds int) {
	var (
		err error
		me string = machine.Uuid()
		r *reader
		mu sync.Mutex
		latest view
		ticker *time.Ticker
		last string
	)
	if (seconds < 1) {
		seconds = policy.DEFAULT_HASTATE_REFRESH_SECONDS
	}
	logger.Debug("hastate: loop starting, period %d s", seconds)
	r = new_reader(me)
	/* the judge address is read by observe: it is a shared storage read too */
	go observe(r, seconds, &mu, &latest)
	ticker = time.NewTicker(time.Duration(seconds) * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		var (
			s State
			data []byte
			isolated int
			probed int
			v view
			line string
			key string
		)
		mu.Lock()
		v = latest
		mu.Unlock()
		isolated, probed, err = Judge(v.addrs)
		if (err != nil) {
			logger.Log("hastate: judge: %s", err.Error())
		}
		s.Uuid = me
		s.Boot = boot_time()
		s.Ts = ts.Now()
		s.Judges_silent = isolated
		if (isolated != 0 && !cluster_unreachable()) {
			/*
			 * The judges are silent but the cluster is still there: this host has a
			 * path problem, not an isolation. Keeping the VMs is the answer, and
			 * the report says the two apart so an operator can see it.
			 */
			isolated = 0
		}
		s.Isolated = isolated
		iso_self = isolated
		s.Probed = probed
		s.Office_epoch = office_epoch()
		s.Isolation_running_vms = isolation_round(me, isolated, probed, s.Ts)
		s.Policy_gen = v.policy_gen
		s.Mgmt_addr = local_mgmt_addr()
		s.Reason = REASON_RUNNING
		if (isolated != 0) {
			s.Reason = REASON_ISOLATED
		}
		s.Heartbeat = uptime_seconds()
		data, err = json.Marshal(&s)
		if (err != nil) {
			logger.Log("hastate: could not encode the report: %s", err.Error())
			continue
		}
		/*
		 * If this fails we have no way to report it: that is exactly the
		 * "unreadable" state on the reader's side (see the comment on top).
		 */
		err = reg.Save_hastate(me, data)
		if (err != nil) {
			logger.Log("hastate: could not write the report: %s", err.Error())
		}
		if (!v.ok) {
			/* the first observation has not come back yet */
			continue
		}
		line = render_line(probed, isolated, v)
		key = change_key(probed, isolated, v)
		/* let read-only consumers (the judgement layer) see the same view */
		publish(v, probed, isolated)
		logger.Debug("hastate: view: %s", line)
		if (key != last) {
			logger.Log("hastate: change: %s", line)
			last = key
		}
		/* the cluster may have changed the pace: follow it */
		if (v.policy != nil && v.policy.Hastate_refresh_seconds > 0 && v.policy.Hastate_refresh_seconds != seconds) {
			seconds = v.policy.Hastate_refresh_seconds
			ticker.Reset(time.Duration(seconds) * time.Second)
		}
	}
}
