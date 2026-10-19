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
 * The pieces one needs to hand the coordinator lease over gracefully. Nothing in
 * this tree calls them yet - release() (actuate.go) still signals the holder
 * CLIENT, and that always leaves the lease in this host's daemon as an orphan.
 * Measured on scratch resources 2026-10-07 (E3 of
 * virtx-ha-租约语义受控实验-2026-10-07.md), one case per way a -P 1 holder ends:
 *
 *  - the child exits by itself: the client's waitpid returns and it releases the
 *    lease explicitly and synchronously (main.c:3766-3781) - client exit status
 *    0, no registration left, the leader's timestamp set to 0;
 *  - the CHILD is signalled: the same release runs (the client's own log shows
 *    "release" / "release done 0") - client exit status 1, which is the child's
 *    failure and not a failed release - and the outcome is equally clean;
 *  - the CLIENT is signalled: that release never runs - client exit status 143 -
 *    the resource file is left untouched, and the lease stays held by this host in
 *    the daemon's orphan list (sanlock.h:61-79, resource.c:1482-1484).
 *
 * So a graceful handover ends the holder's CHILD. Wiring that into release() is
 * Step 2 and needs its own task and verification; this file only provides the
 * capability and reports what it found.
 *
 * Two rules are deliberately strict, because a wrong pid must never be signalled:
 * the client has to look like a sanlock client for the resource in question, and
 * its child has to be exactly one process whose command is the holder command this
 * tree starts (HOLDER_CMD, actuate.go). Anything else is refused, and this code
 * never escalates to SIGKILL - that is the caller's decision.
 */
package coord

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"suse.com/virtx/pkg/lockman"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/paths"
)

/* how long the client is given to exit on its own after its child was signalled */
const HANDOVER_WAIT = 10 * time.Second
const HANDOVER_POLL = 200 * time.Millisecond

/* one status read must not hang this decision */
const HANDOVER_STATUS_TIMEOUT = 5 * time.Second

/*
 * Holder_ops is everything end_holder_gracefully needs from the outside world:
 * /proc and the sanlock tool in the real thing, and fakes in the tests.
 */
type Holder_ops struct {
	/* one process's cmdline, NULs turned into spaces */
	Client_cmdline func(pid int) (string, error)
	/* the pids of one process's children */
	Children func(pid int) ([]int, error)
	/* one process's cmdline, NULs turned into spaces */
	Cmdline func(pid int) (string, error)
	/* is that pid still there? */
	Alive func(pid int) bool
	/* send one signal to one pid */
	Signal func(pid int, sig syscall.Signal) error
	/* one `sanlock client status` output */
	Status func() (string, error)
	/* wait one poll interval (injected so the tests do not sleep) */
	Sleep func(d time.Duration)
	Max_wait time.Duration
	Poll time.Duration
}

/*
 * Graceful_end is what one graceful ending found and did. It answers the three
 * questions the caller has to know: did the child get signalled, did the client
 * exit by itself, and what happened to the registration in the daemon's view.
 */
type Graceful_end struct {
	Client_pid int
	Child_pid int
	Signalled bool
	Client_exited bool
	Waited time.Duration
	Registration_gone bool
	Orphan_left bool
}

/*
 * holder_child_pid finds the child of one holder client and verifies that it is
 * the command this tree starts the holders with. Everything unexpected is refused:
 * the client has to be a sanlock client for this resource, it has to have exactly
 * one child, and that child's command has to be HOLDER_CMD.
 *
 * A client that has just been started registers before it forks its child, so a
 * caller that looks too early gets a refusal and has to decide whether to retry;
 * for a holder that is already holding, the child is there.
 */
