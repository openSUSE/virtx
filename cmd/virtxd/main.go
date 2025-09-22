/*
 * Copyright (c) 2024-2025 SUSE LLC
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
	"time"

	"suse.com/virtx/pkg/serfcomm"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/virtx"
	"suse.com/virtx/pkg/logger"
)

const (
	SerfRPCAddr = "127.0.0.1:7373"
)

var version string = "unknown"

func main() {
	var (
		err error
	)
	logger.Log("version %s", version)

	/* hypervisor: initialize and start listening to hypervisor events */
	err = hypervisor.Connect()
	if (err != nil) {
		logger.Fatal(err.Error())
	}
	defer hypervisor.Shutdown()

	/* virtx service: initialize */
	virtx.Init()
	/*
	 * serf: initialize communication package, and then
	 * the actual serf client for RPC bi-directional comm,
	 * and add a tag entry for this host using its UUID
	 */
	err = serfcomm.Init(SerfRPCAddr)
	if (err != nil) {
		logger.Fatal(err.Error())
	}
	defer serfcomm.Shutdown()
	serf_shutdown_ch := make(chan struct{})
	/*
	 * start listening for outgoing VMEvents and SystemInfo and incoming serf events.
	 */
	serfcomm.Start_listening(hypervisor.GetVmEventCh(), hypervisor.GetSystemInfoCh(), serf_shutdown_ch)

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
			logger.Log(err.Error())
			logger.Fatal(virtx.Close().Error())
		}
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)

	select {
	case sig := <-c:
		logger.Log("Got signal: %d", sig)
	case <-serf_shutdown_ch:
		logger.Log("Serf shutdown")
	case err = <-virtx_err_ch:
		logger.Log("VirtX service error: %s", err.Error())
	}
}
