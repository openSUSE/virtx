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
	"suse.com/virtx/pkg/model"
)

type Hostdata struct {
	host openapi.Host
	vms map[string]struct{}		/* VM Uuid presence */
}

type Vmdata struct {
	Uuid string                 /* VM Uuid */
	Name string                 /* VM Name */
	Runinfo openapi.Vmruninfo   /* host running the VM and VM runstate */
	Vlanid int16                /* XXX need requirements engineering for Vlans XXX */
	Custom []openapi.CustomField
	Stats openapi.Vmstats

	Cpu_time uint64             /* Total cpu time consumed in nanoseconds from libvirt.DomainCPUStats.CpuTime */
	Net_rx int64                /* Net Rx bytes */
	Net_tx int64                /* Net Tx bytes */

	Ts int64
}

type VmEvent struct {
	Uuid string
	State openapi.Vmrunstate
	Ts int64
}

type HostsInventory map[string]Hostdata
type VmsInventory map[string]Vmdata

type Inventory struct {
	m       sync.RWMutex
	cluster openapi.Cluster
	hosts   HostsInventory
	vms     VmsInventory
}

var inventory Inventory

func init() {
	inventory = Inventory{
		m:       sync.RWMutex{},
		cluster: openapi.Cluster{},
		hosts:   make(HostsInventory),
		vms:     make(VmsInventory),
	}
}

func Get_host(uuid string) (openapi.Host, error) {
	inventory.m.RLock()
	defer inventory.m.RUnlock()
	var (
		present bool
		hostdata Hostdata
	)
	hostdata, present = inventory.hosts[uuid]
	if (present) {
		return hostdata.host, nil
	}
	return hostdata.host, fmt.Errorf("inventory: no such host %s", uuid)
}

func Get_vm(uuid string) (Vmdata, error) {
	inventory.m.RLock()
	defer inventory.m.RUnlock()
	var (
		present bool
		vmdata Vmdata
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
		hostdata Hostdata
	)
	hostdata, present = inventory.hosts[host.Uuid]
	if (present) {
		if (hostdata.host.Ts > host.Ts) {
			logger.Log("Host %s: ignoring obsolete Host information: ts %d > %d",
				hostdata.host.Def.Name, hostdata.host.Ts, host.Ts)
			return
		}
		hostdata.host = *host
	} else {
		hostdata = Hostdata{
			host: *host,
			vms: make(map[string]struct{}),
		}
	}
	inventory.hosts[host.Uuid] = hostdata
}

func Set_host_state(uuid string, newstate openapi.Hoststate) error {
	inventory.m.Lock()
	defer inventory.m.Unlock()

	return set_host_state(uuid, newstate)
}

func set_host_state(uuid string, newstate openapi.Hoststate) error {
	hostdata, ok := inventory.hosts[uuid]
	if !ok {
		return fmt.Errorf("no such host %s", uuid)
	}
	hostdata.host.State = newstate
	inventory.hosts[uuid] = hostdata
	return nil
}

func Update_vm_state(e *VmEvent) error {
	inventory.m.Lock()
	defer inventory.m.Unlock()
	var (
		vmdata Vmdata
		hostdata Hostdata
		present bool
	)
	vmdata, present = inventory.vms[e.Uuid]
	if (!present) {
		return fmt.Errorf("no such VM %s", e.Uuid)
	}
	vmdata.Runinfo.Runstate = openapi.Vmrunstate(e.State)
	if (vmdata.Runinfo.Runstate == openapi.RUNSTATE_DELETED) {
		var host_uuid string = vmdata.Runinfo.Host
		hostdata, present = inventory.hosts[host_uuid]
		if (present) {
			delete(hostdata.vms, e.Uuid)
		} else {
			logger.Log("deleted VM %s does not appear in its host %s", e.Uuid, host_uuid)
		}
		delete(inventory.vms, e.Uuid)
	} else {
		inventory.vms[e.Uuid] = vmdata
	}
	return nil
}

func Update_vm(vmdata *Vmdata) error {
	inventory.m.Lock()
	defer inventory.m.Unlock()

	return update_vm(vmdata)
}

func update_vm(vmdata *Vmdata) error {
	var (
		old Vmdata
		present bool
	)
	old, present = inventory.vms[vmdata.Uuid]
	if (present) {
		if (old.Ts > vmdata.Ts) {
			logger.Log("Ignoring old guest info: ts %d > %d %s %s",
				old.Ts, vmdata.Ts, vmdata.Uuid, vmdata.Name,
			)
			return nil
		}
	} else { /* not present */
		var (
			host_uuid string = vmdata.Runinfo.Host
			hostdata Hostdata
		)
		hostdata, present = inventory.hosts[host_uuid]
		if (present) {
			hostdata.vms[vmdata.Uuid] = struct{}{}
			inventory.hosts[host_uuid] = hostdata
		} else {
			logger.Log("new VM %s assigned to nonexisting host %s", vmdata.Uuid, host_uuid)
		}
	}
	inventory.vms[vmdata.Uuid] = *vmdata
	return nil
}
