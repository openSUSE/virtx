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
	labelHostInfo string = "H"
	labelVmInfo string = "G"
	labelVmEvent string = "E"
	maxMessageSize uint = 1024
)

var serf = struct {
	c       *client.RPCClient
	encBuffer [maxMessageSize]byte
	encMux    sync.Mutex
	channel chan map[string]interface{}
	stream  client.StreamHandle
}{}

func sendHostInfo(hostInfo *openapi.Host) error {
	serf.encMux.Lock()
	defer serf.encMux.Unlock()
	var (
		eventsize int
		err error
	)
	eventsize, err = sbinary.Encode(serf.encBuffer[:], binary.LittleEndian, hostInfo)
	if (err != nil) {
		return err
	}
	logger.Log("sendHostInfo payload len=%d\n", eventsize)
	return serf.c.UserEvent(labelHostInfo, serf.encBuffer[:eventsize], false)
}

func sendVmInfo(VmInfo *openapi.Vm) error {
	serf.encMux.Lock()
	defer serf.encMux.Unlock()
	var (
		eventsize int
		err error
	)
	eventsize, err = sbinary.Encode(serf.encBuffer[:], binary.LittleEndian, VmInfo)
	if (err != nil) {
		return err
	}
	logger.Log("sendVmInfo payload len=%d\n", eventsize)
	return serf.c.UserEvent(labelVmInfo, serf.encBuffer[:eventsize], false)
}

func sendVmEvent(e *hypervisor.VmEvent) error {
	serf.encMux.Lock()
	defer serf.encMux.Unlock()
	var (
		eventsize int
		err error
	)
	eventsize, err = sbinary.Encode(serf.encBuffer[:], binary.LittleEndian, e)
	if (err != nil) {
		return err
	}
	logger.Log("sendVmEvent payload len=%d\n", eventsize)
	return serf.c.UserEvent(labelVmEvent, serf.encBuffer[:eventsize], false)
}

func RecvSerfEvents(
	s *virtx.Service,
	shutdownCh chan<- struct{},
) {
	var err error
	logger.Log("RecvSerfEvents loop start...")
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

		/* user event */
		name := e["Name"].(string)
		payload := e["Payload"].([]byte)

		switch (name) {
		case labelHostInfo:
			var (
				hi openapi.Host
				size int
			)
			size, err = sbinary.Decode(payload, binary.LittleEndian, &hi)
			if (err != nil) {
				logger.Log("Decode %s: ERR '%s' at offset %d", name, err.Error(), size)
			} else {
				logger.Log("Decode %s: OK  %d %s %s", name, hi.Ts, hi.Uuid, hi.Def.Name)
				err = s.UpdateHost(&hi)
				if (err != nil) {
					logger.Log(err.Error())
				}
			}
		case labelVmEvent:
			var (
				ve hypervisor.VmEvent
				size int
			)
			size, err = sbinary.Decode(payload, binary.LittleEndian, &ve)
			if (err != nil) {
				logger.Log("Decode %s: ERR '%s' at offset %d", name, err.Error(), size)
			} else {
				logger.Log("Decode %s: OK  %d %s %s %s", name, ve.Ts, ve.Uuid, ve.Name, ve.State)
				err = s.UpdateVmState(&ve)
				if (err != nil) {
					logger.Log(err.Error())
				}
			}
		case labelVmInfo:
			var (
				vm openapi.Vm
				size int
			)
			size, err = sbinary.Decode(payload, binary.LittleEndian, &vm)
			if (err != nil) {
				logger.Log("Decode %s: ERR '%s' at offset %d", name, err.Error(), size)
			} else {
				logger.Log("Decode %s: OK  %d %s %s %s", name, vm.Ts, vm.Uuid, vm.Vmdef.Name, vm.Runstate.State)
				err = s.UpdateVm(&vm)
				if (err != nil) {
					logger.Log(err.Error())
				}
			}
		default:
			logger.Log("[UNKNOWN-EVENT] %s %s", name, payload)
		}
	}
	logger.Log("RecvSerfEvents loop exit!")
	close(shutdownCh)
}

func SendSystemInfo(ch <-chan hypervisor.SystemInfo, service *virtx.Service, shutdownCh chan<- struct{}) {
	var (
		err error
		si hypervisor.SystemInfo
	)
	logger.Log("SendSystemInfo loop start...")
	for si = range ch {
		err = service.UpdateHost(&si.Host)
		if (err != nil) {
			logger.Log(err.Error())
		}
		for i, _ := range si.Vms {
			err = service.UpdateVm(&si.Vms[i])
			if (err != nil) {
				logger.Log(err.Error())
			}
		}
		err = UpdateTags(&si.Host)
		if (err != nil) {
			logger.Log(err.Error())
		}
		err = sendHostInfo(&si.Host)
		if (err != nil) {
			logger.Log(err.Error())
		}
		for _, vm := range si.Vms {
			err = sendVmInfo(&vm)
			if (err != nil) {
				logger.Log(err.Error())
			}
		}
	}
	logger.Log("SendSystemInfo loop exit!")
	close(shutdownCh)
}

func SendVmEvents(
	eventCh <-chan hypervisor.VmEvent,
	shutdownCh chan<- struct{},
) {
	logger.Log("SendVmEvents loop start...")
	for e := range eventCh {
		if err := sendVmEvent(&e); err != nil {
			logger.Log(err.Error())
		}
	}
	logger.Log("SendVmEvents loop exit!")
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

func StartListening(
	vmEventCh chan hypervisor.VmEvent, vmEventShutdownCh chan struct{},
	systemInfoCh chan hypervisor.SystemInfo, systemInfoShutdownCh chan struct{},
	serfShutdownCh chan struct{},
	service *virtx.Service) {
	/* create subroutines to send and process events */
	go SendVmEvents(vmEventCh, vmEventShutdownCh)
	go SendSystemInfo(systemInfoCh, service, systemInfoShutdownCh)
	go RecvSerfEvents(service, serfShutdownCh)
}

func Shutdown() {
	var err error
	logger.Log("serfcomm is shutting down...")
	err = serf.c.Stop(serf.stream)
	if (err != nil) {
        logger.Log(err.Error())
    }
	err = serf.c.Close()
	if (err != nil) {
		logger.Log(err.Error())
	}
	logger.Log("serfcomm shutdown complete.")
}
