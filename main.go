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
)

const (
	SerfRPCAddr = "127.0.0.1:7373"
)

func main() {
	var (
		err error
		logger *log.Logger
		hv *hypervisor.Hypervisor
		hostInfo hypervisor.HostInfo
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
	hostInfo, err = hv.HostInfo()
	if (err != nil) {
		logger.Fatal(err)
	}
	guestInfo, err = hv.GuestInfo()
	if (err != nil) {
		logger.Fatal(err)
	}
	/* service: initialize and first update with the host and guests information */
	service = virtx.New(logger)
	err = service.Update(hostInfo, guestInfo)
	if (err != nil) {
		logger.Fatal(err)
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
	addTags := map[string]string { "uuid": hostInfo.UUID }
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
	err = sendInfoEvent(service, serf, hostInfo.UUID, 1)
	if (err != nil) {
		logger.Fatal(err)
	}
	/* create subroutines to send and process events */
	hvShutdownCh := make(chan struct{})
	go forwardGuestEvents(hv.EventsChannel(), serf, hostInfo, logger, hvShutdownCh)

	serfShutdownCh := make(chan struct{})
	go processSerfEvents(serfCh, serf, service, hostInfo, logger, serfShutdownCh)

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

func processSerfEvents(
	serfCh <-chan map[string]interface{},
	serf *client.RPCClient,
	s *virtx.Service,
	hostInfo hypervisor.HostInfo,
	logger *log.Logger,
	shutdownCh chan<- struct{},
) {
	logger.Println("Processing events...")
	for e := range serfCh {
		switch e["Event"].(string) {
		case "user":
		case "member-leave":
			fallthrough
		case "member-failed":
			for _, m := range e["Members"].([]interface{}) {
				tags := m.(map[interface{}]interface{})["Tags"]
				uuid, ok := tags.(map[interface{}]interface{})["uuid"].(string)
				if !ok {
					logger.Println("failed to get uuid tag")
					continue
				}
				logger.Printf("Host %s OFFLINE", uuid)
				if err := s.SetHostOffline(uuid); err != nil {
					logger.Fatal(err)
				}
			}
			fallthrough
		default:
			continue
		}

		name := e["Name"].(string)
		payload := e["Payload"].([]byte)

		switch name {
		case serfcomm.HostInfoEvent:
			hi, newHost, err := serfcomm.UnpackHostInfoEvent(payload)
			if err != nil {
				logger.Fatal(err)
			}
			logger.Printf(
				"%s: %d %s %s newHost(%d)",
				name, hi.Seq, hi.UUID, hi.Hostname, newHost,
			)
			if err := s.UpdateHostState(hi); err != nil {
				logger.Fatal(err)
			}
			if newHost == 1 && hi.UUID != hostInfo.UUID {
				if err := sendInfoEvent(s, serf, hostInfo.UUID, 0); err != nil {
					logger.Fatal(err)
				}
			}
		case serfcomm.GuestInfoEvent:
			gi, hostUUID, err := serfcomm.UnpackGuestInfoEvent(payload)
			if err != nil {
				logger.Fatal(err)
			}
			logger.Printf(
				"%s: %d %s %s state(%d - %s) hostUUID(%s)",
				name, gi.Seq, gi.UUID, gi.Name,
				gi.State, hypervisor.GuestStateToString(gi.State),
				hostUUID,
			)
			if err := s.UpdateGuestState(hostUUID, gi); err != nil {
				logger.Fatal(err)
			}
		default:
			logger.Println("[UNKNOWN-EVENT]", name, payload)
		}
	}
	logger.Println("Processing done")
	close(shutdownCh)
}

func forwardGuestEvents(
	eventCh <-chan hypervisor.GuestInfo,
	serf *client.RPCClient,
	hostInfo hypervisor.HostInfo,
	logger *log.Logger,
	shutdownCh chan<- struct{},
) {
	logger.Println("Forwarding guest events...")
	for gi := range eventCh {
		if err := sendGuestInfo(serf, gi, hostInfo.UUID); err != nil {
			logger.Fatal(err)
		}
	}
	logger.Println("Forwarding done")
	close(shutdownCh)
}

func sendInfoEvent(s *virtx.Service, serf *client.RPCClient, uuid string, newHost int) error {
	s.RLock()
	defer s.RUnlock()

	hostState := s.HostState(uuid)

	if err := sendHostInfo(serf, hostState.HostInfo, newHost); err != nil {
		return err
	}

	for _, gi := range hostState.Guests {
		if err := sendGuestInfo(serf, gi, hostState.HostInfo.UUID); err != nil {
			return err
		}
	}

	return nil
}

func sendHostInfo(serf *client.RPCClient, hostInfo hypervisor.HostInfo, newHost int) error {
	payload, err := serfcomm.PackHostInfoEvent(hostInfo, newHost)
	if err != nil {
		return err
	}
	if err := serf.UserEvent(serfcomm.HostInfoEvent, payload, false); err != nil {
		return err
	}
	return nil
}

func sendGuestInfo(serf *client.RPCClient, guestInfo hypervisor.GuestInfo, hostUUID string) error {
	payload, err := serfcomm.PackGuestInfoEvent(guestInfo, hostUUID)
	if err != nil {
		return err
	}
	if err := serf.UserEvent(serfcomm.GuestInfoEvent, payload, false); err != nil {
		return err
	}
	return nil
}
