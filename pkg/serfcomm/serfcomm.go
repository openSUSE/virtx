package serfcomm

import (
	"fmt"
	"log"

	"suse.com/virtXD/pkg/hypervisor"
	"github.com/hashicorp/serf/client"
	"suse.com/virtXD/pkg/virtx"
	"suse.com/virtXD/pkg/model"
)

const (
	labelHostInfo string = "host-info"
	labelGuestInfo string = "guest-info"
)

func packHostInfoEvent(hostInfo openapi.Host) ([]byte, error) {
	return hostInfo.MarshalJSON()
}

func unpackHostInfoEvent(payload []byte) (openapi.Host, error) {
	var (
		host openapi.Host
		err error
	)
	err = host.UnmarshalJSON(payload)
	return host, err;
}

func packGuestInfoEvent(guestInfo hypervisor.GuestInfo, hostUUID string) ([]byte, error) {
	var str string = fmt.Sprintf(
		"%s %d %s %s %d %d %d",
		hostUUID, guestInfo.Seq, guestInfo.UUID, guestInfo.Name,
		guestInfo.State, guestInfo.Memory, guestInfo.NrVirtCpu,
	)
	return []byte(str), nil
}

func unpackGuestInfoEvent(payload []byte) (hypervisor.GuestInfo, string, error) {
	var (
		seq         uint64
		hostUUID    string
		guestUUID   string
		name        string
		state       int
		memory      uint64
		nrVirtCPU   uint
		guestInfo   hypervisor.GuestInfo
		n           int
		err         error
	)
	n, err = fmt.Sscanf(
		string(payload), "%s %d %s %s %d %d %d",
		&hostUUID, &seq, &guestUUID, &name,
		&state, &memory, &nrVirtCPU,
	)
	if (err != nil || n != 7) {
		return guestInfo, "", err
	}
	guestInfo = hypervisor.GuestInfo {
		Seq:       seq,
		Name:      name,
		UUID:      guestUUID,
		State:     state,
		Memory:    memory,
		NrVirtCpu: nrVirtCPU,
	}
	return guestInfo, hostUUID, nil
}

func sendHostInfo(serf *client.RPCClient, hostInfo openapi.Host) error {
	payload, err := packHostInfoEvent(hostInfo)
	if err != nil {
		return err
	}
	var eventsize int = len(payload)
	fmt.Printf("sendHostInfo payload len=%d\n", eventsize)
	fmt.Printf("%s\n", string(payload))
	if err := serf.UserEvent(labelHostInfo, payload, false); err != nil {
		return err
	}
	return nil
}

func sendGuestInfo(serf *client.RPCClient, guestInfo hypervisor.GuestInfo, hostUUID string) error {
	payload, err := packGuestInfoEvent(guestInfo, hostUUID)
	if err != nil {
		return err
	}
	if err := serf.UserEvent(labelGuestInfo, payload, false); err != nil {
		return err
	}
	return nil
}

func SendInfoEvent(s *virtx.Service, serf *client.RPCClient, uuid string) error {
	s.RLock()
	defer s.RUnlock()

	host, err := s.GetHost(uuid)
	if (err != nil) {
		return err;
	}
	if err = sendHostInfo(serf, host); err != nil {
		return err
	}

	/* XXX probably here we want to send only the guests running on host? XXX */

	//for _, gi := range s.guests {
	//	if err = sendGuestInfo(serf, gi, host); err != nil {
	//		return err
	//	}

	return nil
}

func RecvSerfEvents(
	serfCh <-chan map[string]interface{},
	serf *client.RPCClient,
	s *virtx.Service,
	hostInfo openapi.Host,
	logger *log.Logger,
	shutdownCh chan<- struct{},
) {
	logger.Println("Processing events...")
	for e := range serfCh {
		var newstate string = string(openapi.FAILED)
		switch e["Event"].(string) {
		case "user":
		case "member-leave":
			newstate = string(openapi.LEFT)
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
				if err := s.SetHostState(uuid, newstate); err != nil {
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
		case labelHostInfo:
			hi, err := unpackHostInfoEvent(payload)
			if err != nil {
				logger.Fatal(err)
			}
			logger.Printf(
				"%s: %d %s %s",
				name, hi.Seq, hi.Uuid, hi.Hostdef.Name,
			)
			if err := s.UpdateHost(hi); err != nil {
				logger.Fatal(err)
			}
			/*
			 *  newHost does not exist anymore, do we really need it?
			 */
			/*
			if newHost == 1 && hi.Uuid != hostInfo.Uuid {
				if err := SendInfoEvent(s, serf, hostInfo.UUID, 0); err != nil {
					logger.Fatal(err)
				}
			}
			*/
		case labelGuestInfo:
			gi, hostUUID, err := unpackGuestInfoEvent(payload)
			if err != nil {
				logger.Fatal(err)
			}
			logger.Printf(
				"%s: %d %s %s state(%d - %s) hostUUID(%s)",
				name, gi.Seq, gi.UUID, gi.Name,
				gi.State, hypervisor.GuestStateToString(gi.State),
				hostUUID,
			)
			if err := s.UpdateGuest(gi); err != nil {
				logger.Fatal(err)
			}
		default:
			logger.Println("[UNKNOWN-EVENT]", name, payload)
		}
	}
	logger.Println("Processing done")
	close(shutdownCh)
}

func SendHypervisorEvents(
	eventCh <-chan hypervisor.GuestInfo,
	serf *client.RPCClient,
	hostInfo openapi.Host,
	logger *log.Logger,
	shutdownCh chan<- struct{},
) {
	logger.Println("Forwarding guest events...")
	for gi := range eventCh {
		if err := sendGuestInfo(serf, gi, hostInfo.Uuid); err != nil {
			logger.Fatal(err)
		}
	}
	logger.Println("Forwarding done")
	close(shutdownCh)
}
