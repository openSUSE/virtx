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
 * The contending half of the coordinator role: how this host creates, takes
 * and holds the __VIRTX__COORD lease, and what it publishes while it holds it.
 *
 * Two decisions of 2026-10-04 are implemented literally here:
 *   - the first host that needs the lease creates it, following the
 *     "create first, then take" path of the design's row 5a;
 *   - the handover window equals MASK_WINDOW_MS (see coord.go).
 *
 * It deliberately does NOT reuse lockman.Run(): that one prepends
 * virtx-check-lvb, which insists the lock-value block holds a VM uuid, while this
 * lock-value block holds the epoch and the mask deadline. It is also
 * blocking, and the patrol must never block.
 */
package coord

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"suse.com/virtx/pkg/lockman"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/paths"
	. "suse.com/virtx/pkg/constants"

)

/*
 * what the spawned client runs while it holds the lease: it only has to stay
 * alive. -P 1 sets the resource's PERSISTENT flag (SANLK_RES_PERSISTENT 0x8):
 * if the holder pid exits, the lease is not released - it moves to the sanlock
 * daemon's orphan list and stays held by this host (sanlock.h:61-79,
 * resource.c:1482-1484). Nothing restarts the child: when it exits, do_spawn
 * releases the lease explicitly and synchronously (SANLK_REL_ALL) and the
 * client exits (main.c:3688-3787).
 */
const HOLDER_CMD = "/bin/sleep"
const HOLDER_ARG = "2147483647"

/* pending is a holder client we started but whose lease we do not own yet. */
var pending *exec.Cmd

/* Holder is this host's coordinator lease, once taken. */
type Holder struct {
	resource_path string
	/* the resource this holder holds: what the handover verifies the client
	 * against before anything is signalled (COORD_RESOURCE in this tree) */
	resource string
	cmd *exec.Cmd
	epoch int64
}

var holder *Holder

/*
 * office_mu guards holder, pending and the mutable fields of Holder.
 *
 * Both pointers are written by the resigning goroutine (release_with_ops) and by
 * the patrol (spawn, adopt), and read by the patrol, by the watcher and by the
 * shutdown goroutine (resign_settled through Wait_resign): every access goes
 * through the accessors below, a snapshot, or the field accessors - a plain read
 * of holder or pending from another goroutine is a data race (measured
 * 2026-10-08: go test -race reported release_with_ops against resign_settled).
 *
 * The lock is never held while a sanlock command runs, while a client is
 * signalled, or while the lock-value block is read or written: callers take a snapshot
 * the lock and act on it outside.
 */
var office_mu sync.Mutex

/* office_pending_get returns the client we started but whose lease we do not own
 * yet (nil when there is none). */
func office_pending_get() *exec.Cmd {
	office_mu.Lock()
	defer office_mu.Unlock()
	return pending
}

func office_pending_set(cmd *exec.Cmd) {
	office_mu.Lock()
	defer office_mu.Unlock()
	pending = cmd
}

/* office_holder_get returns this host's coordinator lease, once taken. */
func office_holder_get() *Holder {
	office_mu.Lock()
	defer office_mu.Unlock()
	return holder
}

func office_holder_set(h *Holder) {
	office_mu.Lock()
	defer office_mu.Unlock()
	holder = h
}

/* office_snapshot returns both pointers under one lock, for the callers that
 * have to see them as one state (blocked_by_holder, resign_settled,
 * release_with_ops). */
func office_snapshot() (*exec.Cmd, *Holder) {
	office_mu.Lock()
	defer office_mu.Unlock()
	return pending, holder
}

/* office_promote installs the pending client as the holder and clears pending
 * under one lock: settle() must never be observable as "both set" or
 * "both nil". */
func office_promote(h *Holder) {
	office_mu.Lock()
	defer office_mu.Unlock()
	holder = h
	pending = nil
}

/* office_clear forgets both clients; the caller has ended them already. */
func office_clear() {
	office_mu.Lock()
	defer office_mu.Unlock()
	pending = nil
	holder = nil
}

/* holder_epoch_get reads the term of the current holder. The epoch is the only
 * field of Holder that is written after publication (next_epoch), so it has its
 * own accessors rather than a lock on Holder itself: Holder is replaced, and a
 * lock on a value that gets swapped out locks the wrong object. */
