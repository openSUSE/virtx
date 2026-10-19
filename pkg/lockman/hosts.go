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

package lockman

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"suse.com/virtx/pkg/config"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/paths"
	. "suse.com/virtx/pkg/constants"
)

/*
 * sanlock_cmd_timeout is how long one sanlock command may take. It used to be a
 * compile-time constant and now comes from /etc/virtx/virtxd.conf
 * (sanlock_cmd_timeout_seconds). It is read at every use, so a caller that
 * changes the configuration is seen without another wiring step, and there is
 * no second copy of the default here.
 */
func sanlock_cmd_timeout() time.Duration {
	return time.Duration(config.C.Sanlock_cmd_timeout_seconds) * time.Second
}

/*
 * run_sanlock runs a read-only sanlock subcommand with a deadline. Without one
 * a stalled NFS would block the caller for as long as the kernel keeps
 * retrying, and our callers refresh their own liveness report in the same loop:
 * a stuck monitor read must not be able to make this host look dead.
 *
 * WaitDelay makes CombinedOutput return even when the child can not be killed
 * right away (a process waiting in an uninterruptible NFS read does not die on
 * SIGKILL immediately), so the caller is not held hostage by it either.
 */
func run_sanlock(timeout time.Duration, args ...string) ([]byte, error) {
	var (
		ctx context.Context
		cancel context.CancelFunc
		cmd *exec.Cmd
		out []byte
		err error
	)
	if (timeout <= 0) {
		timeout = time.Second
	}
	ctx, cancel = context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd = exec.CommandContext(ctx, paths.Get("SANLOCK"), args...)
	cmd.WaitDelay = time.Second
	out, err = cmd.CombinedOutput()
	if (ctx.Err() != nil) {
		return out, fmt.Errorf("sanlock %v timed out after %s", args, timeout)
	}
	return out, err
}

/*
 * Host_lease is one host_id slot as our own sanlock daemon sees it. The
 * timestamp is the writer's own monotime (its uptime in seconds when it last
 * renewed), so it must never be compared with our clock.
 */
type Host_lease struct {
	Id uint64
	Gen uint64
	Timestamp uint64
	State string
}

/* Host_status is one line of "sanlock client host_status -s <lockspace>": the
 * host_id and the timestamp of its host_id lease. The design requires
 * exactly this read to tell "the coordinator is still renewing" (row 4) from
 * "it is past DEAD_CONFIRM_MS" (row 3): the timestamp is the object. */
type Host_status struct {
	Id uint64
	Timestamp uint64
}

/*
 * Inq_host_status asks sanlock for the host_id leases of one lockspace.
 * It is a read-only command; the caller gives it a deadline.
 */
func Inq_host_status(lockspace string) ([]Host_status, error) {
	var (
		err error
		output []byte
		statuses []Host_status
		fields []string
		id, timestamp uint64
	)
	output, err = run_sanlock(0, "client", "host_status", "-s", lockspace)
	if (err != nil) {
		return nil, err
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields = strings.Fields(line)
		/* "<host_id> timestamp <timestamp>" */
		if (len(fields) != 3 || fields[1] != "timestamp") {
			continue
		}
		id, err = strconv.ParseUint(fields[0], 10, 64)
		if (err != nil) {
			continue
		}
		timestamp, err = strconv.ParseUint(fields[2], 10, 64)
		if (err != nil) {
			continue
		}
		statuses = append(statuses, Host_status{ Id: id, Timestamp: timestamp })
	}
	return statuses, nil
}

/* Resource_leader is the leader record of one resource, read by explicit path
 * rather than by scanning the lock directory: the coordinator lease is a flat
 * file, which a directory scan does not see. */
type Resource_leader struct {
	Found bool
	Owner_id uint64
	Owner_generation uint64
	Timestamp uint64
}

/*
 * leader_read_absent says whether a failed leader read means "this resource has
 * no leader yet" rather than "the storage could not be read".
 *
 * The two must not be confused: the coordinator may create the lease only when
 * the resource really is not there (row 5a), while a storage that cannot
 * be read must leave the lease unclaimed (row 5b, fail closed). Measured on
 * this cluster 2026-10-07:
 *
 *	the path is not there -> "read_leader done -2" and exit 1
 *	the path is there      -> "read_leader done 0"  and exit 0
 *
 * -2 is ENOENT, the answer for a path that is not there. A failing storage
 * answers with its own code (EIO, ESTALE, ETIMEDOUT, ...) or with no answer at
 * all, and must stay a read failure. The exit code alone cannot decide either
 * (a sanlock that cannot be started also exits non-zero, without printing the
 * line, and calling that "absent" would hide a broken installation), so this
 * needs both: sanlock answering ENOENT for this path, and the filesystem
 * agreeing that the path is not there.
 */
