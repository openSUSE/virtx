/*
 * Copyright (c) 2024-2026 SUSE LLC
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
	"fmt"
	"net"
	"sync"
	"encoding/binary"
	"time"
	"github.com/hashicorp/serf/client"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/encoding/sbinary"
	"suse.com/virtx/pkg/encoding/serftags"
	"suse.com/virtx/pkg/reg"
)

const (
	LABEL_VM_INFO string = "VI"
	LABEL_VM_EVENT string = "VE"
	MAX_MESSAGE_SIZE uint = 1024
	RECONNECT_SECONDS = 5
	RPC_ADDR = "127.0.0.1:7373"
	VMINFO_QUEUE_LEN = 256
)

/* hosts whose reg vminfo needs to be applied, consumed by load_vminfo */
var vminfo_ch chan string = make(chan string, VMINFO_QUEUE_LEN)

var serf = struct {
	m sync.RWMutex
	c *client.RPCClient
	enc_buffer [MAX_MESSAGE_SIZE]byte
	channel chan map[string]any
	stream client.StreamHandle
}{}

/* locking version of the serf.c == nil check */
func is_connected() bool {
	serf.m.RLock()
	defer serf.m.RUnlock()
	return serf.c != nil
}

func send_user_event(label string, payload []byte) error {
	if (serf.c == nil) {
		return errors.New("RPC client closed")
	}
	return serf.c.UserEvent(label, payload, false)
}

func send_VI(vminfo *inventory.VmInfo) error {
	serf.m.Lock()
	defer serf.m.Unlock()
	var (
		eventsize int
		err error
	)
	eventsize, err = sbinary.Encode(serf.enc_buffer[:], binary.LittleEndian, vminfo)
	if (err != nil) {
		return err
	}
	logger.Debug("send_VI payload len=%d\n", eventsize)
	return send_user_event(LABEL_VM_INFO, serf.enc_buffer[:eventsize])
}

func send_VE(e *inventory.VmEvent) error {
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
	logger.Debug("send_VE payload len=%d\n", eventsize)
	return send_user_event(LABEL_VM_EVENT, serf.enc_buffer[:eventsize])
}

/* publish the HostInfo as serf tags */
func update_host_tags(host_info *inventory.HostInfo) error {
	serf.m.Lock()
	defer serf.m.Unlock()
	var (
		tags map[string]string
		err error
	)
	if (serf.c == nil) {
		return errors.New("RPC client closed")
	}
	tags, err = serftags.Encode(host_info)
	if (err != nil) {
		return err
	}
	return serf.c.UpdateTags(tags, []string{})
}

func recv_serf_events() {
	for {
		logger.Debug("RecvSerfEvents loop start...")

		for e := range serf.channel {
			var name string = e["Event"].(string)
			switch (name) {
			case "user":
				handle_user_event(e)
			case "member-leave":
				handle_member_change(e, openapi.CSTATE_LEFT)
			case "member-reap", "member-failed":
				handle_member_change(e, openapi.CSTATE_FAILED)
			case "member-join", "member-update":
				handle_member_change(e, openapi.CSTATE_ACTIVE)
			}
		}

		logger.Debug("RecvSerfEvents loop exit")
		serf.m.Lock()
		stop_listening()
		serf.m.Unlock()

		logger.Log("reconnect to serf, attempt every %d seconds...", RECONNECT_SECONDS)
		var err error = errors.New("")
		for ; err != nil; err = Connect() {
			time.Sleep(time.Duration(RECONNECT_SECONDS) * time.Second)
		}
		logger.Log("reconnected.")
	}
}

func handle_member_change(e map[string]any, newstate openapi.Cstate) {
	var (
		err error
		hi inventory.HostInfo
		tags map[string]string
		name string = e["Event"].(string)
		addr []byte
		ok bool
	)
	for _, m := range e["Members"].([]any) {
		tags = make(map[string]string)
		for k, v := range m.(map[any]any)["Tags"].(map[any]any) {
			tags[k.(string)] = v.(string)
		}
		hi = inventory.HostInfo{}
		err = serftags.Decode(tags, &hi)
		if (err != nil) {
			logger.Log("handle_member_change: %s: %s", name, err.Error())
			continue
		}
		if (hi.Uuid == "") {
			logger.Log("handle_member_change: %s: Uuid tag missing", name)
			continue
		}
		logger.Debug("%s %s", name, hi.Uuid)
		if (newstate == openapi.CSTATE_ACTIVE) {
			/* join or update: the tags carry the current HostInfo */
			hi.Cstate = newstate
			addr, ok = m.(map[any]any)["Addr"].([]byte)
			if (!ok) {
				logger.Log("handle_member_change: %s: %s: Addr missing", name, hi.Uuid)
				continue
			}
			handle_hostinfo(&hi, net.IP(addr).String())
			continue
		}
		err = inventory.Set_host_state(hi.Uuid, newstate)
		if (err != nil) {
			logger.Log("%s", err.Error())
		}
	}
}