func holder_epoch_get() int64 {
	office_mu.Lock()
	defer office_mu.Unlock()
	if (holder == nil) {
		return 0
	}
	return holder.epoch
}

func holder_epoch_set(e int64) {
	office_mu.Lock()
	defer office_mu.Unlock()
	if (holder != nil) {
		holder.epoch = e
	}
}

/* term_epoch is the term of the current holder, published for the goroutines
 * that are not allowed to read the holder itself (Term() and publish()). 0 =
 * none. It is written by the patrol when it takes office (adopt) and cleared in
 * release().
 *
 * At rest, "term_epoch != 0" and "holder != nil" mean the same thing. The two
 * transitions are not atomic, and each has one instant in which they disagree
 * (release(): holder nils first, then the term; adopt(): holder is set first,
 * then the term). term_epoch is an atomic; holder, pending and the mutable fields
 * of Holder are guarded by office_mu, so every read or write that can cross a
 * goroutine goes through the accessors (office_*, holder_epoch_*) or through a
 * snapshot - the shutdown goroutine reads them through Wait_resign's predicate,
 * which is exactly the pair the race detector flagged on 2026-10-08. Neither
 * value can authorise an action on its own: a reader that still sees the old term
 * one round late is stopped by the landing's stale-epoch and idempotency
 * re-checks, and a reader that briefly sees "not coordinator" merely declines to
 * act. Do not read this as one atomic state. */
var term_epoch atomic.Int64

/* lock_value_mu serialises the lock-value block writes: the patrol and the judgement loop
 * publish (the second one while a takeover is in flight). */
var lock_value_mu sync.Mutex

/*
 * next_epoch applies the design: the epoch lives in the lease's lock-value block, and
 * after acquiring we re-read it and move it forward. A payload we cannot parse
 * (a fresh resource, or a VM uuid) starts at 1.
 */
func next_epoch(payload string) int64 {
	var (
		p Published
		ok bool
	)
	p, ok = decode(payload)
	if (!ok) {
		return 1
	}
	return p.Epoch + 1
}

/* resource_path is where the coordinator lease lives: one flat file in the lock
 * directory, not the directory-per-resource layout the VM leases use. */
func resource_path() string {
	return lockman.Get_coord_path(COORD_RESOURCE)
}

/*
 * spec renders the sanlock resource specification, the same shape lockman uses
 * for VM leases: lockspace:resource:path:offset.
 */
func spec() string {
	return fmt.Sprintf("%s:%s:%s:%d", LOCK_SPACE, COORD_RESOURCE, resource_path(), 0)
}

/*
 * holder_still_holds answers the question the holder's fate turns on: is the client
 * this process keeps a handle on the one that is holding the lease?
 *
 * The obvious answers are both wrong. "The leader names us" is what adopt() trusted
 * when it promoted that client - and a measured round left behind exactly the case
 * where the leader named this host while a *different* client of this host held the lease
 * (measured 2026-10-07: h13 ran with holder(epoch=6) while the daemon's registration
 * was the long-lived client of the previous virtxd, pid 11306). term_epoch is no
 * better: it was filled from that same belief. Two local facts do settle it, and only
 * these two:
 *
 *  - the client process is gone (or a zombie). A sanlock client that *loses* the
 *    acquire exits by itself, and a dead client holds nothing, so a handle on it can
 *    never come back - that is the state a measured round left behind: a host
 *    believed it held the office for 3 min 47 s with no client at all.
 *  - the daemon still lists a registration of this resource, but under a different
 *    pid than ours. One host holds one lease, so ours is the loser; a measured round
 *    exactly that shape.
 *
 * Everything else counts as "still holds": no registration at all while the client is
 * alive (it may be between fork and register), an unreadable status, a Holder with no
 * resource name. Clearing a holder we cannot disprove would end the office of a host
 * that does hold it, which is the one thing this must never do.
 */
func holder_still_holds(ops Holder_ops, h *Holder) bool {
	var (
		out string
		err error
		pid int
		mine, other bool
	)
	if (h == nil || h.cmd == nil || h.cmd.Process == nil) {
		return false
	}
	pid = h.cmd.Process.Pid
	if (ops.Alive != nil && !ops.Alive(pid)) {
		return false
	}
	if (ops.Status == nil || h.resource == "") {
		return true
	}
	out, err = ops.Status()
	if (err != nil) {
		return true
	}
	for _, reg := range lockman.Parse_registrations(out) {
		if (reg.Resource != h.resource || reg.Orphan) {
			continue
		}
		if (reg.Pid == pid) {
			mine = true
		} else {
			other = true
		}
	}
	if (mine) {
		return true
	}
	if (other) {
		return false
	}
	return true
}

