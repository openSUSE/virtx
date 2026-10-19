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
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/paths"
	. "suse.com/virtx/pkg/constants"
)

/*
 * This file gives back the resource leases this host still holds in its own
 * sanlock daemon but no longer owns on the shared storage, so that they cannot
 * keep this host from registering a lease ever again.
 *
 * How such a leftover comes to exist:
 *
 *  - a holder this tree starts with -P 1 (SANLK_RES_PERSISTENT) does not give
 *    its lease back when the process is killed: sanlock moves it to the daemon's
 *    orphan list and this host keeps holding it (sanlock.h:61-79,
 *    resource.c:1482-1484). The coordinator's holders are started like that
 *    (pkg/coord, HOLDER_CMD). A holder that exits on its own does give the lease
 *    back, persistent or not: do_spawn releases explicitly and synchronously
 *    (main.c:3766-3781, measured 2026-10-07);
 *  - a holder from an earlier virtxd is not in the new process's handles at all,
 *    so nothing in this tree can signal it (pkg/coord's release()).
 *
 * Why that is an availability hole:
 *
 * The daemon refuses a new acquire of a resource this host still has - with
 * -EEXIST when the registration is live (acquire_token "resource exists",
 * resource.c:2016-2021) and with -EUCLEAN when it is an orphan and the caller
 * did not ask for orphans (acquire_token "found orphan", resource.c:2027-2031).
 * The refusal is per resource: find_resource matches the lockspace name and the
 * resource name (resource.c:1500-1513). So a leftover __VIRTX__COORD
 * registration is exactly what stops this host from registering a coordinator
 * lease again, and a host that cannot register one can never take office.
 * Observed 2026-10-07: with the coordinator lease deleted, two of the three
 * hosts still had such a registration, neither of them managed to register a
 * holder in the fresh election, and the host without a leftover took office in
 * 13 s. (A separate 2026-10-04 observation - `register done -13` after a holder
 * had been killed - has no explanation in the source; it is kept as an open
 * observation in pkg/coord/actuate.go's release().)
 *
 * What may be given back, and how:
 *
 *  - only orphans, and only with -O 1 (SANLK_REL_ORPHAN, main.c:2781-2784 and
 *    3963-3978); the daemon then walks its orphan list only (cmd.c:657 calls
 *    release_orphan, resource.c:2983-3008), so a live registration - which may
 *    be the valid holder of this host's lease, the one the coordinator adopts
 *    after a restart - is never touched;
 *  - only our own resources: our lockspace and a resource name this tree gives
 *    (RESOURCE_PREFIX below). The daemon's view is this host's, so another
 *    host's registration is not reachable from here in the first place;
 *  - only when the shared storage says the lease is not ours any more: an orphan
 *    whose leader still names this host *and still has a timestamp* is still this
 *    host's office, and it stays. A leader with timestamp 0 (LEASE_FREE) has been
 *    given back: sanlock leaves owner_id, owner_generation and lver untouched when
 *    a lease is released and writes only timestamp 0 (paxos_lease.c:3143-3150),
 *    measured on scratch resources 2026-10-07 (from the controlled lease
 *    experiments). Such an orphan holds nothing on the
 *    shared storage, so keeping it would only keep this host from registering that
 *    lease - it is given back here as well;
 *  - and the daemon only accepts a release from a connection whose target client
 *    is registered (main.c:1216-1240; an unregistered caller gets -EBUSY and the
 *    daemon logs "cmd 8 ... no pid"). The CLI never registers itself, so this
 *    sweep starts a scratch holder of its own for exactly that purpose and gives
 *    it back afterwards (stale_release_caller). Measured 2026-10-07: without a
 *    registered caller the daemon answered "release done -16" and h11's
 *    __VIRTX__COORD orphan stayed; with one it answered "release done 1" and the
 *    orphan was gone, while h13's live lease, its leader record and its
 *    coordinator role were untouched - paxos_release refuses to write unless the
 *    on-disk leader is the token's own (paxos_lease.c:3055-3132);
 *  - when the leader cannot be read nothing is released - a storage we cannot
 *    read is not evidence that the lease is gone (fail closed).
 *
 * Every failure only logs (fail soft: a startup is never blocked by this), the
 * scratch caller is cleaned up in every outcome, and the sweep is idempotent:
 * what it released is gone from the daemon's view, and what it keeps it keeps
 * again.
 */
