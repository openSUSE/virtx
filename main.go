package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/hashicorp/serf/client"

	"suse.com/virtXD/pkg/serfcomm"
	"suse.com/virtXD/pkg/hypervisor"
	"suse.com/virtXD/pkg/virtx"
	"suse.com/virtXD/pkg/model"
)

const (
	SerfRPCAddr = "127.0.0.1:7373"
)

func main() {
	var (
		err error
		logger *log.Logger
		hv *hypervisor.Hypervisor
		hostInfo openapi.Host
		guestInfo []hypervisor.GuestInfo
		service *virtx.Service
		serf *client.RPCClient
	)
	logger = log.New(os.Stderr, "", log.LstdFlags | log.Lshortfile)

	/* hypervisor: initialize and start listening to hypervisor events */
	hv, err = hypervisor.New(logger)
	if (err != nil) {
		logger.Fatal(err)
	}
	defer hv.Shutdown()
	err = hv.StartListening()
	if (err != nil) {
		logger.Fatal(err)
	}
	hostInfo, err = hv.GetHostInfo()
	if (err != nil) {
		logger.Fatal(err)
	}
	guestInfo, err = hv.GuestInfo()
	if (err != nil) {
		logger.Fatal(err)
	}
	/* service: initialize and first update with the host and guests information */
	service = virtx.New(logger)
	err = service.UpdateHost(hostInfo)
	if (err != nil) {
		logger.Fatal(err)
	}
	for _, gi := range guestInfo {
		if err := service.UpdateGuest(gi); err != nil {
			logger.Fatal(err)
		}
	}
	/*
     * serf: initialize RPC bi-directional communication with serf,
     * and add a tag entry for this host using its UUID
	 */
	serf, err = client.NewRPCClient(SerfRPCAddr)
	if (err != nil) {
		logger.Fatal(err)
	}
	defer serf.Close()
	addTags := map[string]string { "uuid": hostInfo.Uuid }
	removeTags := []string {}
	err = serf.UpdateTags(addTags, removeTags)
	if (err != nil) {
		logger.Fatal(err)
	}
	/* serf: create channel and stream to receive serf events */
	serfCh := make(chan map[string]interface{}, 64)
	var stream client.StreamHandle
	stream, err = serf.Stream("*", serfCh)
	if (err != nil) {
		logger.Fatal(err)
	}
	defer serf.Stop(stream)
	/* serf: send Info Event with the host UUID to Serf */
	err = serfcomm.SendInfoEvent(service, serf, hostInfo.Uuid)
	if (err != nil) {
		logger.Fatal(err)
	}
	/* create subroutines to send and process events */
	hvShutdownCh := make(chan struct{})
	go serfcomm.SendHypervisorEvents(hv.EventsChannel(), serf, hostInfo, logger, hvShutdownCh)

	serfShutdownCh := make(chan struct{})
	go serfcomm.RecvSerfEvents(serfCh, serf, service, hostInfo, logger, serfShutdownCh)

	/* create server subroutine to listen for API requests */
	go func() {
		err = service.ListenAndServe()
		if (err != nil && errors.Is(err, http.ErrServerClosed)) {
			logger.Println(err)
		} else {
			logger.Fatal(err)
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
			logger.Println(err)
			logger.Fatal(service.Close())
		}
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)

	select {
	case sig := <-c:
		logger.Println("Got signal:", sig)
	case <-hvShutdownCh:
		logger.Println("Hypervisor shutdown")
	case <-serfShutdownCh:
		logger.Println("Serf shutdown")
	}
}