func holder_child_pid(ops Holder_ops, resource string, client_pid int) (int, error) {
	var (
		line string
		kids []int
		child string
		fields []string
		err error
	)
	line, err = ops.Client_cmdline(client_pid)
	if (err != nil) {
		return 0, fmt.Errorf("cannot read the cmdline of pid %d: %s", client_pid, err.Error())
	}
	if (!strings.Contains(line, "spawn") || !strings.Contains(line, resource)) {
		return 0, fmt.Errorf("pid %d is not a holder client of %s (%q)", client_pid, resource, line)
	}
	kids, err = ops.Children(client_pid)
	if (err != nil) {
		return 0, fmt.Errorf("cannot read the children of pid %d: %s", client_pid, err.Error())
	}
	if (len(kids) != 1) {
		return 0, fmt.Errorf("pid %d has %d children, want exactly one holder command", client_pid, len(kids))
	}
	child, err = ops.Cmdline(kids[0])
	if (err != nil) {
		return 0, fmt.Errorf("cannot read the cmdline of pid %d: %s", kids[0], err.Error())
	}
	fields = strings.Fields(child)
	if (len(fields) == 0 || fields[0] != HOLDER_CMD) {
		return 0, fmt.Errorf("pid %d runs %q, not the holder command %s", kids[0], child, HOLDER_CMD)
	}
	return kids[0], nil
}

/*
 * end_holder_gracefully hands one lease over the way a measured round found to be clean: it
 * SIGTERMs the holder's CHILD (never the client) and then waits, bounded, for the
 * client to exit on its own and run its explicit release. It reports what it saw -
 * the client exiting, the registration disappearing, an orphan being left - and it
 * never escalates to SIGKILL: that is the caller's decision. A wrong or ambiguous
 * process tree is refused before anything is signalled.
 */
func end_holder_gracefully(ops Holder_ops, resource string, client_pid int) (Graceful_end, error) {
	var (
		end Graceful_end
		child int
		output string
		waited time.Duration
		tries int
		i int
		err error
	)
	end.Client_pid = client_pid
	child, err = holder_child_pid(ops, resource, client_pid)
	if (err != nil) {
		return end, err
	}
	end.Child_pid = child
	err = ops.Signal(child, syscall.SIGTERM)
	if (err != nil) {
		return end, fmt.Errorf("cannot signal the holder child %d: %s", child, err.Error())
	}
	end.Signalled = true
	logger.Log("coord: signalled the holder child %d of client %d (SIGTERM): the client should release %s on its own",
	           child, client_pid, resource)
	if (ops.Poll <= 0) {
		ops.Poll = HANDOVER_POLL
	}
	if (ops.Max_wait <= 0) {
		ops.Max_wait = HANDOVER_WAIT
	}
	tries = int(ops.Max_wait / ops.Poll)
	for i = 0; i < tries; i++ {
		ops.Sleep(ops.Poll)
		waited += ops.Poll
		if (!ops.Alive(client_pid)) {
			end.Client_exited = true
			break
		}
	}
	end.Waited = waited
	if (end.Client_exited) {
		logger.Log("coord: the holder client %d exited by itself after %s", client_pid, waited.String())
	} else {
		logger.Log("coord: the holder client %d did not exit within %s; not escalating to SIGKILL here",
		           client_pid, ops.Max_wait.String())
	}
	output, err = ops.Status()
	if (err != nil) {
		logger.Log("coord: cannot read the registrations after ending the holder of %s (%s)", resource, err.Error())
		return end, nil
	}
	end.Registration_gone = true
	for _, reg := range lockman.Parse_registrations(output) {
		if (reg.Resource != resource) {
			continue
		}
		end.Registration_gone = false
		if (reg.Orphan) {
			end.Orphan_left = true
		}
	}
	if (end.Orphan_left) {
		logger.Log("coord: %s is still registered as ORPHAN after its child was signalled: this host keeps holding that lease",
		           resource)
	} else if (end.Registration_gone) {
		logger.Log("coord: %s is not registered here any more: the lease was given back", resource)
	} else {
		logger.Log("coord: %s still has a live registration: the client did not exit", resource)
	}
	return end, nil
}

/*
 * default_holder_ops is the real thing: /proc for the process tree and the real
 * sanlock tool for the registrations.
 */
