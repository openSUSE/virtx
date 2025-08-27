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

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/encoding/sbinary"
)

const (
	label_host_info string = "HI"
	label_vm_stat string = "VI"
	label_vm_event string = "VE"
	max_message_size uint = 1024
)

var serf = struct {
	c *client.RPCClient
	enc_buffer [max_message_size]byte
	enc_mux sync.Mutex
	channel chan map[string]interface{}
	stream client.StreamHandle
}{}

func send_host_info(host_info *openapi.Host) error {
	serf.enc_mux.Lock()
	defer serf.enc_mux.Unlock()
	var (
		eventsize int
		err error
	)
	eventsize, err = sbinary.Encode(serf.enc_buffer[:], binary.LittleEndian, host_info)
	if (err != nil) {
		return err
	}
	logger.Log("send_host_info payload len=%d\n", eventsize)
	return serf.c.UserEvent(label_host_info, serf.enc_buffer[:eventsize], false)
}

func send_vm_stat(vmdata *hypervisor.Vmdata) error {
	serf.enc_mux.Lock()
	defer serf.enc_mux.Unlock()
	var (
		eventsize int
		err error
	)
	eventsize, err = sbinary.Encode(serf.enc_buffer[:], binary.LittleEndian, vmdata)
	if (err != nil) {
		return err
	}
	logger.Log("send_vm_stat payload len=%d\n", eventsize)
	return serf.c.UserEvent(label_vm_stat, serf.enc_buffer[:eventsize], false)
}

func send_vm_event(e *hypervisor.VmEvent) error {
	serf.enc_mux.Lock()
	defer serf.enc_mux.Unlock()
	var (
		eventsize int
		err error
	)
	eventsize, err = sbinary.Encode(serf.enc_buffer[:], binary.LittleEndian, e)
	if (err != nil) {
		return err
	}
	logger.Log("send_vm_event payload len=%d\n", eventsize)
	return serf.c.UserEvent(label_vm_event, serf.enc_buffer[:eventsize], false)
}

func recv_serf_events(shutdown_ch chan<- struct{}) {
	var err error
	logger.Log("RecvSerfEvents loop start...")
	for e := range serf.channel {
		var newstate openapi.Hoststate = openapi.HOST_FAILED
		switch e["Event"].(string) {
		case "user":
		case "member-leave":
			newstate = openapi.HOST_LEFT
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
				err = inventory.Set_host_state(uuid, newstate)
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
		case label_host_info:
			var (
				hi openapi.Host
				size int
			)
			size, err = sbinary.Decode(payload, binary.LittleEndian, &hi)
			if (err != nil) {
				logger.Log("Decode %s: ERR '%s' at offset %d", name, err.Error(), size)
			} else {
				logger.Log("Decode %s: OK  %d %s %s", name, hi.Ts, hi.Uuid, hi.Def.Name)
				inventory.Update_host(&hi)
			}
		case label_vm_event:
			var (
				ve hypervisor.VmEvent
				size int
			)
			size, err = sbinary.Decode(payload, binary.LittleEndian, &ve)
			if (err != nil) {
				logger.Log("Decode %s: ERR '%s' at offset %d", name, err.Error(), size)
			} else {
				logger.Log("Decode %s: OK  %d %s %s", name, ve.Ts, ve.Uuid, ve.State)
				err = inventory.Update_vm_state(&ve)
				if (err != nil) {
					logger.Log(err.Error())
				}
			}
		case label_vm_stat:
			var (
				vm hypervisor.Vmdata
				size int
			)
			size, err = sbinary.Decode(payload, binary.LittleEndian, &vm)
			if (err != nil) {
				logger.Log("Decode %s: ERR '%s' at offset %d", name, err.Error(), size)
			} else {
				logger.Log("Decode %s: OK  %d %s %s %d", name, vm.Ts, vm.Uuid, vm.Name, vm.Runinfo.Runstate)
				err = inventory.Update_vm(&vm)
				if (err != nil) {
					logger.Log(err.Error())
				}
			}
		default:
			logger.Log("[UNKNOWN-EVENT] %s %s", name, payload)
		}
	}
	logger.Log("RecvSerfEvents loop exit!")
	close(shutdown_ch)
}

func send_system_info(ch <-chan hypervisor.SystemInfo) {
	var (
		err error
		si hypervisor.SystemInfo
	)
	logger.Log("SendSystemInfo loop start...")
	for si = range ch {
		err = update_tags(&si.Host)
		if (err != nil) {
			logger.Log(err.Error())
		}
		err = send_host_info(&si.Host)
		if (err != nil) {
			logger.Log(err.Error())
		}
		for _, vmdata := range si.Vmdata {
			err = send_vm_stat(&vmdata)
			if (err != nil) {
				logger.Log(err.Error())
			}
		}
	}
	logger.Fatal("SendSystemInfo loop exit! (Should never happen!)")
}

func send_vm_events(eventCh <-chan hypervisor.VmEvent) {
	logger.Log("SendVmEvents loop start...")
	for e := range eventCh {
		if err := send_vm_event(&e); err != nil {
			logger.Log(err.Error())
		}
	}
	logger.Fatal("SendVmEvents loop exit! (Should never happen!)")
}

func update_tags(host *openapi.Host) error {
	var err error
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

func Start_listening(
	vm_event_ch chan hypervisor.VmEvent, system_info_ch chan hypervisor.SystemInfo, serf_shutdown_ch chan struct{}) {
	/* create subroutines to send and process events */
	go send_vm_events(vm_event_ch)
	go send_system_info(system_info_ch)
	go recv_serf_events(serf_shutdown_ch)
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