/*
 * blocked_by_holder is take()'s gate: an attempt already in flight, or a holder that
 * still holds the lease, is in the way. A holder that provably holds nothing is not.
 */
func blocked_by_holder(ops Holder_ops) bool {
	p, h := office_snapshot()
	if (p != nil) {
		return true
	}
	if (h == nil) {
		return false
	}
	/* holder_still_holds runs sanlock: it is called on the snapshot, outside the lock */
	return holder_still_holds(ops, h)
}

/*
 * drop_losing_holder_with gives up a holder that provably holds nothing, so that this
 * host can compete again: without it a host that once adopted a client which lost the
 * acquire stays out of the contest for good, because take() refuses to start while a
 * holder is set and the patrol's release branch only runs for roles that are neither
 * candidate nor keep. It ends the stray client the same way release() always has -
 * graceful first, bounded, never SIGKILL unless the client ignores SIGTERM - and
 * clears holder, pending and the term with it.
 */
func drop_losing_holder_with(ops Holder_ops) bool {
	var (
		pid int
		resource string
	)
	p, h := office_snapshot()
	if (h == nil || p != nil) {
		return false
	}
	/* the snapshot is used outside the lock: holder_still_holds runs sanlock */
	if (holder_still_holds(ops, h)) {
		return false
	}
	if (h.cmd != nil && h.cmd.Process != nil) {
		pid = h.cmd.Process.Pid
	}
	resource = h.resource
	logger.Log("coord: the holder client %d of %s holds nothing (it lost the acquire, or it exited): giving it up so this host can compete again",
	           pid, resource)
	release_with_ops(ops)
	return true
}

/* drop_losing_holder is drop_losing_holder_with on the real process tree. */
func drop_losing_holder() bool {
	return drop_losing_holder_with(default_holder_ops())
}

/*
 * own_registration finds the client of THIS host that the daemon says is holding the
 * resource, verified before it is believed. It returns 0 when there is none.
 *
 * A pid is only adopted when holder_child_pid() recognises it as a sanlock spawn of
 * that resource with exactly one child running the holder command: a pid that does not
 * answer that way may belong to something else entirely (pids are reused), and
 * signalling or waiting on a stranger is the one thing this must never do. A
 * registration the daemon marks ORPHAN is not it either - nothing holds the lease
 * then, and the startup sweep (pkg/lockman/stale.go) gives those back.
 *
 * What this is NOT: the attempt in flight. pending is a client *we* started and whose
 * acquire has not settled; adopt() handles that one.
 */
func own_registration(ops Holder_ops) (int, error) {
	var (
		out string
		err error
	)
	if (ops.Status == nil) {
		return 0, fmt.Errorf("no way to read the registrations")
	}
	out, err = ops.Status()
	if (err != nil) {
		return 0, err
	}
	for _, reg := range lockman.Parse_registrations(out) {
		if (reg.Resource != COORD_RESOURCE || reg.Orphan || reg.Pid <= 0) {
			continue
		}
		_, err = holder_child_pid(ops, reg.Resource, reg.Pid)
		if (err != nil) {
			logger.Debug("coord: the registration of %s under pid %d is not one of ours (%s)",
			             reg.Resource, reg.Pid, err.Error())
			continue
		}
		return reg.Pid, nil
	}
	return 0, nil
}

/*
 * Take the office over instead of taking it again.
 *
 * Why this exists: adopt() only ever settles an attempt in flight, and the only thing
 * that starts one is take(), which spawns a client. So a virtxd that starts while THIS
 * host already holds the lease - the holder client of the previous virtxd is still
 * running, which is exactly why there is no gap in the role - could only spawn a second
 * client, and that one can only lose to the client that already holds it (-EEXIST from
 * the daemon). Measured 2026-10-07: h13 kept a handle on such a client for 3 min 47 s
 * while the registration belonged to the older client, and h12, with the
 * handle cleared again, spawned a new doomed client on every patrol and moved the epoch
 * with it (epoch 22, 23, 24 ... every 5 s). Adopting the registration that is
 * there removes both: no second client, one epoch, and a handle that points at the
 * client that really holds the lease - which is also what the resignation needs to give
 * the lease back instead of ending a client that holds nothing.
 *
 * own_registration() is the gate: only a verified registration of this resource is
 * adopted, and anything else leaves the caller to take() the usual way. The epoch is
 * published ONCE, through the same epoch_from_lvb() the spawn path uses - if it cannot
 * be published, that function releases the lease, and claiming a lease whose payload we
 * cannot write is precisely what must not happen (row 5b).
 */
