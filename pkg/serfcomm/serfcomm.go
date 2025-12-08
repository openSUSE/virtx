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
	"errors"
	"sync"
	"encoding/binary"
	"time"
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
	reconnect_seconds = 5
	rpc_addr = "127.0.0.1:7373"
)

var serf = struct {
	m sync.RWMutex
	c *client.RPCClient
	enc_buffer [max_message_size]byte
	channel chan map[string]any
	stream client.StreamHandle
}{}

/* locking version of the serf.c == nil check */
func is_connected() bool {
	serf.m.Lock()
	defer serf.m.Unlock()
	return serf.c != nil
}

func send_user_event(label string, payload []byte) error {
	if (serf.c == nil) {
		return errors.New("RPC client closed")
	}
	return serf.c.UserEvent(label, payload, false)
}

func update_tags(host *openapi.Host) error {
	serf.m.Lock()
	defer serf.m.Unlock()

	if (serf.c == nil) {
		return errors.New("RPC client closed")
	}
	addTags := map[string]string { host.Uuid: "" }
	removeTags := []string {}
	return serf.c.UpdateTags(addTags, removeTags)
}

func send_host_info(host_info *openapi.Host) error {
	serf.m.Lock()
	defer serf.m.Unlock()
	var (
		eventsize int
		err error
	)
	eventsize, err = sbinary.Encode(serf.enc_buffer[:], binary.LittleEndian, host_info)
	if (err != nil) {
		return err
	}
	logger.Log("send_host_info payload len=%d\n", eventsize)
	return send_user_event(label_host_info, serf.enc_buffer[:eventsize])
}

func send_vm_stat(vmdata *inventory.Vmdata) error {
	serf.m.Lock()
	defer serf.m.Unlock()
	var (
		eventsize int
		err error
	)
	eventsize, err = sbinary.Encode(serf.enc_buffer[:], binary.LittleEndian, vmdata)
	if (err != nil) {
		return err
	}
	logger.Log("send_vm_stat payload len=%d\n", eventsize)
	return send_user_event(label_vm_stat, serf.enc_buffer[:eventsize])
}

func send_vm_event(e *inventory.VmEvent) error {
	serf.m.Lock()
	defer serf.m.Unlock()
	var (
		eventsize int
		err error
	)
	eventsize, err = sbinary.Encode(serf.enc_buffer[:], binary.LittleEndian, e)
	if (err != nil) {
		return err
	}
	logger.Log("send_vm_event payload len=%d\n", eventsize)
	return send_user_event(label_vm_event, serf.enc_buffer[:eventsize])
}

func recv_serf_events() {
	for {
		logger.Log("RecvSerfEvents loop start...")

		for e := range serf.channel {
			var name string = e["Event"].(string)
			switch (name) {
			case "user":
				handle_user_event(e)
			case "member-leave":
				handle_member_change(e, openapi.HOST_LEFT)
			case "member-reap":
				fallthrough
			case "member-failed":
				handle_member_change(e, openapi.HOST_FAILED)
			case "member-join":
				handle_member_change(e, openapi.HOST_ACTIVE)
			}
		}

		serf.m.Lock()
		serf.c.Stop(serf.stream)
		serf.c.Close()
		serf.c = nil
		serf.m.Unlock()

		logger.Log("RecvSerfEvents loop exit")
		logger.Log("reconnect to serf, attempt every %d seconds...", reconnect_seconds)
		var err error = errors.New("")
		for ; err != nil; err = Connect() {
			time.Sleep(time.Duration(reconnect_seconds) * time.Second)
		}
	}
}

func handle_member_change(e map[string]any, newstate openapi.Hoststate) {
	var (
		err error
		name string = e["Event"].(string)
	)
	for _, m := range e["Members"].([]any) {
		tags := m.(map[any]any)["Tags"].(map[any]any)
		for tag := range tags {
			var uuid string = tag.(string)
			logger.Log("%s %s", name, uuid)
			err = inventory.Set_host_state(uuid, newstate)
			if (err != nil) {
				logger.Log(err.Error())
			}
		}
	}
}

func handle_user_event(e map[string]any) {
	var (
		name string = e["Name"].(string)
		payload []byte = e["Payload"].([]byte)
		err error
	)
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
			ve inventory.VmEvent
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
			vm inventory.Vmdata
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

func send_system_info(ch <-chan hypervisor.SystemInfo) {
	var (
		err error
		si hypervisor.SystemInfo
	)
	logger.Log("SendSystemInfo loop start...")
	for si = range ch {
		if (!is_connected()) {
			/* do nothing with the systeminfo if we are not connected */
			continue
		}
		err = update_tags(&si.Host)
		if (err != nil) {
			logger.Log("update_tags: " + err.Error())
		}
		err = send_host_info(&si.Host)
		if (err != nil) {
			logger.Log("send_host_info: " + err.Error())
		}
		for _, vmdata := range si.Vms {
			err = send_vm_stat(&vmdata)
			if (err != nil) {
				logger.Log("send_vm_stat: " + err.Error())
			}
		}
	}
	logger.Log("SendSystemInfo loop exit")
}

func send_vm_events(eventCh <-chan inventory.VmEvent) {
	logger.Log("SendVmEvents loop start...")
	for e := range eventCh {
		if (!is_connected()) {
			/* do nothing with the vm events if we are not connected */
			continue
		}
		if err := send_vm_event(&e); err != nil {
			logger.Log(err.Error())
		}
	}
	logger.Log("SendVmEvents loop exit")
}

func Connect() error {
	serf.m.Lock()
	defer serf.m.Unlock()

	var err error
	serf.c, err = client.NewRPCClient(rpc_addr)
	if (err != nil) {
		serf.c = nil
		return err
	}
	serf.channel = make(chan map[string]any, 64)
	serf.stream, err = serf.c.Stream("*", serf.channel)
	if (err != nil) {
		serf.c.Close()
		serf.c = nil
		return err
	}
	return nil
}

func Start_listening(
	vm_event_ch chan inventory.VmEvent, system_info_ch chan hypervisor.SystemInfo) {
	/* create subroutines to send and process events */
	go send_vm_events(vm_event_ch)
	go send_system_info(system_info_ch)
	go recv_serf_events()
}

func Shutdown() {
	serf.m.Lock()
	defer serf.m.Unlock()

	var err error
	logger.Log("serfcomm is shutting down...")
	if (serf.c != nil) {
		err = serf.c.Stop(serf.stream)
		if (err != nil) {
			logger.Log(err.Error())
		}
		err = serf.c.Close()
		if (err != nil) {
			logger.Log(err.Error())
		}
	}
	logger.Log("serfcomm shutdown complete.")
}