func leader_read_absent(read_err error, output []byte, resource_path string) bool {
	var (
		stat_err error
	)
	if (read_err == nil) {
		return false
	}
	if (!strings.Contains(string(output), "read_leader done -2")) {
		return false
	}
	_, stat_err = os.Stat(resource_path)
	return os.IsNotExist(stat_err)
}

/*
 * Read_resource_leader reads one resource's leader directly (the read the design
 * prescribes). It is read-only and works for any layout.
 */
func Read_resource_leader(resource_name string, resource_path string) (Resource_leader, error) {
	var (
		err error
		output []byte
		l Resource_leader
		spec string
		fields []string
		value uint64
	)
	spec = fmt.Sprintf("%s:%s:%s:%d", LOCK_SPACE, resource_name, resource_path, 0)
	output, err = run_sanlock(sanlock_cmd_timeout(), "direct", "read_leader", "-r", spec)
	if (err != nil) {
		if (leader_read_absent(err, output, resource_path)) {
			/*
			 * The resource does not exist: that is "no leader yet"
			 * (OBS_ABSENT for the coordinator), not a read failure.
			 */
			return l, nil
		}
		return l, err
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields = strings.Fields(line)
		if (len(fields) != 2) {
			continue
		}
		value, err = strconv.ParseUint(fields[1], 10, 64)
		if (err != nil) {
			continue
		}
		switch (fields[0]) {
		case "owner_id":
			l.Owner_id = value
			l.Found = true
		case "owner_generation":
			l.Owner_generation = value
		case "timestamp":
			l.Timestamp = value
		}
	}
	return l, nil
}

/*
 * Resource_lease is the leader record of one sanlock resource, that is, of one
 * VM disk lease: timestamp 0 usually means the lease is free, but only the
 * decision layer may conclude that (its lease test wants timestamp 0; the owner_id is
 * deliberately not checked - sanlock never clears it, so requiring it to be 0
 * would make the lease test unsatisfiable, as ruled on 2026-10-04).
 */
type Resource_lease struct {
	Name string
	Timestamp uint64
	Owner_id uint64
	Owner_gen uint64
}

/*
 * Parse_hosts picks the "h <host_id> gen <gen> timestamp <ts> <state>" lines out
 * of "sanlock client gets -h 1". The reference for this table is
 * scripts/virtx-ha-probe.sh (live_states); keep the two in step. The ids are
 * read as 64 bit and are not range checked against uint16 on purpose: a value
 * that does not fit would silently disappear from the answer otherwise.
 */
func Parse_hosts(output string) (leases []Host_lease, dropped int) {
	for _, line := range strings.Split(output, "\n") {
		var (
			l Host_lease
			id, gen, timestamp uint64
		)
		n, _ := fmt.Sscanf(line, "h %d gen %d timestamp %d %s", &id, &gen, &timestamp, &l.State)
		if (n == 4) {
			l.Id = id
			l.Gen = gen
			l.Timestamp = timestamp
			leases = append(leases, l)
			continue
		}
		if (strings.HasPrefix(strings.TrimSpace(line), "h ")) {
			dropped++
		}
	}
	return leases, dropped
}

/*
 * Inq_hosts returns the state of every host_id our own sanlock daemon knows
 * about: FREE, LIVE, FAIL, DEAD, UNKNOWN or ERROR (the strings come from
 * sanlock's host_state_str, main.c:3418-3434; the UNKNOWN_OLD bit is carried in
 * the same flags but is not a value of this column). Only a host that has
 * joined the lockspace can answer this, and the answer is about the leases on
 * the shared storage, not about network reachability: FREE means the slot was
 * never joined, UNKNOWN means we have not watched it long enough to tell, and
 * neither of them means "it died".
 *
 * A failure is an error, never a partial table: sanlock prints its errors with
 * log_tool() (plain printf to stdout), a failed or timed out run may still have
 * produced some host lines, and treating those as the whole table would silently
 * turn the missing hosts into "unknown".
 */