func take_over_registration_with(ops Holder_ops, publish func() bool) bool {
	var (
		pid int
		err error
		proc *os.Process
	)
	p, h := office_snapshot()
	if (h != nil || p != nil) {
		return false
	}
	pid, err = own_registration(ops)
	if (err != nil) {
		logger.Debug("coord: cannot read the registrations (%s): taking the lease the usual way", err.Error())
		return false
	}
	if (pid <= 0) {
		return false
	}
	proc, err = os.FindProcess(pid)
	if (err != nil) {
		logger.Log("coord: cannot get a handle on our own holder client %d (%s): taking the lease the usual way",
		           pid, err.Error())
		return false
	}
	taken := &Holder{ resource_path: resource_path(), resource: COORD_RESOURCE, cmd: &exec.Cmd{ Process: proc } }
	office_promote(taken)
	if (publish == nil || !publish()) {
		/* publish released it: we could not read or write the payload */
		return true
	}
	term_epoch.Store(holder_epoch_get())
	logger.Log("coord: took %s over from our own existing client %d as host_id %d, epoch %d",
	           COORD_RESOURCE, pid, lockman.Lockid(), holder_epoch_get())
	return true
}

/* take_over_registration is take_over_registration_with on the real world. */
func take_over_registration() bool {
	return take_over_registration_with(default_holder_ops(), func() bool {
		h := office_holder_get()
		if (h == nil) {
			return false
		}
		epoch_from_lvb(h.cmd)
		return office_holder_get() != nil
	})
}

/*
 * take starts a holder client and returns at once. It deliberately does not
 * wait for the lease: taking over the lease of a *dead* owner goes through
 * sanlock's own takeover logic and can take tens of seconds (measured
 * 2026-10-04: a 3 s bound made the attempt fail while the old owner was dead),
 * and the patrol must never block. Ownership is settled by
 * adopt() on a later patrol; a client that loses is killed there.
 */
func take() error {
	var (
		err error
		cmd *exec.Cmd
	)
	if (blocked_by_holder(default_holder_ops())) {
		return nil
	}
	/*
	 * Our own dead registration for this resource is the one thing that can keep
	 * us from taking it: give it back before the attempt.
	 */
	recover_own_orphan(default_holder_ops())
	/*
	 * Create first, then take (row 5a). Both benign outcomes come back
	 * as nil from Create_resource_flat: the file may already exist, or another
	 * host may have created it a moment ago (that is the race this function is
	 * meant to run into). So an error here is a real failure - a temporary file
	 * that vanished, a full or read-only storage, a permission problem - and it
	 * is never "it already exists".
	 */
	err = lockman.Create_resource_flat(COORD_RESOURCE, machine.Uuid())
	if (err != nil) {
		logger.Debug("coord: create %s failed: %s", COORD_RESOURCE, err.Error())
	}
	cmd = exec.Command(paths.Get("SANLOCK"), "client", "spawn", "-P", "1",
		"-r", spec(), "-c", "2", HOLDER_CMD, HOLDER_ARG)
	err = cmd.Start()
	if (err != nil) {
		return fmt.Errorf("spawn: %w", err)
	}
	office_pending_set(cmd)
	logger.Debug("coord: started a holder client for %s, waiting for it to win", COORD_RESOURCE)
	return nil
}

/*
 * recover_own_orphan gives back an orphaned registration of this host for the
 * coordinator resource, before the office is taken again.
 *
 * An orphan is a registration whose client is gone. It keeps the lease alive in
 * the daemon, so a new client cannot acquire it and exits at once; a host in
 * that state cannot take the office at all, and the take that keeps starting a
 * client which cannot win also keeps killing it, which leaves one more orphan
 * every patrol (measured 2026-10-12: a host whose own orphan blocked it took and
 * killed a client every five seconds for as long as it ran).
 *
 * The startup sweep (pkg/lockman/stale.go) gives orphans back; its rule keeps
 * one whose shared lease still names this host with a live timestamp, which is
 * exactly what an orphan of ours looks like from the outside - the orphan is
 * what keeps that lease alive. So the take asks for it directly.
 */
