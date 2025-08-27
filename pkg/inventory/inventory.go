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
package inventory

import (
	"fmt"
	"sync"

	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/model"
	. "suse.com/virtx/pkg/constants"
)

type Hostdata map[string]openapi.Host
type Vmdata map[string]hypervisor.Vmdata

type Inventory struct {
	m       sync.RWMutex
	cluster openapi.Cluster
	hosts   Hostdata
	vms     Vmdata
}

var inventory Inventory

func init() {
	inventory = Inventory{
		m:       sync.RWMutex{},
		cluster: openapi.Cluster{},
		hosts:   make(Hostdata),
		vms:     make(Vmdata),
	}
}

func Get_host(uuid string) (openapi.Host, error) {
	inventory.m.RLock()
	defer inventory.m.RUnlock()
	var (
		present bool
		host openapi.Host
	)
	host, present = inventory.hosts[uuid]
	if (present) {
		return host, nil
	}
	return host, fmt.Errorf("inventory: no such host %s", uuid)
}

func Get_vm(uuid string) (hypervisor.Vmdata, error) {
	inventory.m.RLock()
	defer inventory.m.RUnlock()
	var (
		present bool
		vmdata hypervisor.Vmdata
	)
	vmdata, present = inventory.vms[uuid]
	if (present) {
		return vmdata, nil
	}
	return vmdata, fmt.Errorf("inventory: no such vm %s", uuid)
}

func Update_host(host *openapi.Host) {
	inventory.m.Lock()
	defer inventory.m.Unlock()

	update_host(host)
}

func update_host(host *openapi.Host) {
	var (
		present bool
		old openapi.Host
	)
	old, present = inventory.hosts[host.Uuid]
	if (present && old.Ts > host.Ts) {
		logger.Log("Host %s: ignoring obsolete Host information: ts %d > %d",
			old.Def.Name, old.Ts, host.Ts)
		return
	}
	inventory.hosts[host.Uuid] = *host
	return
}

func Set_host_state(uuid string, newstate openapi.Hoststate) error {
	inventory.m.Lock()
	defer inventory.m.Unlock()

	return set_host_state(uuid, newstate)
}

func set_host_state(uuid string, newstate openapi.Hoststate) error {
	host, ok := inventory.hosts[uuid]
	if !ok {
		return fmt.Errorf("no such host %s", uuid)
	}
	host.State = newstate
	inventory.hosts[uuid] = host
	return nil
}

func Update_vm_state(e *hypervisor.VmEvent) error {
	inventory.m.Lock()
	defer inventory.m.Unlock()
	vmdata, ok := inventory.vms[e.Uuid]
	if !ok {
		return fmt.Errorf("no such VM %s", e.Uuid)
	}
	vmdata.Runinfo.Runstate = openapi.Vmrunstate(e.State)
	if (vmdata.Runinfo.Runstate == openapi.RUNSTATE_DELETED) {
		delete(inventory.vms, e.Uuid)
	} else {
		inventory.vms[e.Uuid] = vmdata
	}
	return nil
}

func Update_vm(vmdata *hypervisor.Vmdata) error {
	inventory.m.Lock()
	defer inventory.m.Unlock()

	return update_vm(vmdata)
}

func update_vm(vmdata *hypervisor.Vmdata) error {
	if (inventory.vms == nil) {
		inventory.vms = make(map[string]hypervisor.Vmdata)
	}
	if old, ok := inventory.vms[vmdata.Uuid]; ok {
		if (old.Ts > vmdata.Ts) {
			logger.Log("Ignoring old guest info: ts %d > %d %s %s",
				old.Ts, vmdata.Ts, vmdata.Uuid, vmdata.Name,
			)
			return nil
		}
		/* calculate deltas from previous Vm info */
		if (vmdata.Runinfo.Runstate > openapi.RUNSTATE_POWEROFF &&
			old.Runinfo.Runstate > openapi.RUNSTATE_POWEROFF) {
			var delta uint64 = hypervisor.Counter_delta_uint64(vmdata.Cpu_time, old.Cpu_time)
			if (delta > 0 && (vmdata.Ts - old.Ts) > 0 && vmdata.Stats.Vcpus > 0) {
				vmdata.Stats.CpuUtilization = int32((delta * 100) / (uint64(vmdata.Ts - old.Ts) * 1000000))
			}
		}
		{
			var delta int64 = hypervisor.Counter_delta_int64(vmdata.Net_rx, old.Net_rx)
			if (delta > 0 && (vmdata.Ts - old.Ts) > 0) {
				vmdata.Stats.NetRxBw = int32((delta * 1000) / ((vmdata.Ts - old.Ts) * KiB))
			}
			delta = hypervisor.Counter_delta_int64(vmdata.Net_tx, old.Net_tx)
			if (delta > 0 && (vmdata.Ts - old.Ts) > 0) {
				vmdata.Stats.NetTxBw = int32((delta * 1000) / ((vmdata.Ts - old.Ts) * KiB))
			}
		}
	}
	inventory.vms[vmdata.Uuid] = *vmdata
	return nil
}
