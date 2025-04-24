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
		guestInfo []hypervisor.GuestInfo
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
	hostInfo, err = hv.GetHostInfo()
	if (err != nil) {
		logger.Fatal(err.Error())
	}
	guestInfo, err = hv.GuestInfo()
	if (err != nil) {
		logger.Fatal(err.Error())
	}
	/* service: initialize and first update with the host and guests information */
	service = virtx.New()
	err = service.UpdateHost(hostInfo)
	if (err != nil) {
		logger.Fatal(err.Error())
	}
	for _, gi := range guestInfo {
		if err := service.UpdateGuest(gi); err != nil {
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

	err = serfcomm.UpdateTags(hostInfo)
	if (err != nil) {
		logger.Log(err.Error())
	}
	/* serf: send Info Event with the host UUID to Serf */
	err = serfcomm.SendInfoEvent(service, hostInfo.Uuid)
	if (err != nil) {
		logger.Fatal(err.Error())
	}
	/* create subroutines to send and process events */
	hvShutdownCh := make(chan struct{})
	go serfcomm.SendHypervisorEvents(hv.EventsChannel(), hostInfo, hvShutdownCh)

	serfShutdownCh := make(chan struct{})
	go serfcomm.RecvSerfEvents(service, hostInfo, serfShutdownCh)

	/* create server subroutine to listen for API requests */
	go func() {
		err = service.ListenAndServe()
		if (err != nil && errors.Is(err, http.ErrServerClosed)) {
			logger.Log(err.Error())
		} else {
			logger.Fatal(err.Error())
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