func recover_own_orphan(ops Holder_ops) {
	var (
		out string
		err error
		reg lockman.Local_registration
	)
	if (ops.Status == nil) {
		return
	}
	out, err = ops.Status()
	if (err != nil) {
		return
	}
	for _, reg = range lockman.Parse_registrations(out) {
		if (reg.Resource != COORD_RESOURCE || !reg.Orphan) {
			continue
		}
		err = lockman.Release_orphan(lockman.Registration_spec(reg))
		if (err != nil) {
			logger.Log("coord: cannot give this host's orphaned %s registration back (%s): the take may fail",
			           COORD_RESOURCE, err.Error())
			continue
		}
		logger.Log("coord: gave this host's orphaned %s registration back before taking it again", COORD_RESOURCE)
	}
}

/*
 * adopt settles an attempt that is in flight: when the lease says we own it,
 * keep that client and publish the epoch (re-read after acquiring and move it
 * forward; if the payload cannot be read, do not declare
 * ourselves - row 5b - which epoch_from_lvb handles by releasing).
 *
 * "The leader names someone else" is NOT yet a loss: while it still names the
 * old, dead owner, our client is simply working through sanlock's takeover
 * (measured 2026-10-04 - treating that as a loss made both candidates kill
 * their own client every patrol and nobody ever took over). We only drop the
 * attempt when the owner is a host_id that is still renewing.
 */
func adopt(me uint64, hosts []lockman.Host_lease) {
	var (
		leader lockman.Resource_leader
		err error
	)
	p := office_pending_get()
	if (p == nil) {
		return
	}
	leader, err = lockman.Read_resource_leader(COORD_RESOURCE, resource_path())
	if (err != nil || !leader.Found) {
		return
	}
	if (leader.Owner_id == me) {
		h := &Holder{ resource_path: resource_path(), resource: COORD_RESOURCE, cmd: p }
		office_promote(h)
		epoch_from_lvb(p)
		/* epoch_from_lvb may have released the office (it could not read or
		 * write the payload): re-read it under the lock before using it */
		h = office_holder_get()
		if (h == nil) {
			/* epoch_from_lvb released it: we could not read or write the payload */
			return
		}
		/*
		 * Publish the term with the office, not on the next patrol: the
		 * judgement loop may issue an instruction at any time, and until the
		 * snapshot is there it would be told "not the coordinator" and refuse
		 * (Term() and publish() read only this snapshot - see term_epoch).
		 */
		term_epoch.Store(holder_epoch_get())
		logger.Log("coord: took %s as host_id %d, epoch %d", COORD_RESOURCE, me, holder_epoch_get())
		return
	}
	for _, h := range hosts {
		if (h.Id == leader.Owner_id && h.State == "LIVE") {
			logger.Debug("coord: %s is held by the live host_id %d, dropping our attempt", COORD_RESOURCE, leader.Owner_id)
			release()
			return
		}
	}
	logger.Debug("coord: %s still names the non-renewing host_id %d, our attempt continues", COORD_RESOURCE, leader.Owner_id)
}

/*
 * epoch_from_lvb reads the payload, publishes the next epoch, and releases the
 * lease again if the payload cannot be read or written.
 */
func epoch_from_lvb(cmd *exec.Cmd) {
	var (
		payload string
		err error
		next int64
		p Published
	)
	h := office_holder_get()
	if (h == nil) {
		return
	}
	payload, err = lockman.Read_lvb(h.resource_path)
	if (err != nil) {
		logger.Log("coord: cannot read the LOCK_VALUE (%s): releasing the lease", err.Error())
		release()
		return
	}
	next = next_epoch(payload)
	p = Published{ Epoch: next, Mask_deadline_ms: 0 }
	err = lockman.Write_lvb(h.resource_path, encode(p))
	if (err != nil) {
		logger.Log("coord: cannot write the LOCK_VALUE (%s): releasing the lease", err.Error())
		release()
		return
	}
	/*
	 * The read-modify-write is serialised the way office_mu allows: the payload
	 * is read and written OUTSIDE the lock (they are sanlock calls), and the new
	 * epoch is published back under it, and only while the office is still the
	 * very object this payload was read for. That closes the window that
	 * matters - a release() on another goroutine can no longer be resurrected
	 * with an epoch computed for a lease we no longer hold, so the epoch cannot
	 * move backwards. It is not one atomic step, and it does not need to be:
	 * only the patrol goroutine runs this path (adopt and take_over_registration
	 * are its callers, and publish()/the judgement loop never call it).
	 */
	if (office_holder_get() == h) {
		holder_epoch_set(next)
	}
}