/* update the inventory with the HostInfo of a host, and queue its vminfo if needed */
func handle_hostinfo(hi *inventory.HostInfo, man_ip string) {
	if (inventory.Update_host(hi, man_ip)) {
		/*
		 * queue the host for load_vminfo without blocking the serf events loop:
		 * if vminfo_ch is full, select takes the default case and the vminfo of
		 * this host is not read now. It will be at its next update, as the
		 * VI_ts still differs from the applied one.
		 */
		select {
		case vminfo_ch <- hi.Uuid:
		default:
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
	case LABEL_VM_EVENT:
		var (
			ve inventory.VmEvent
			size int
		)
		size, err = sbinary.Decode(payload, binary.LittleEndian, &ve)
		if (err != nil) {
			logger.Log("Decode %s: ERR '%s' at offset %d", name, err.Error(), size)
		} else {
			logger.Debug("Decode %s: OK  %d %s %s", name, ve.Ts, ve.Uuid, ve.Runstate)
			err = inventory.Update_vm_state(&ve)
			if (err != nil) {
				logger.Log("%s", err.Error())
			}
		}
	case LABEL_VM_INFO:
		var (
			vm inventory.VmInfo
			size int
		)
		size, err = sbinary.Decode(payload, binary.LittleEndian, &vm)
		if (err != nil) {
			logger.Log("Decode %s: ERR '%s' at offset %d", name, err.Error(), size)
		} else {
			logger.Debug("Decode %s: OK  %d %s %s %d", name, vm.Ts, vm.Uuid, vm.Name, vm.Runstate)
			err = inventory.Update_vm(&vm)
			if (err != nil) {
				logger.Log("%s", err.Error())
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
	logger.Debug("SendSystemInfo loop start...")
	for si = range ch {
		if (!is_connected()) {
			/* do nothing with the systeminfo if we are not connected */
			continue
		}
		if (si.Host.Uuid != "") {
			/* we have a full System Info with Host Information and all VMs */
			err = update_host_tags(&si.Host.HostInfo)
			if (err != nil) {
				logger.Log("update_host_tags: %s", err.Error())
			}
		} else {
			/* this is a one-shot VI event, currently only for DEFINED */
			for _, vm := range si.Vms {
				err = send_VI(&vm.VmInfo)
				if (err != nil) {
					logger.Log("send_VI: %s", err.Error())
				}
			}
		}
	}
	logger.Debug("SendSystemInfo loop exit")
}

func send_vm_events(eventCh <-chan inventory.VmEvent) {
	logger.Debug("SendVmEvents loop start...")
	for e := range eventCh {
		if (!is_connected()) {
			/* do nothing with the vm events if we are not connected */
			continue
		}
		var err error
		err = send_VE(&e)
		if (err != nil) {
			logger.Log("%s", err.Error())
		}
	}
	logger.Debug("SendVmEvents loop exit")
}

/* load and apply the reg vminfo of the queued hosts, one at a time to preserve the order */
func load_vminfo() {
	var (
		host_uuid string
		vi_ts int64
		vms []inventory.VmInfo
		err error
	)
	for host_uuid = range vminfo_ch {
		vi_ts, vms, err = reg.Load_vminfo(host_uuid)
		if (err != nil) {
			logger.Log("load_vminfo: %s", err.Error())
			continue
		}
		err = inventory.Update_host_vms(host_uuid, vi_ts, vms)
		if (err != nil) {
			logger.Log("load_vminfo: %s", err.Error())
		}
	}
}

/* remove the tags of our own serf member that are not HostInfo fields */
func remove_stale_tags() error {
	/* assert serf.m.Lock() */
	var (
		keys map[string]string
		stats map[string]map[string]string
		members []client.Member
		stale []string
		name, key string
		present bool
		err error
	)
	keys, err = serftags.Encode(&inventory.HostInfo{})
	if (err != nil) {
		return err
	}
	stats, err = serf.c.Stats()
	if (err != nil) {
		return err
	}
	name = stats["agent"]["name"]
	members, err = serf.c.Members()
	if (err != nil) {
		return err
	}
	for _, m := range members {
		if (m.Name != name) {
			continue
		}
		for key = range m.Tags {
			_, present = keys[key]
			if (!present) {
				stale = append(stale, key)
			}
		}
		if (len(stale) == 0) {
			return nil
		}
		return serf.c.UpdateTags(map[string]string{}, stale)
	}
	return fmt.Errorf("could not find our node name '%s' in serf", name)
}

/* add all current serf members to the inventory */
func read_all_hostinfo() error {
	/* assert serf.m.Lock() */
	var (
		members []client.Member
		hi inventory.HostInfo
		err error
	)
	members, err = serf.c.Members()
	if (err != nil) {
		return err
	}
	for _, m := range members {
		hi = inventory.HostInfo{}
		err = serftags.Decode(m.Tags, &hi)
		if (err != nil) {
			logger.Log("read_all_hostinfo: %s: %s", m.Name, err.Error())
			continue
		}
		if (hi.Uuid == "") {
			continue
		}
		switch (m.Status) {
		case "alive":
			hi.Cstate = openapi.CSTATE_ACTIVE
		case "leaving", "left":
			hi.Cstate = openapi.CSTATE_LEFT
		case "failed":
			hi.Cstate = openapi.CSTATE_FAILED
		case "none":
			hi.Cstate = openapi.CSTATE_INVALID
		default:
			logger.Log("read_all_hostinfo: %s: unknown serf status '%s'", m.Name, m.Status)
			continue
		}
		handle_hostinfo(&hi, m.Addr.String())
	}
	return nil
}

func Connect() error {
	serf.m.Lock()
	defer serf.m.Unlock()

	var err error
	serf.c, err = client.NewRPCClient(RPC_ADDR)
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
	err = remove_stale_tags()
	if (err != nil) {
		serf.c.Stop(serf.stream)
		serf.c.Close()
		serf.c = nil
		return err
	}
	err = read_all_hostinfo()
	if (err != nil) {
		serf.c.Stop(serf.stream)
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
	go load_vminfo()
	go recv_serf_events()
}

func stop_listening() {
	/* assert(serf.m.Lock()) */
	var err error
	if (serf.c == nil) {
		return
	}
	err = serf.c.Stop(serf.stream)
	if (err != nil) {
		logger.Log("%s", err.Error())
	}
	err = serf.c.Close()
	if (err != nil) {
		logger.Log("%s", err.Error())
	}
	serf.c = nil
}