func default_holder_ops() Holder_ops {
	return Holder_ops{
		Client_cmdline: proc_cmdline,
		Children: proc_children,
		Cmdline: proc_cmdline,
		Alive: proc_client_alive,
		Signal: proc_signal,
		Status: holder_status,
		Sleep: time.Sleep,
		Max_wait: HANDOVER_WAIT,
		Poll: HANDOVER_POLL,
	}
}

/*
 * proc_client_alive reports whether the holder client is still there as a RUNNING
 * process. A zombie counts as exited: it has already run its explicit release, and
 * the caller waiting for it must not be held up by a child it has not reaped yet
 * (measured on h11 2026-10-07: the client sat as "[sanlock] <defunct>" while the
 * lease had already been given back, and process_alive() - which only asks the
 * kernel whether the pid exists - would have waited the full bound). This is
 * deliberately a separate function: release() keeps using process_alive().
 */
func proc_client_alive(pid int) bool {
	var (
		output []byte
		err error
	)
	if (pid <= 0) {
		return false
	}
	output, err = os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if (err != nil) {
		return false
	}
	return client_alive(stat_state(string(output)))
}

/*
 * client_alive reads one process state (the field /proc/<pid>/stat carries after
 * the command) and answers what the handover needs to know: Z and X mean the
 * process has finished, and a state we could not read does not count as running
 * either - the registration check that follows decides what really happened.
 */
func client_alive(state string) bool {
	return (state != "" && state != "Z" && state != "X")
}

/*
 * stat_state picks the process state out of one /proc/<pid>/stat line. The command
 * in that line sits in parentheses and may itself contain spaces and parentheses,
 * so the state is the first field after the LAST ')'.
 */
func stat_state(content string) string {
	var (
		close_paren int
		fields []string
	)
	close_paren = strings.LastIndex(content, ")")
	if (close_paren < 0 || close_paren+1 >= len(content)) {
		return ""
	}
	fields = strings.Fields(content[close_paren+1:])
	if (len(fields) == 0) {
		return ""
	}
	return fields[0]
}

/* proc_cmdline reads one process's cmdline, with its NUL separators turned into
 * spaces, so that the shape is easy to match. */
func proc_cmdline(pid int) (string, error) {
	var (
		output []byte
		err error
	)
	if (pid <= 0) {
		return "", fmt.Errorf("pid %d is not a process", pid)
	}
	output, err = os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if (err != nil) {
		return "", err
	}
	return strings.TrimSpace(strings.ReplaceAll(string(output), "\x00", " ")), nil
}

/* proc_children reads the pids of one process's children (the kernel's own list
 * for that thread group, so no /proc scan is needed). */
func proc_children(pid int) ([]int, error) {
	var (
		output []byte
		kids []int
		value int
		err error
	)
	if (pid <= 0) {
		return nil, fmt.Errorf("pid %d is not a process", pid)
	}
	output, err = os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", pid, pid))
	if (err != nil) {
		return nil, err
	}
	for _, field := range strings.Fields(string(output)) {
		value, err = strconv.Atoi(field)
		if (err != nil) {
			continue
		}
		kids = append(kids, value)
	}
	return kids, nil
}

/* proc_signal sends one signal to one pid. */
func proc_signal(pid int, sig syscall.Signal) error {
	if (pid <= 0) {
		return fmt.Errorf("pid %d is not a process", pid)
	}
	return syscall.Kill(pid, sig)
}

/* holder_status is one `sanlock client status` output, bounded by a timeout so
 * that a stuck tool cannot hang the ending. */
func holder_status() (string, error) {
	var (
		ctx context.Context
		cancel context.CancelFunc
		cmd *exec.Cmd
		output []byte
		err error
	)
	ctx, cancel = context.WithTimeout(context.Background(), HANDOVER_STATUS_TIMEOUT)
	defer cancel()
	cmd = exec.CommandContext(ctx, paths.Get("SANLOCK"), "client", "status")
	output, err = cmd.Output()
	if (err != nil) {
		return string(output), err
	}
	return string(output), nil
}