/*
 * publish refreshes the payload while we hold the lease. The mask deadline is
 * zero until a takeover is actually in flight, and then it is the furthest one
 * still running (the patrol asks mask_now() for it); the epoch is the value we
 * took office with.
 *
 * It is called from TWO goroutines - the patrol (this file) and the judgement
 * loop, which publishes the mask deadline of the instruction it is issuing - so
 * it must not touch `holder`. That variable belongs to the patrol, and
 * release() clears it without a lock: reading it here was a data race, and the
 * nil check followed by a dereference was a window in which release() could
 * make the dereference panic (there is no recover anywhere in this tree, so the
 * daemon would exit). Everything this function needs is either the published
 * snapshot of the term (term_epoch, written by the patrol when it takes office
 * and cleared when it lets go) or a pure function of the constants
 * (resource_path). There is nothing left to dereference, so there is no window
 * either.
 *
 * The write itself is serialised with the patrol's, rather than left to the
 * file system.
 */
func publish(mask_deadline_ms int64) {
	var (
		epoch int64
		err error
	)
	epoch = term_epoch.Load()
	if (epoch == 0) {
		/* not the coordinator (or not any more): there is no lease to write to */
		return
	}
	lock_value_mu.Lock()
	err = lockman.Write_lvb(resource_path(), encode(Published{
		Epoch: epoch, Mask_deadline_ms: mask_deadline_ms }))
	lock_value_mu.Unlock()
	if (err != nil) {
		logger.Log("coord: cannot refresh the LOCK_VALUE: %s", err.Error())
	}
}

/*
 * release gives the lease back, or abandons an attempt that has not won.
 *
 * It asks the holder client to exit with SIGTERM and only falls back to SIGKILL
 * if that does not work. That does not hand the lease back: what decides that is
 * whether the client gets to run its own explicit release, and it does so
 * whenever the holder's CHILD ends - however it ends - while signalling the
 * CLIENT bypasses it. Measured on scratch resources 2026-10-07 (E3 of
 * virtx-ha-租约语义受控实验-2026-10-07.md), one case per ending:
 *
 *  - the child exits by itself (the command behind -c finishes; client exit
 *    status 0): do_spawn's waitpid returns and it releases the lease explicitly
 *    and synchronously (main.c:3766-3781). The registration is gone, the leader's
 *    timestamp is 0, and this gives the lease back even for a -P 1 holder;
 *  - the CHILD is signalled (client exit status 1): the same release runs - the
 *    client's own log shows "release" / "release done 0" - so this is equally
 *    clean. The status 1 is the child's failure, not a failed release;
 *  - the CLIENT is signalled, which is what release() does (client exit status
 *    143): do_spawn never runs that release, the daemon sees the client die, and a
 *    persistent resource stays held by this host in the daemon's orphan list
 *    (sanlock.h:61-79, resource.c:1482-1484) while the resource file is left
 *    untouched. Measured 2026-10-07: `kill -TERM` on a holder left the process and
 *    its `p <pid>` line gone, but the resource line became
 *
 *	r __VIRTX__DISKS__:__VIRTX__COORD:/vms/lock/__VIRTX__COORD:0:15 p 7072 ORPHAN
 *
 *    and stayed that way (checked 20 s and two minutes later); ORPHAN comes from
 *    client_cmd.c:106.
 *
 * So the client's exit status is not a success criterion for this (1 is the
 * normal outcome of the child-signalled case), and a graceful handover - giving
 * the lease back on purpose instead of leaving an orphan for the next startup to
 * sweep - would have to end the holder's CHILD, not signal the client. That is a
 * change to what release() does and needs its own task and verification: it is
 * registered here, not implemented.
 *
 * Giving such an orphan back after the fact takes `sanlock client release -O 1`
 * from a registered caller (SANLK_REL_ORPHAN, main.c:2781-2784 and 3978; the daemon
 * then walks its orphan list only, cmd.c:657 -> release_orphan,
 * resource.c:2983-3008; pkg/lockman/stale.go starts that caller), or a restart of
 * that host's sanlock daemon.
 *
 * A holder from an earlier virtxd is not in this process's handles at all
 * (neither pending nor holder), so release() cannot signal it - and the daemon
 * then refuses a new acquire of that resource: -EEXIST while the registration is
 * live (acquire_token "resource exists", resource.c:2016-2021), -EUCLEAN while
 * it is an orphan (acquire_token "found orphan", resource.c:2027-2031). That is
 * per resource, not per lockspace (find_resource matches the lockspace name and
 * the resource name, resource.c:1500-1513), and it is how a host stops being
 * able to register a coordinator lease after leaving a registration behind;
 * lockman's startup sweep gives the stale ones back (pkg/lockman/stale.go).
 *
 * One observation is kept as it was made, and left unexplained: on 2026-10-04,
 * after a holder had been killed, `sanlock client spawn` kept failing and the
 * session recorded "register done -13". The register step cannot report a lease
 * conflict (it is only the client's socket handshake, client.c:1241-1259, and
 * the daemon's handler only allocates token slots, cmd.c:3490-3516), and the
 * documented lease-level refusals are -EEXIST and -EUCLEAN (resource.c:2016-2021
 * and resource.c:2027-2031) - neither of which is -13 (EACCES). Where that error
 * came from is still unknown: it is kept as an open observation, not as an
 * explanation of the orphan behaviour above.
 */
