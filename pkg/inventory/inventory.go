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

type nothing struct {
}

/*
 * HostInfo: Host Information to keep in all hosts of the cluster,
 * for quick access and search without having to contact the host
 */
type HostInfo struct {
	openapi.Host
}

/*
 * Hostdata: contains the hostinfo and also the UUIDs of VMS running on this host
 */
type Hostdata struct {
	Info HostInfo
	Vms map[string]nothing		/* VM Uuid presence */
}

/*
 * VmEvent: Vm state report/change event, sent to all hosts in the cluster to
 * minimally update the Vminfo.Runinfo.Runstate field.
 */
type VmEvent struct {
	Uuid string
	Host string
	Runstate openapi.Vmrunstate /* the main purpose of the VmEvent, runstate update */
	Ts int64
}

/*
 * VmInfo: simplified VM Data to keep in the inventory on all hosts in the cluster,
 * for quick access and search without having to contact the responsible libvirt
 */
type VmInfo struct {
	VmEvent                     /* embedded basic information */
	Name string                 /* VM Name */
	Vlanid int16                /* XXX need requirements engineering for Vlans XXX */
	Custom []openapi.CustomField
	Vcpus int16                 /* total number of vcpus in this VM */
}

type HostsInventory map[string]Hostdata
type VmsInventory map[string]VmInfo

type Inventory struct {
	m       sync.RWMutex
	hosts   HostsInventory
	vms     VmsInventory
}

var inventory Inventory

func init() {
	inventory = Inventory{
		m:       sync.RWMutex{},
		hosts:   make(HostsInventory),
		vms:     make(VmsInventory),
	}
}

func Get_hostdata(uuid string) (Hostdata, error) {
	inventory.m.RLock()
    defer inventory.m.RUnlock()
	var (
		present bool
		hostdata Hostdata
	)
	hostdata, present = inventory.hosts[uuid]
	if (present) {
		return hostdata, nil
	}
	return hostdata, fmt.Errorf("inventory: no such host %s", uuid)
}

func Get_hostinfo(uuid string) (HostInfo, error) {
	inventory.m.RLock()
	defer inventory.m.RUnlock()
	var (
		present bool
		hostdata Hostdata
	)
	hostdata, present = inventory.hosts[uuid]
	if (present) {
		return hostdata.Info, nil
	}
	return hostdata.Info, fmt.Errorf("inventory: no such host %s", uuid)
}

func Get_vminfo(uuid string) (VmInfo, error) {
	inventory.m.RLock()
	defer inventory.m.RUnlock()
	var (
		present bool
		vminfo VmInfo
	)
	vminfo, present = inventory.vms[uuid]
	if (present) {
		return vminfo, nil
	}
	return vminfo, fmt.Errorf("inventory: no such vm %s", uuid)
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
		if (hostdata.Info.Ts > host.Ts) {
			logger.Log("Host %s: ignoring obsolete Host information: ts %d > %d",
				hostdata.Info.Def.Name, hostdata.Info.Ts, host.Ts)
			return
		}
		hostdata.Info.Host = *host
	} else {
		/* this is the first time we see this host. */
		hostdata = Hostdata{
			Info: HostInfo { *host },
			Vms: make(map[string]nothing),
		}
	}
	inventory.hosts[host.Uuid] = hostdata
}

func Set_host_state(uuid string, newstate openapi.Cstate) error {
	inventory.m.Lock()
	defer inventory.m.Unlock()

	return set_host_state(uuid, newstate)
}

func set_host_state(uuid string, newstate openapi.Cstate) error {
	hostdata, ok := inventory.hosts[uuid]
	if !ok {
		return fmt.Errorf("no such host %s", uuid)
	}
	hostdata.Info.Cstate = newstate
	inventory.hosts[uuid] = hostdata
	return nil
}

func Update_vm_state(e *VmEvent) error {
	inventory.m.Lock()
	defer inventory.m.Unlock()
	return update_vm_state(e.Uuid, e.Runstate, e.Host, e.Ts)
}

func update_vm_state(uuid string, state openapi.Vmrunstate, host string, ts int64) error {
	var (
		vminfo VmInfo
		present bool
	)
	vminfo, present = inventory.vms[uuid]
	if (!present) {
		return fmt.Errorf("no such VM %s", uuid)
	}
	if (vminfo.Ts > ts) {
		logger.Log("Vm %s: ignoring obsolete Vm state information: ts %d > %d",	uuid, vminfo.Ts, ts)
		return nil
	}
	vminfo.Runstate = state

	if (vminfo.Runstate == openapi.RUNSTATE_DELETED) {
		var host_uuid string = vminfo.Host
		_, present = inventory.hosts[host_uuid]
		if (present) {
			delete(inventory.hosts[host_uuid].Vms, uuid)
		} else {
			logger.Log("deleted VM %s in unknown host %s", uuid, host_uuid)
		}
		delete(inventory.vms, uuid)
		return nil
	}

	/* for all other states, check to see if we have to update host */
	var old_host string = vminfo.Host
	vminfo.Host = host

	_, present = inventory.hosts[old_host]
	if (present) {
		if (old_host != host) {
			/*
			 *  we seem to have changed hosts, which normally follows a VmEvent of a resumed migrated domain:
			 *  (e.Event == libvirt.DOMAIN_EVENT_RESUMED && e.Detail == libvirt.DOMAIN_EVENT_RESUMED_MIGRATED)
			 */
			delete(inventory.hosts[old_host].Vms, uuid)
		}
	}
	_, present = inventory.hosts[host]
	if (present) {
		if (old_host != host) {
			/* add the VM to the new host (migration or weird cases of missed events) */
			inventory.hosts[host].Vms[uuid] = nothing{}
		}
	} else {
		logger.Log("VM %s refers to unknown host %s", vminfo.Uuid, host)
	}
	/* update the vms inventory data */
	inventory.vms[uuid] = vminfo
	return nil
}

func Update_vm(vminfo *VmInfo) error {
	inventory.m.Lock()
	defer inventory.m.Unlock()

	return update_vm(vminfo)
}

func update_vm(vminfo *VmInfo) error {
	var (
		old VmInfo
		present bool
	)
	old, present = inventory.vms[vminfo.Uuid]
	if (present) {
		if (old.Ts > vminfo.Ts) {
			logger.Log("Ignoring old guest info: ts %d > %d %s %s",
				old.Ts, vminfo.Ts, vminfo.Uuid, vminfo.Name,
			)
			return nil
		}
		/*
		 * generate an artificial state change event to make sure to update the
		 * inventory data structures, should we have missed previous events
		 */
		update_vm_state(vminfo.Uuid, vminfo.Runstate, vminfo.Host, vminfo.Ts)

	} else { /* not present */
		var (
			host_uuid string = vminfo.Host
			hostdata Hostdata
		)
		hostdata, present = inventory.hosts[host_uuid]
		if (present) {
			hostdata.Vms[vminfo.Uuid] = nothing{}
			inventory.hosts[host_uuid] = hostdata
		} else {
			logger.Log("new VM %s assigned to unknown host %s", vminfo.Uuid, host_uuid)
		}
	}
	inventory.vms[vminfo.Uuid] = *vminfo
	return nil
}
