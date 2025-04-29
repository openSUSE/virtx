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
package serfcomm

import (
	"fmt"
	"sync"
	"encoding/binary"
	"github.com/hashicorp/serf/client"

	"suse.com/virtXD/pkg/hypervisor"
	"suse.com/virtXD/pkg/virtx"
	"suse.com/virtXD/pkg/model"
	"suse.com/virtXD/pkg/logger"
	"suse.com/virtXD/pkg/encoding/sbinary"
)

const (
	labelHostInfo string = "host-info"
	labelGuestInfo string = "guest-info"
	maxMessageSize uint = 1024
)

var serf = struct {
	c       *client.RPCClient
	encBuffer [maxMessageSize]byte
	encMux    sync.Mutex
	channel chan map[string]interface{}
	stream  client.StreamHandle
}{}

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

func sendHostInfo(hostInfo *openapi.Host) error {
	serf.encMux.Lock()
	defer serf.encMux.Unlock()
	eventsize, err := sbinary.Encode(serf.encBuffer[:], binary.LittleEndian, hostInfo)

	if err != nil {
		return err
	}
	logger.Log("sendHostInfo payload len=%d\n", eventsize)
	if err := serf.c.UserEvent(labelHostInfo, serf.encBuffer[:eventsize], false); err != nil {
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
	if err = sendHostInfo(&host); err != nil {
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
	shutdownCh chan<- struct{},
) {
	var err error
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
				err = s.SetHostState(uuid, newstate)
				if (err != nil) {
					logger.Log(err.Error())
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
			var (
				hi openapi.Host
				size int
			)
			size, err = sbinary.Decode(payload, binary.LittleEndian, &hi)
			if (err != nil) {
				logger.Log("Decode: '%s' at offset %d", err.Error(), size)
			} else {
				logger.Log("Decode: %s: %d %s %s", name, hi.Seq, hi.Uuid, hi.Def.Name)
				err = s.UpdateHost(&hi)
				if (err != nil) {
					logger.Log(err.Error())
				}
			}
		case labelGuestInfo:
			var (
				gi hypervisor.GuestInfo
				hostUUID string
			)
			gi, hostUUID, err = unpackGuestInfoEvent(payload)
			if (err != nil) {
				logger.Log(err.Error())
			}
			logger.Log("%s: %d %s %s state(%d - %s) hostUUID(%s)",
				name, gi.Seq, gi.UUID, gi.Name,
				gi.State, hypervisor.GuestStateToString(gi.State), hostUUID,
			)
			err = s.UpdateGuest(gi)
			if (err != nil) {
				logger.Log(err.Error())
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
	uuid string,
	shutdownCh chan<- struct{},
) {
	logger.Log("Forwarding guest events...")
	for gi := range eventCh {
		if err := sendGuestInfo(gi, uuid); err != nil {
			logger.Log(err.Error())
		}
	}
	logger.Log("Forwarding done")
	close(shutdownCh)
}

func UpdateTags(host *openapi.Host) error {
	var err error
	/* XXX TODO: compress the Host information into gob and set as value XXX */
	addTags := map[string]string { host.Uuid: "" }
	removeTags := []string {}
	err = serf.c.UpdateTags(addTags, removeTags)
	return err
}

func Init(rpcAddr string) error {
	var err error
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