const RESOURCE_PREFIX = "__VIRTX__"

/* the scratch holder whose only job is to be a registered sanlock client */
const STALE_RELEASE_NAME = "__VIRTX__STALE_RELEASE"
const STALE_RELEASE_PATH = "/tmp/__VIRTX__STALE_RELEASE"
const STALE_CALLER_CMD = "/bin/sleep"
const STALE_CALLER_ARG = "300"
const STALE_CALLER_TRIES = 30
const STALE_CALLER_WAIT_MS = 100
const STALE_CALLER_STOP_TRIES = 20
const STALE_CALLER_STOP_MS = 100

/*
 * Local_registration is one resource lease this host's sanlock daemon reports:
 * one "r <lockspace>:<resource>:<path>:<offset>:<lver> p <pid>[ ORPHAN]" line of
 * `sanlock client status`.
 */
type Local_registration struct {
	Lockspace string
	Resource string
	Path string
	Offset uint64
	Lver uint64
	Pid int
	Orphan bool
}

/*
 * Registration_action is what the sweep decides for one local registration.
 */
type Registration_action int

const (
	/* ours, orphaned, and the shared storage no longer names this host */
	REGISTRATION_RELEASE Registration_action = iota
	/* the shared lease still names this host: that orphan is our office */
	REGISTRATION_KEEP_OURS
	/* a live registration: it may be the valid holder of this host's lease */
	REGISTRATION_KEEP_LIVE
	/* another lockspace, or a resource name that is not ours */
	REGISTRATION_SKIP_FOREIGN
	/* the shared storage could not be read: do not guess */
	REGISTRATION_SKIP_UNKNOWN
)

func (a Registration_action) String() string {
	switch (a) {
	case REGISTRATION_RELEASE:
		return "release"
	case REGISTRATION_KEEP_OURS:
		return "keep-ours"
	case REGISTRATION_KEEP_LIVE:
		return "keep-live"
	case REGISTRATION_SKIP_FOREIGN:
		return "skip-foreign"
	}
	return "skip-unknown"
}

/* Registration_sweep counts what one sweep found and did. */
type Registration_sweep struct {
	Released int
	Kept_ours int
	Kept_live int
	Foreign int
	Unknown int
	Failed int
}

/*
 * Parse_registrations picks the resource leases out of one `sanlock client
 * status` output. The other lines (the daemon line, "p -1 helper", "s ...") and
 * anything this version does not understand are ignored: a status we cannot read
 * is not a reason to release anything.
 */
func Parse_registrations(output string) []Local_registration {
	var (
		regs []Local_registration
		reg Local_registration
		fields []string
		parts []string
		value uint64
		err error
		orphan bool
		i int
	)
	for _, line := range strings.Split(output, "\n") {
		fields = strings.Fields(line)
		if (len(fields) < 4 || fields[0] != "r" || fields[2] != "p") {
			continue
		}
		parts = strings.Split(fields[1], ":")
		if (len(parts) != 5) {
			continue
		}
		value, err = strconv.ParseUint(parts[3], 10, 64)
		if (err != nil) {
			continue
		}
		reg = Local_registration{
			Lockspace: parts[0],
			Resource: parts[1],
			Path: parts[2],
			Offset: value,
		}
		value, err = strconv.ParseUint(parts[4], 10, 64)
		if (err != nil) {
			continue
		}
		reg.Lver = value
		value, err = strconv.ParseUint(fields[3], 10, 64)
		if (err != nil) {
			continue
		}
		reg.Pid = int(value)
		orphan = false
		for i = 4; i < len(fields); i++ {
			if (fields[i] == "ORPHAN") {
				orphan = true
			}
		}
		reg.Orphan = orphan
		regs = append(regs, reg)
	}
	return regs
}

