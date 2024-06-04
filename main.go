package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/google/uuid"

	"github.com/hashicorp/serf/client"

	"suse.com/inventory-service/pkg/comm"
	"suse.com/inventory-service/pkg/hypervisor"
	"suse.com/inventory-service/pkg/inventory"
)

const (
	SerfRPCAddr = "127.0.0.1:7373"
)

func main() {
	logger := log.New(os.Stderr, "", log.LstdFlags|log.Lshortfile)

	hv, err := hypervisor.New(logger)
	if err != nil {
		logger.Fatal(err)
	}
	defer hv.Shutdown()

	eventCh := make(chan hypervisor.GuestInfo, 64)
	watch, err := hv.Watch(eventCh)
	if err != nil {
		logger.Fatal(err)
	}
	defer hv.Stop(watch)

	hostInfo, err := hv.HostInfo()
	if err != nil {
		logger.Fatal(err)
	}
	guestInfo, err := hv.GuestInfo()
	if err != nil {
		logger.Fatal(err)
	}

	s := inventory.NewService(logger)
	if err := s.Update(hostInfo, guestInfo); err != nil {
		logger.Fatal(err)
	}

	serf, err := client.NewRPCClient(SerfRPCAddr)
	if err != nil {
		logger.Fatal(err)
	}
	defer serf.Close()

	tags := map[string]string{
		"uuid": hostInfo.UUID.String(),
	}
	if err := serf.UpdateTags(tags, []string{}); err != nil {
		logger.Fatal(err)
	}

	serfCh := make(chan map[string]interface{}, 64)
	stream, err := serf.Stream("*", serfCh)
	if err != nil {
		logger.Fatal(err)
	}
	defer serf.Stop(stream)

	if err := sendInfoEvent(s, serf, hostInfo.UUID.String(), 1); err != nil {
		logger.Fatal(err)
	}

	hvShutdownCh := make(chan struct{})
	go forwardGuestEvents(eventCh, serf, hostInfo, logger, hvShutdownCh)

	serfShutdownCh := make(chan struct{})
	go processSerfEvents(serfCh, serf, s, hostInfo, logger, serfShutdownCh)

	go func() {
		err := s.ListenAndServe()
		if err != nil && errors.Is(err, http.ErrServerClosed) {
			logger.Println(err)
		} else {
			logger.Fatal(err)
		}
	}()

	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(
			context.Background(),
			time.Second*5,
		)
		defer shutdownCancel()
		if err := s.Shutdown(shutdownCtx); err != nil {
			logger.Println(err)
			logger.Fatal(s.Close())
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
	s *inventory.Service,
	hostInfo *hypervisor.HostInfo,
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
		case comm.HostInfoEvent:
			hi, newHost, err := comm.UnpackHostInfoEvent(payload)
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
				if err := sendInfoEvent(s, serf, hostInfo.UUID.String(), 0); err != nil {
					logger.Fatal(err)
				}
			}
		case comm.GuestInfoEvent:
			gi, hostUUID, err := comm.UnpackGuestInfoEvent(payload)
			if err != nil {
				logger.Fatal(err)
			}
			logger.Printf(
				"%s: %d %s %s state(%d - %s) hostUUID(%s)",
				name, gi.Seq, gi.UUID, gi.Name,
				gi.State, hypervisor.GuestStateToString(gi.State),
				hostUUID,
			)
			if err := s.UpdateGuestState(hostUUID.String(), gi); err != nil {
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
	hostInfo *hypervisor.HostInfo,
	logger *log.Logger,
	shutdownCh chan<- struct{},
) {
	logger.Println("Forwarding guest events...")
	for gi := range eventCh {
		if err := sendGuestInfo(serf, &gi, hostInfo.UUID); err != nil {
			logger.Fatal(err)
		}
	}
	logger.Println("Forwarding done")
	close(shutdownCh)
}

func sendInfoEvent(s *inventory.Service, serf *client.RPCClient, hostKey string, newHost int) error {
	s.RLock()
	defer s.RUnlock()

	hostState := s.HostState(hostKey)

	if err := sendHostInfo(serf, &hostState.HostInfo, newHost); err != nil {
		return err
	}

	for _, gi := range hostState.Guests {
		if err := sendGuestInfo(serf, &gi, hostState.UUID); err != nil {
			return err
		}
	}

	return nil
}

func sendHostInfo(serf *client.RPCClient, hostInfo *hypervisor.HostInfo, newHost int) error {
	payload, err := comm.PackHostInfoEvent(hostInfo, newHost)
	if err != nil {
		return err
	}
	if err := serf.UserEvent(comm.HostInfoEvent, payload, false); err != nil {
		return err
	}
	return nil
}

func sendGuestInfo(serf *client.RPCClient, guestInfo *hypervisor.GuestInfo, hostUUID uuid.UUID) error {
	payload, err := comm.PackGuestInfoEvent(guestInfo, hostUUID)
	if err != nil {
		return err
	}
	if err := serf.UserEvent(comm.GuestInfoEvent, payload, false); err != nil {
		return err
	}
	return nil
}
