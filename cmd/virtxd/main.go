/*
 * Copyright (c) 2024-2026 SUSE LLC
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
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"
	"flag"

	"suse.com/virtx/pkg/serfcomm"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/virtx"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/lockman"
	"suse.com/virtx/pkg/paths"
	"suse.com/virtx/pkg/config"
	"suse.com/virtx/pkg/hastate"
	"suse.com/virtx/pkg/verdict"
	"suse.com/virtx/pkg/coord"
)

var version string = "unknown"

/*
 * apply_config_flag hands the flags the operator really gave to the
 * configuration: a flag that only fell back to its default was not given, and
 * recording it would turn a default into an override.
 */
func apply_config_flag(f *flag.Flag) {
	var (
		key string
		ok bool
	)
	key, ok = config.Flag_key(f.Name)
	if (!ok) {
		return
	}
	config.C.Override(key, f.Value.String())
}

func main() {
	var (
		err error
		debug *bool
		line string
		tier lockman.Tier
	)
	logger.Log("version %s", version)
	debug = flag.Bool("D", false, "add debug info to logs")

	/* runtime paths: initialize the runtime binary dependencies */
	paths.Init()

	/* configuration: this host's own settings, before anything reads them */
	config.Init()
	flag.Int("isolation-ping-timeout-seconds", config.C.Isolation_ping_timeout_seconds,
		"how long one ping to an isolation address may take, in seconds")
	flag.Int("sanlock-cmd-timeout-seconds", config.C.Sanlock_cmd_timeout_seconds,
		"how long one sanlock command may take, in seconds")
	flag.Int("hastate-round-budget-seconds", config.C.Hastate_round_budget_seconds,
		"how long one round of publishing and reading host reports may take, in seconds")
	flag.Parse()
	flag.Visit(apply_config_flag)
	logger.Set_debug(*debug)
	for _, line = range config.C.Lines() {
		logger.Log("%s", line)
	}

	/* hypervisor: initialize and start listening to hypervisor events */
	err = hypervisor.Connect()
	if (err != nil) {
		logger.Fatal("%s", err.Error())
	}
	/*
	 * hypervisor: wait for the system information to be filled before proceeding.
	 * Everything below relies on this: machine.Uuid(), machine.Arch() and a non-nil
	 * hv.si are only guaranteed once this returns. Do not move anything above it.
	 */
	err = hypervisor.Wait_system_info()
	if (err != nil) {
		logger.Fatal("%s", err.Error())
	}
	/* lockman: initialize the lock manager (needs system info to get the hypervisor Uuid()). */
	err = lockman.Init(machine.Uuid())
	if (err != nil) {
		logger.Fatal("%s", err.Error())
	}

	/*
	 * the effective sanlock tier: io_timeout and watchdog_fire_timeout, the two
	 * numbers every time window of the judgement layer is derived from, with
	 * the place they were read from - so that the startup log alone answers
	 * "which tier is this, and where did it come from".
	 */
	tier = lockman.Read_tier()
	for _, line = range tier.Lines() {
		logger.Log("%s", line)
	}
	verdict.Set_tier(tier)
	coord.Set_tier(tier)
	for _, line = range verdict.Window_lines() {
		logger.Log("%s", line)
	}
	for _, line = range coord.Window_lines() {
		logger.Log("%s", line)
	}
	/*
	 * A window that the tier does not derive is a wrong safety margin, so this
	 * check is fatal instead of a warning.
	 */
	err = verdict.Check_sanlock_tier_windows()
	if (err != nil) {
		logger.Fatal("%s", err.Error())
	}
	err = coord.Check_sanlock_tier_windows()
	if (err != nil) {
		logger.Fatal("%s", err.Error())
	}

	/* virtx service: initialize */
	virtx.Init()
	/*
	 * serf: initialize communication package, and then
	 * the actual serf client for RPC bi-directional comm,
	 * and add a tag entry for this host using its UUID
	 */
	err = serfcomm.Connect()
	if (err != nil) {
		logger.Fatal("%s", err.Error())
	}
	serfcomm.Start_listening(hypervisor.Get_vm_event_channel(), hypervisor.Get_system_info_channel())

	/*
	 * hastate: publish what this host sees about itself and the others, and
	 * never act on it. It reads the cluster policy with the rest of the shared
	 * storage, so it starts after the cluster view exists.
	 */
	hastate.Start()

	/*
	 * verdict: judge every known host once per round and log it. The loop acts
	 * only when the cluster policy has the action surface on, and even then an
	 * action needs an execution half to be installed, which nothing does yet.
	 */
	verdict.Start()

	/*
	 * coord: the coordinator role - who holds __VIRTX__COORD, whether that
	 * holder is still renewing, and which host contends for it when the office
	 * is empty. Holding the office is what makes a host the one that may act,
	 * so the role does not depend on the switch of the action surface: it also
	 * makes the host that will write a policy when an operator enables one.
	 *
	 * The execution half of the judgement is installed here rather than imported
	 * by pkg/verdict, because coord already imports verdict. It is what refuses to
	 * act unless this host holds the office, and what re-checks a landing before
	 * the VM is moved.
	 */
	verdict.Set_drive(coord.Drive_round)
	coord.Start()

	/* create server subroutine to listen for API requests */
	virtx_err_ch := virtx.Start_listening()

	/* prepare atexit function to shutdown service */
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(
			context.Background(),
			time.Second*5,
		)
		defer shutdownCancel()
		err = virtx.Shutdown(shutdownCtx)
		if (err != nil) {
			logger.Log("%s", err.Error())
			err = virtx.Close()
			if (err != nil) {
				logger.Log("%s", err.Error())
			}
			logger.Fatal("shutdown timed out, connections closed")
		}
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-c:
		logger.Log("Got signal: %d", sig)
		/*
		 * The coordinator hands the office over in its own goroutine, which
		 * received the same signal (signal.Notify only ever adds a receiver).
		 * Let that ending finish before this process goes away: it has to read
		 * /proc and ask sanlock once - tens to a few hundred milliseconds - and
		 * returning now cuts it off, which would leave the lease held by a
		 * client this exiting process never touched.
		 *
		 * Bounded and fail-soft: Wait_resign gives up after
		 * coord.RESIGN_WAIT_BUDGET and returns at once when there is nothing to
		 * give back, so a host that is not the coordinator waits not at all.
		 */
		coord.Wait_resign(coord.RESIGN_WAIT_BUDGET)
	case err = <-virtx_err_ch:
		logger.Log("VirtX service error: %s", err.Error())
	}
}
