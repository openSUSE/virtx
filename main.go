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
	"errors"
	"net/http"
	"os"
	"os/signal"
	"time"

	"suse.com/virtXD/pkg/serfcomm"
	"suse.com/virtXD/pkg/hypervisor"
	"suse.com/virtXD/pkg/virtx"
	"suse.com/virtXD/pkg/model"
	"suse.com/virtXD/pkg/logger"
)

const (
	SerfRPCAddr = "127.0.0.1:7373"
)

func main() {
	var (
		err error
		hv *hypervisor.Hypervisor
		hostInfo openapi.Host
		vms []openapi.Vm
		service *virtx.Service
	)
	/* hypervisor: initialize and start listening to hypervisor events */
	hv, err = hypervisor.New()
	if (err != nil) {
		logger.Fatal(err.Error())
	}
	defer hv.Shutdown()
	err = hv.StartListening()
	if (err != nil) {
		logger.Fatal(err.Error())
	}
	hostInfo, vms, err = hv.GetSystemInfo()
	if (err != nil) {
		logger.Fatal(err.Error())
	}
	/* service: initialize and first update with the system information */
	service = virtx.New()
	err = service.UpdateHost(&hostInfo)
	if (err != nil) {
		logger.Fatal(err.Error())
	}
	for i, _ := range vms {
		if err := service.UpdateVm(&vms[i]); err != nil {
			logger.Fatal(err.Error())
		}
	}
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

	err = serfcomm.UpdateTags(&hostInfo)
	if (err != nil) {
		logger.Log(err.Error())
	}
	/* serf: send user Events for host and vms to Serf */
	err = serfcomm.SendInfo(&hostInfo, vms)
	if (err != nil) {
		logger.Fatal(err.Error())
	}
	/* create subroutines to send and process events */
	hvShutdownCh := make(chan struct{})
	go serfcomm.SendVmEvents(hv.EventsChannel(), hvShutdownCh)

	serfShutdownCh := make(chan struct{})
	go serfcomm.RecvSerfEvents(service, serfShutdownCh)

	/* create server subroutine to listen for API requests */
	go func() {
		err = service.ListenAndServe()
		if (err != nil && errors.Is(err, http.ErrServerClosed)) {
			logger.Log(err.Error())
		} else {
			logger.Log(err.Error())
		}
	}()

	/* prepare atexit function to shutdown service */
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(
			context.Background(),
			time.Second*5,
		)
		defer shutdownCancel()
		err = service.Shutdown(shutdownCtx)
		if (err != nil) {
			logger.Log(err.Error())
			logger.Fatal(service.Close().Error())
		}
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)

	select {
	case sig := <-c:
		logger.Log("Got signal: %d", sig)
	case <-hvShutdownCh:
		logger.Log("Hypervisor shutdown")
	case <-serfShutdownCh:
		logger.Log("Serf shutdown")
	}
}