/* scratch_registered reports whether one status output lists the scratch lease. */
func scratch_registered(output string) bool {
	for _, reg := range Parse_registrations(output) {
		if (reg.Resource == STALE_RELEASE_NAME) {
			return true
		}
	}
	return false
}

/*
 * registration_action applies the rules from the top of this file to one
 * registration, and decides nothing else: the caller does the releasing.
 */
func registration_action(reg Local_registration, lockspace string, resource_prefix string,
                         my_host_id uint64, leader Resource_leader, leader_err error) Registration_action {
	if (reg.Lockspace != lockspace || !strings.HasPrefix(reg.Resource, resource_prefix)) {
		return REGISTRATION_SKIP_FOREIGN
	}
	if (!reg.Orphan) {
		return REGISTRATION_KEEP_LIVE
	}
	if (leader_err != nil) {
		return REGISTRATION_SKIP_UNKNOWN
	}
	/*
	 * The leader naming this host is not enough: a lease that was released still
	 * names us, with timestamp 0 (measured 2026-10-07, from the controlled
	 * lease-semantics experiment), and an orphan of a lease that is free holds
	 * nothing - keeping it would keep this host from registering that lease.
	 */
	if (leader.Found && leader.Owner_id == my_host_id && leader.Timestamp != 0) {
		return REGISTRATION_KEEP_OURS
	}
	return REGISTRATION_RELEASE
}

/*
 * release_result reads what `sanlock client release -O 1` answered. The daemon
 * returns the number of orphans it gave back and the tool exits non-zero for a
 * positive count, so the exit status must not be read as a failure: "release
 * done 1" is a success, "release done 0" means there was nothing left to give
 * back, and a negative value is the daemon's error (measured 2026-10-07:
 * "release done -16" when the caller was not a registered client).
 */
func release_result(output string, err error) error {
	var (
		fields []string
		n int
		perr error
	)
	for _, line := range strings.Split(output, "\n") {
		fields = strings.Fields(line)
		if (len(fields) != 3 || fields[0] != "release" || fields[1] != "done") {
			continue
		}
		n, perr = strconv.Atoi(fields[2])
		if (perr != nil) {
			continue
		}
		if (n >= 0) {
			return nil
		}
		return fmt.Errorf("release done %d: %s", n, strings.TrimSpace(output))
	}
	if (err != nil) {
		return fmt.Errorf("%s (output %q)", err.Error(), strings.TrimSpace(output))
	}
	return fmt.Errorf("no release answer in %q", strings.TrimSpace(output))
}

/*
 * stale_release_caller starts one scratch holder of our own and returns the pid
 * of its registered client, so that this sweep can be a registered caller: the
 * daemon refuses a release from a connection whose target client is not
 * registered (main.c:1216-1240), and the CLI never registers itself.
 *
 * The scratch lease is deliberately NOT persistent: when the client exits,
 * do_spawn gives it back explicitly and synchronously (main.c:3766-3781) instead
 * of leaving an orphan of its own. Its resource lives on local /tmp, not on the
 * shared storage - nothing else ever looks at it.
 *
 * The cleanup it returns stops the client and removes the file; it is safe to
 * call once, in every outcome.
 */