func release() {
	release_with_ops(default_holder_ops())
}

/*
 * release_with_ops is release() with the handover's outside world passed in: the
 * resignation (resign.go) uses a shorter graceful wait, because a shutdown must not
 * sit for the full bound. Which clients are ended and the bookkeeping at the end are
 * the same either way.
 */
func release_with_ops(ops Holder_ops) {
	var (
		resource string
	)
	p, h := office_snapshot()
	h_cmd := (*exec.Cmd)(nil)
	h_resource := ""
	if (h != nil) {
		h_cmd = h.cmd
		h_resource = h.resource
	}
	for _, c := range []*exec.Cmd{ p, h_cmd } {
		if (c == nil || c.Process == nil) {
			continue
		}
		/*
		 * A pending client is always the coordinator lease (the only thing this
		 * package takes, and the only thing a holder this process started can
		 * hold); a Holder carries its resource, which the handover uses to
		 * verify the client before anything is signalled.
		 */
		resource = COORD_RESOURCE
		if (h != nil && h_cmd == c && h_resource != "") {
			resource = h_resource
		}
		end_holder_client(c, ops, resource)
	}
	if (h != nil) {
		logger.Log("coord: released %s", COORD_RESOURCE)
	}
	office_clear()
	/* the term goes with the lease: an action may not be driven on a term this
	 * host no longer holds */
	term_epoch.Store(0)
}

/*
 * end_holder_client ends one holder client this process started, the way that
 * gives the lease back. It first asks the holder's CHILD to stop - measured on
 * scratch resources 2026-10-07 (E3 of virtx-ha-租约语义受控实验-2026-10-07.md):
 * the client's waitpid returns and it releases the lease explicitly and
 * synchronously (main.c:3766-3781), nothing stays registered, and the client
 * exits by itself - so ★no signal is sent to the client at all★.
 *
 * Only when that cannot be done or does not give the lease back cleanly (the child
 * cannot be identified or verified, the ending failed, the registration is still
 * there, or an orphan was left) does it fall back to signalling the CLIENT: that is
 * this tree's old behaviour, it leaves an orphan this host keeps holding, and
 * lockman's startup sweep gives it back later (pkg/lockman/stale.go).
 *
 * Both waits are bounded - the graceful one by the capability's own bound, the
 * fallback by the 2 s this code always had - and SIGKILL is only ever used on the
 * fallback path, when the client ignored SIGTERM. It reports which path it took.
 */
/*
 * process_zombie reports whether a pid is a zombie: it has exited, and it waits
 * for this process to reap it. /proc/<pid>/stat puts the state after the command
 * name, which may itself contain spaces and parentheses - so the state is read
 * after the last ")".
 */