func Inq_hosts() ([]Host_lease, error) {
	var (
		args []string
		output []byte
		leases []Host_lease
		dropped int
		cerr, perr error
	)
	args = []string{ "client", "gets", "-h", "1" }
	logger.Debug("sanlock %v", args)
	output, cerr = run_sanlock(sanlock_cmd_timeout(), args...)
	if (cerr != nil) {
		return nil, fmt.Errorf("sanlock client gets -h 1: %s (output %q)", cerr.Error(), strings.TrimSpace(string(output)))
	}
	leases, dropped = Parse_hosts(string(output))
	if (dropped > 0) {
		logger.Log("lockman: sanlock gets -h 1: %d line(s) not understood", dropped)
	}
	if (len(leases) == 0) {
		perr = fmt.Errorf("no host line in the output")
		return nil, fmt.Errorf("sanlock client gets -h 1: %s (output %q)", perr.Error(), strings.TrimSpace(string(output)))
	}
	return leases, nil
}

/*
 * Parse_leader picks the fields we need out of "sanlock direct read_leader",
 * whose output is a list of "name value" lines. The first occurrence of a field
 * wins: the leader record comes first, and the host record that follows it has
 * its own "timestamp".
 */
func Parse_leader(name string, output string) (Resource_lease, error) {
	var (
		l Resource_lease
		fields map[string]uint64 = make(map[string]uint64)
		seen int
	)
	l.Name = name
	for _, line := range strings.Split(output, "\n") {
		var (
			key string
			value uint64
		)
		n, _ := fmt.Sscanf(strings.TrimSpace(line), "%s %d", &key, &value)
		if (n != 2) {
			continue
		}
		switch (key) {
		case "timestamp", "owner_id", "owner_generation":
			var have bool
			_, have = fields[key]
			if (have) {
				continue
			}
			fields[key] = value
			seen++
		}
	}
	if (seen < 3) {
		return l, fmt.Errorf("no leader fields in the output")
	}
	l.Timestamp = fields["timestamp"]
	l.Owner_id = fields["owner_id"]
	l.Owner_gen = fields["owner_generation"]
	return l, nil
}

/*
 * Lease_timeout is how long we are willing to wait for one leader read: what is
 * left of the caller's budget, capped at the per-command limit. A value at or
 * below zero means the budget is spent.
 */
func Lease_timeout(now time.Time, deadline time.Time, limit time.Duration) time.Duration {
	remaining := deadline.Sub(now)
	if (remaining > limit) {
		return limit
	}
	return remaining
}

/*
 * Inq_leases reads the leader record of every resource on the lockspace, which
 * is the raw material the decision layer needs for its positive evidence. It is
 * a read-only call: "sanlock direct read_leader -r <lockspace>:<resource>:<path>:0".
 *
 * It returns how many leaders could not be read and how many were skipped
 * because the caller's budget ran out, so that "there are no resources" can be
 * told apart from "the reads are failing" or "we did not get that far". One
 * fork per resource, so a large cluster should not call this on a hot path.
 *
 * The budget is a soft bound: one command may take its timeout plus the
 * WaitDelay, so a stalled read can spend the whole budget and everything after
 * it is then reported as skipped.
 */
func Inq_leases(deadline time.Time) ([]Resource_lease, int, int, error) {
	var (
		entries []os.DirEntry
		err error
		leases []Resource_lease
		unreadable int
		skipped int
	)
	entries, err = os.ReadDir(LOCK_DIR)
	if (err != nil) {
		return nil, 0, 0, err
	}
	for _, e := range entries {
		var (
			name string
			path string
			spec string
			output []byte
			lease Resource_lease
			timeout time.Duration
		)
		if (!e.IsDir()) {
			/* the lockspace file itself */
			continue
		}
		timeout = Lease_timeout(time.Now(), deadline, sanlock_cmd_timeout())
		if (timeout <= 0) {
			skipped++
			continue
		}
		name = e.Name()
		path = fmt.Sprintf("%s%s/%s", LOCK_DIR, name, name)
		spec = fmt.Sprintf("%s:%s:%s:0", LOCK_SPACE, name, path)
		output, err = run_sanlock(timeout, "direct", "read_leader", "-r", spec)
		if (err != nil) {
			/*
			 * A read that timed out is not a read that failed: the renderer
			 * keeps "skipped" and "unreadable" apart, so this must too. Only
			 * the timeout says "we did not wait long enough"; any other error
			 * is a real one, even if the deadline has passed meanwhile.
			 */
			if (strings.Contains(err.Error(), "timed out")) {
				skipped++
			} else {
				unreadable++
			}
			logger.Debug("lockman: read_leader %s failed: %s", name, strings.TrimSpace(string(output)))
			continue
		}
		lease, err = Parse_leader(name, string(output))
		if (err != nil) {
			unreadable++
			logger.Log("lockman: read_leader %s: %s", name, err.Error())
			continue
		}
		leases = append(leases, lease)
	}
	return leases, unreadable, skipped, nil
}
