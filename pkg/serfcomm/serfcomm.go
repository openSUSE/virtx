package serfcomm

import (
	"fmt"
	"sync"
	"bytes"
	"encoding/gob"
	"github.com/hashicorp/serf/client"

	"suse.com/virtXD/pkg/hypervisor"
	"suse.com/virtXD/pkg/virtx"
	"suse.com/virtXD/pkg/model"
	"suse.com/virtXD/pkg/logger"
)

const (
	labelHostInfo string = "host-info"
	labelGuestInfo string = "guest-info"
	maxMessageSize uint = 1024
)

var serf = struct {
	c       *client.RPCClient
	encoder *gob.Encoder
	decoder *gob.Decoder
	encBuffer *bytes.Buffer
	decBuffer *bytes.Buffer
	encMux    *sync.Mutex
	decMux    *sync.Mutex
	channel chan map[string]interface{}
	stream  client.StreamHandle
}{}

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

func sendHostInfo(hostInfo openapi.Host) error {
	payload, err := packHostInfoEvent(hostInfo)
	if err != nil {
		return err
	}
	var eventsize int = len(payload)
	logger.Log("sendHostInfo payload len=%d\n", eventsize)
	if err := serf.c.UserEvent(labelHostInfo, payload, false); err != nil {
		return err
	}
	return nil
}

func sendGuestInfo(guestInfo hypervisor.GuestInfo, hostUUID string) error {
	payload, err := packGuestInfoEvent(guestInfo, hostUUID)
	if err != nil {
		return err
	}
	if err := serf.c.UserEvent(labelGuestInfo, payload, false); err != nil {
		return err
	}
	return nil
}

func SendInfoEvent(s *virtx.Service, uuid string) error {
	host, err := s.GetHost(uuid)
	if (err != nil) {
		return err;
	}
	if err = sendHostInfo(host); err != nil {
		return err
	}

	/* XXX probably here we want to send only the guests running on host? XXX */

	//for _, gi := range s.guests {
	//	if err = sendGuestInfo(gi, host); err != nil {
	//		return err
	//	}

	return nil
}

func RecvSerfEvents(
	s *virtx.Service,
	hostInfo openapi.Host,
	shutdownCh chan<- struct{},
) {
	logger.Log("Processing events...")
	for e := range serf.channel {
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
					logger.Log("failed to get uuid tag")
					continue
				}
				logger.Log("Host %s OFFLINE", uuid)
				if err := s.SetHostState(uuid, newstate); err != nil {
					logger.Fatal(err.Error())
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
				logger.Fatal(err.Error())
			}
			logger.Log("%s: %d %s %s", name, hi.Seq, hi.Uuid, hi.Hostdef.Name)
			if err := s.UpdateHost(hi); err != nil {
				logger.Fatal(err.Error())
			}
		case labelGuestInfo:
			gi, hostUUID, err := unpackGuestInfoEvent(payload)
			if err != nil {
				logger.Fatal(err.Error())
			}
			logger.Log("%s: %d %s %s state(%d - %s) hostUUID(%s)",
				name, gi.Seq, gi.UUID, gi.Name,
				gi.State, hypervisor.GuestStateToString(gi.State), hostUUID,
			)
			if err := s.UpdateGuest(gi); err != nil {
				logger.Fatal(err.Error())
			}
		default:
			logger.Log("[UNKNOWN-EVENT] %s %s", name, payload)
		}
	}
	logger.Log("Processing done")
	close(shutdownCh)
}

func SendHypervisorEvents(
	eventCh <-chan hypervisor.GuestInfo,
	hostInfo openapi.Host,
	shutdownCh chan<- struct{},
) {
	logger.Log("Forwarding guest events...")
	for gi := range eventCh {
		if err := sendGuestInfo(gi, hostInfo.Uuid); err != nil {
			logger.Fatal(err.Error())
		}
	}
	logger.Log("Forwarding done")
	close(shutdownCh)
}

func UpdateTags(host openapi.Host) error {
	var err error
	/* XXX TODO: compress the Host information into gob and set as value XXX */
	addTags := map[string]string { host.Uuid: "" }
	removeTags := []string {}
	err = serf.c.UpdateTags(addTags, removeTags)
	return err
}

func Init(rpcAddr string) error {
	var err error
	serf.encBuffer = bytes.NewBuffer(make([]byte, 0, maxMessageSize))
	serf.decBuffer = bytes.NewBuffer(make([]byte, 0, maxMessageSize))
	serf.encoder = gob.NewEncoder(serf.encBuffer)
	serf.decoder = gob.NewDecoder(serf.decBuffer)

	serf.c, err = client.NewRPCClient(rpcAddr)
	if (err != nil) {
		return err
	}
	serf.channel = make(chan map[string]interface{}, 64)
	serf.stream, err = serf.c.Stream("*", serf.channel)
	if (err != nil) {
		return err
	}
	return nil
}

func Shutdown() {
	var err error
	logger.Log("Shutting down...")
	err = serf.c.Stop(serf.stream)
	if (err != nil) {
        logger.Log(err.Error())
    }
	err = serf.c.Close()
	if (err != nil) {
		logger.Log(err.Error())
	}
	logger.Log("Shutdown complete.")
}