func process_zombie(pid int) bool {
	var (
		data []byte
		err error
		cut int
	)
	data, err = os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if (err != nil) {
		return true
	}
	cut = strings.LastIndex(string(data), ")")
	if (cut < 0 || cut + 2 >= len(data)) {
		return true
	}
	return (string(data)[cut + 2] == 'Z')
}

func end_holder_client(c *exec.Cmd, ops Holder_ops, resource string) (graceful bool) {
	var (
		end Graceful_end
		err error
		i int
		client_pid int
	)
	client_pid = c.Process.Pid
	_, err = holder_child_pid(ops, resource, client_pid)
	if (err != nil) {
		logger.Log("coord: cannot identify the child of the holder client %d (%s): falling back to signalling the client",
		           client_pid, err.Error())
	} else {
		end, err = end_holder_gracefully(ops, resource, client_pid)
		if (err != nil) {
			logger.Log("coord: cannot end the holder child of the client %d (%s): falling back to signalling the client",
			           client_pid, err.Error())
		} else if (end.Registration_gone && !end.Orphan_left) {
			logger.Log("coord: the holder client %d gave %s back after its child was signalled (client exited: %v, waited %s)",
			           client_pid, resource, end.Client_exited, end.Waited.String())
			if (proc_client_alive(client_pid)) {
				logger.Log("coord: the holder client %d is still running but holds nothing: not signalling it", client_pid)
			} else {
				/* gone, or a zombie we still have to reap */
				_ = c.Wait()
			}
			return true
		} else {
			logger.Log("coord: the holder client %d did not give %s back cleanly (client exited: %v, registration gone: %v, orphan left: %v): falling back to signalling the client",
			           client_pid, resource, end.Client_exited, end.Registration_gone, end.Orphan_left)
		}
	}
	if (c.ProcessState != nil || !process_alive(client_pid) || process_zombie(client_pid)) {
		/*
		 * The client is already gone, or a zombie this process has not reaped.
		 * Signalling it or killing it would change nothing for it, and the kill
		 * is what leaves an orphan behind: its placeholder child dies with it
		 * and the daemon keeps the lease as an orphan of this host, which then
		 * blocks the next take.
		 */
		logger.Log("coord: the holder client %d is already gone: not signalling it", client_pid)
		_ = c.Wait()
		return false
	}
	_ = c.Process.Signal(syscall.SIGTERM)
	for i = 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		if (c.ProcessState != nil || !process_alive(client_pid) || process_zombie(client_pid)) {
			break
		}
	}
	if (process_alive(client_pid)) {
		logger.Log("coord: a holder client ignored SIGTERM, killing it (this may leave an orphan lease)")
		_ = c.Process.Kill()
	}
	_ = c.Wait()
	return false
}

/* holder_cmd is the command of the current holder, if any. */
func holder_cmd() *exec.Cmd {
	h := office_holder_get()
	if (h == nil) {
		return nil
	}
	return h.cmd
}

/* process_alive reports whether a pid still exists. */
func process_alive(pid int) bool {
	var (
		err error
	)
	if (pid <= 0) {
		return false
	}
	err = syscall.Kill(pid, 0)
	return (err == nil)
}

/* Result reports what one act() call did, for the patrol's log line. */
func Result() string {
	h := office_holder_get()
	if (h == nil) {
		return "none"
	}
	return fmt.Sprintf("holder(epoch=%d)", h.epoch)
}

/*
 * Term reports the coordinator term this host holds the lease in, which is
 * "the current epoch is known" as far as this host can vouch for it.
 * ok is false when this host is not the coordinator, and that is the only
 * answer an action may be driven on: only the coordinator may authorise.
 *
 * The holder itself belongs to the patrol goroutine, so the term is published
 * to other goroutines (the judgement loop) through term_epoch: 0 means "not the
 * coordinator". It is written by adopt() with the office and cleared inside
 * release(), so at rest the term and the holder mean the same thing (each of
 * the two transitions has one instant in which they disagree - see term_epoch)
 * - and so that a reader never touches the holder at all (publish() reads the
 * same snapshot, which is what makes it race-free).
 *
 * Note what it is not: it is not the epoch to put on an instruction. The
 * instruction's epoch is read by the landing host from the lock-value block
 * (Observe_landing), and a value from memory must never decide whether a
 * ruling is stale.
 */
func Term() (int64, bool) {
	var (
		e int64 = term_epoch.Load()
	)
	if (e == 0) {
		return 0, false
	}
	return e, true
}