func stale_release_caller() (int, func(), error) {
	var (
		scratch_path string
		spec string
		output []byte
		err error
		f *os.File
		cmd *exec.Cmd
		caller_pid int
		cleanup func()
	)
	scratch_path = fmt.Sprintf("%s.%d", STALE_RELEASE_PATH, os.Getpid())
	_ = os.Remove(scratch_path)
	f, err = os.OpenFile(scratch_path, os.O_WRONLY | os.O_CREATE | os.O_EXCL, 0600)
	if (err != nil) {
		return 0, nil, fmt.Errorf("cannot create %s: %s", scratch_path, err.Error())
	}
	_ = f.Close()
	spec = fmt.Sprintf("%s:%s:%s:%d", LOCK_SPACE, STALE_RELEASE_NAME, scratch_path, 0)
	output, err = run_sanlock(sanlock_cmd_timeout(), "client", "init", "-r", spec)
	if (err != nil) {
		_ = os.Remove(scratch_path)
		return 0, nil, fmt.Errorf("client init: %s (output %q)", err.Error(), strings.TrimSpace(string(output)))
	}
	cmd = exec.Command(paths.Get("SANLOCK"), "client", "spawn", "-r", spec,
	                   "-c", "2", STALE_CALLER_CMD, STALE_CALLER_ARG)
	err = cmd.Start()
	if (err != nil) {
		_ = os.Remove(scratch_path)
		return 0, nil, fmt.Errorf("client spawn: %s", err.Error())
	}
	caller_pid = cmd.Process.Pid
	cleanup = func() {
		var (
			alive bool = true
			i int
		)
		if (cmd.Process != nil) {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			for i = 0; i < STALE_CALLER_STOP_TRIES; i++ {
				if (cmd.Process.Signal(syscall.Signal(0)) != nil) {
					alive = false
					break
				}
				time.Sleep(STALE_CALLER_STOP_MS * time.Millisecond)
			}
			if (alive) {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
		}
		_ = os.Remove(scratch_path)
	}
	/*
	 * Only when the daemon lists the scratch lease is the client registered,
	 * and only then is it a legal caller for the release.
	 */
	output, err = run_sanlock(sanlock_cmd_timeout(), "client", "status")
	if (err == nil && scratch_registered(string(output))) {
		return caller_pid, cleanup, nil
	}
	for i := 0; i < STALE_CALLER_TRIES; i++ {
		time.Sleep(STALE_CALLER_WAIT_MS * time.Millisecond)
		output, err = run_sanlock(sanlock_cmd_timeout(), "client", "status")
		if (err == nil && scratch_registered(string(output))) {
			return caller_pid, cleanup, nil
		}
	}
	cleanup()
	return 0, nil, fmt.Errorf("the scratch holder %s did not register in time", STALE_RELEASE_NAME)
}

/*
 * release_orphan_with gives one orphan registration back through a registered
 * caller: `release -O 1` targets the orphan list only (cmd.c:657 ->
 * release_orphan, resource.c:2983-3008), so a live lease - the caller's own
 * scratch lease included - is never touched.
 */
/*
 * Registration_spec renders one local registration the way sanlock names it, so
 * that a caller can hand that one registration back.
 */
func Registration_spec(reg Local_registration) string {
	return fmt.Sprintf("%s:%s:%s:%d", reg.Lockspace, reg.Resource, reg.Path, reg.Offset)
}

/*
 * Release_orphan gives one orphaned registration of this host back through a
 * registered caller: sanlock's own release is the only thing that can do it, and
 * it has to come from a client that is registered with the daemon.
 *
 * The sweep above decides which orphans to give back at startup; this is the
 * same mechanism without the decision, for the case where the decision is made
 * elsewhere - a host that is about to take a resource again has to give its own
 * dead registration for it back first, or the new client cannot acquire it and
 * exits at once.
 */
func Release_orphan(spec string) error {
	var (
		caller_pid int
		cleanup func()
		err error
	)
	caller_pid, cleanup, err = stale_release_caller()
	if (err != nil) {
		return err
	}
	defer cleanup()
	return release_orphan_with(caller_pid, spec)
}

func release_orphan_with(caller_pid int, spec string) error {
	var (
		output []byte
		err error
	)
	output, err = run_sanlock(sanlock_cmd_timeout(), "client", "release", "-O", "1",
	                          "-p", strconv.Itoa(caller_pid), "-r", spec)
	return release_result(string(output), err)
}

/*
 * sweep_registrations is Release_stale_registrations with its inputs passed in,
 * so that every decision can be tested without a sanlock daemon: status() is one
 * `client status` output, leader() reads one resource's leader from the shared
 * storage, caller() starts a registered client and returns its pid plus the
 * cleanup for it, and release() gives one orphan back through that client.
 *
 * The caller is started only when there is something to release, and reused for
 * every registration this sweep gives back.
 */
func sweep_registrations(lockspace string, resource_prefix string, my_host_id uint64,
                         status func() (string, error),
                         leader func(resource string, path string) (Resource_leader, error),
                         caller func() (int, func(), error),
                         release func(caller_pid int, spec string) error) Registration_sweep {
	var (
		sweep Registration_sweep
		regs []Local_registration
		action Registration_action
		output string
		spec string
		l Resource_leader
		lerr error
		err error
		caller_pid int
		caller_cleanup func()
		started bool
	)
	defer func() {
		if (started && caller_cleanup != nil) {
			caller_cleanup()
		}
	}()
	output, err = status()
	if (err != nil) {
		sweep.Unknown++
		logger.Log("lockman: cannot read the local sanlock registrations (%s): releasing nothing", err.Error())
		return sweep
	}
	regs = Parse_registrations(output)
	for _, reg := range regs {
		l = Resource_leader{}
		lerr = nil
		if (reg.Orphan && reg.Lockspace == lockspace && strings.HasPrefix(reg.Resource, resource_prefix)) {
			l, lerr = leader(reg.Resource, reg.Path)
		}
		action = registration_action(reg, lockspace, resource_prefix, my_host_id, l, lerr)
		switch (action) {
		case REGISTRATION_RELEASE:
			if (!started) {
				caller_pid, caller_cleanup, err = caller()
				if (err != nil) {
					sweep.Failed++
					logger.Log("lockman: cannot start a registered caller (%s): leaving the stale registrations alone",
					           err.Error())
					return sweep
				}
				started = true
			}
			spec = fmt.Sprintf("%s:%s:%s:%d", reg.Lockspace, reg.Resource, reg.Path, reg.Offset)
			err = release(caller_pid, spec)
			if (err != nil) {
				sweep.Failed++
				logger.Log("lockman: cannot release the stale %s registration (%s): it stays, so this host may not be able to register that lease",
				           reg.Resource, err.Error())
				break
			}
			sweep.Released++
			logger.Log("lockman: released the stale %s registration (orphaned, and the shared lease is not ours)", reg.Resource)
		case REGISTRATION_KEEP_OURS:
			sweep.Kept_ours++
			logger.Debug("lockman: %s is still ours on the shared storage: keeping the orphan registration", reg.Resource)
		case REGISTRATION_KEEP_LIVE:
			sweep.Kept_live++
			logger.Debug("lockman: %s has a live client here: keeping it", reg.Resource)
		case REGISTRATION_SKIP_UNKNOWN:
			sweep.Unknown++
			logger.Log("lockman: cannot read the leader of %s (%s): not releasing its orphan registration (failed closed)",
			           reg.Resource, lerr.Error())
		default:
			sweep.Foreign++
		}
	}
	return sweep
}

/*
 * Release_stale_registrations gives back the orphaned resource leases this host
 * still holds in its own sanlock daemon but no longer owns on the shared storage
 * (the top of this file explains why that matters). lockman.Init calls it once,
 * after the lockspace is joined and before the coordinator classifies its first
 * row; it never fails - what it cannot do it logs - and a second call has
 * nothing left to do.
 *
 * The host_id is a parameter because lockman.Init holds lm.m while it calls
 * this: reading it here through Lockid() would take the read lock again and
 * deadlock the startup (measured 2026-10-07, on the first field run of this
 * sweep). Nothing in this file may read lockman's state for the same reason.
 */
func Release_stale_registrations(my_host_id uint16) {
	var (
		sweep Registration_sweep
	)
	sweep = sweep_registrations(LOCK_SPACE, RESOURCE_PREFIX, uint64(my_host_id),
		func() (string, error) {
			var (
				output []byte
				err error
			)
			output, err = run_sanlock(sanlock_cmd_timeout(), "client", "status")
			return string(output), err
		},
		func(resource string, path string) (Resource_leader, error) {
			return Read_resource_leader(resource, path)
		},
		stale_release_caller,
		release_orphan_with)
	logger.Debug("lockman: stale registrations: released %d, kept ours %d, kept live %d, foreign %d, unknown %d, failed %d",
	             sweep.Released, sweep.Kept_ours, sweep.Kept_live, sweep.Foreign, sweep.Unknown, sweep.Failed)
}
