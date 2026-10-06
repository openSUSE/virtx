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
package inventory

import (
	"fmt"
	"maps"
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
	VI_ts int64                 /* Ts of the last change to the VmInfo of any VM on this host */
	Uuid string
	Mig_ip string               /* IP in the migration network, "" if not configured */
	openapi.HostListFields
}

/*
 * Hostdata: contains the hostinfo and also the UUIDs of VMS running on this host
 */
type Hostdata struct {
	VI_applied_ts int64
	Man_ip string               /* management IP, from the serf member address */
	Info HostInfo
	Vms map[string]nothing		/* VM Uuid presence */
}

/*
 * VmEvent: Vm state report/change event, sent to all hosts in the cluster to
 * minimally update the Vminfo.Runinfo.Runstate and Host fields.
 */
type VmEvent struct {
	Ts int64
	Uuid string
	Host string
	Runstate openapi.Vmrunstate
}

/*
 * VmInfo: simplified VM Data to keep in the inventory on all hosts in the cluster,
 * for quick access and search without having to contact the responsible libvirt.
 * Field names (including embedded) are the JSON keys of reg vminfo: do not rename.
 */
type VmInfo struct {
	VmEvent                     /* embedded basic information */
	VmDetails                   /* embedded details read from the domain metadata */
}

/* VmDetails: the fields derived from the domain metadata, filled by get_domain_details */
type VmDetails struct {
	Name string                 /* VM Name */
	Custom []openapi.CustomField
}

/*
 * Vmdata: contains the vminfo and also the Ts of the last Runstate/Host update
 */
type Vmdata struct {
	update_ts int64 /* from a VmEvent or VmInfo, whichever is newer */
	Info VmInfo
}

type HostsInventory map[string]Hostdata
type VmsInventory map[string]Vmdata

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
		/* copy the map: the caller reads it without holding inventory.m */
		hostdata.Vms = maps.Clone(hostdata.Vms)
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
		vmdata Vmdata
	)
	vmdata, present = inventory.vms[uuid]
	if (present) {
		return vmdata.Info, nil
	}
	return vmdata.Info, fmt.Errorf("inventory: no such vm %s", uuid)
}

func Update_host(hostinfo *HostInfo, man_ip string) bool {
	inventory.m.Lock()
	defer inventory.m.Unlock()

	return update_host(hostinfo, man_ip)
}

/* returns true if the reg vminfo of the host needs to be applied */
func update_host(hostinfo *HostInfo, man_ip string) bool {
	var (
		present bool
		hostdata Hostdata
	)
	hostdata, present = inventory.hosts[hostinfo.Uuid]
	if (present) {
		if (hostdata.Info.Ts > hostinfo.Ts) {
			logger.Log("Host %s: ignoring obsolete Host information: ts %d > %d",
				hostdata.Info.Name, hostdata.Info.Ts, hostinfo.Ts)
			return false
		}
		hostdata.Info = *hostinfo
	} else {
		/* this is the first time we see this host. */
		hostdata = Hostdata{
			Info: *hostinfo,
			Vms: make(map[string]nothing),
		}
	}
	hostdata.Man_ip = man_ip
	inventory.hosts[hostinfo.Uuid] = hostdata
	/* VI_ts is only compared for equality, see system_info_get */
	return (hostdata.VI_applied_ts != hostinfo.VI_ts)
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
		vmdata Vmdata
		present bool
	)
	vmdata, present = inventory.vms[uuid]
	if (!present) {
		return fmt.Errorf("no such VM %s", uuid)
	}
	if (vmdata.update_ts > ts) {
		logger.Log("Vm %s: ignoring obsolete Vm state information: ts %d > %d",	uuid, vmdata.update_ts, ts)
		return nil
	}
	if (state == openapi.RUNSTATE_DELETED) {
		delete_hostdata_vm(uuid, vmdata.Info.Host, host)
		delete(inventory.vms, uuid)
		return nil
	}
	update_hostdata_vm(uuid, vmdata.Info.Host, host)

	/* update the vms inventory data */
	vmdata.update_ts = ts
	vmdata.Info.Runstate = state
	vmdata.Info.Host = host
	inventory.vms[uuid] = vmdata
	return nil
}

func Update_vm(vminfo *VmInfo) error {
	inventory.m.Lock()
	defer inventory.m.Unlock()

	return update_vm(vminfo)
}

func update_vm(vminfo *VmInfo) error {
	var (
		cur, vmdata Vmdata
		present bool
	)
	cur, present = inventory.vms[vminfo.Uuid]
	if (present && cur.Info.Ts > vminfo.Ts) {
		logger.Log("Ignoring old guest info: ts %d > %d %s %s",
			cur.Info.Ts, vminfo.Ts, vminfo.Uuid, vminfo.Name,
		)
		return nil
	}
	vmdata = Vmdata{ update_ts: vminfo.Ts, Info: *vminfo }
	if (present && cur.update_ts > vminfo.Ts) {
		/* a newer VmEvent already set these: keep them */
		vmdata.update_ts = cur.update_ts
		vmdata.Info.Runstate = cur.Info.Runstate
		vmdata.Info.Host = cur.Info.Host
	}
	update_hostdata_vm(vminfo.Uuid, cur.Info.Host, vmdata.Info.Host)
	inventory.vms[vminfo.Uuid] = vmdata
	return nil
}

/* Update_host_vms replaces the VMs of a host with the ones read from reg vminfo */
func Update_host_vms(host_uuid string, vi_ts int64, vms []VmInfo) error {
	inventory.m.Lock()
	defer inventory.m.Unlock()

	return update_host_vms(host_uuid, vi_ts, vms)
}

func update_host_vms(host_uuid string, vi_ts int64, vms []VmInfo) error {
	var (
		hostdata Hostdata
		vmdata Vmdata
		listed map[string]bool = make(map[string]bool, len(vms))
		uuid string
		present bool
		i int
	)
	hostdata, present = inventory.hosts[host_uuid]
	if (!present) {
		return fmt.Errorf("no such host %s", host_uuid)
	}
	/* VI_ts is only compared for equality, see system_info_get */
	if (hostdata.VI_applied_ts == vi_ts) {
		return nil
	}
	for i = range vms {
		listed[vms[i].Uuid] = true
		update_vm(&vms[i])
	}
	/* remove the VMs not listed, unless updated later or now on another host */
	for uuid = range hostdata.Vms {
		if (listed[uuid]) {
			continue
		}
		vmdata = inventory.vms[uuid]
		if (vmdata.Info.Host == host_uuid && vmdata.update_ts <= vi_ts) {
			delete(hostdata.Vms, uuid)
			delete(inventory.vms, uuid)
		}
	}
	hostdata.VI_applied_ts = vi_ts
	inventory.hosts[host_uuid] = hostdata
	return nil
}

/* update Hostdata Vms, including new entry into hostdata and update for VM migration */
func update_hostdata_vm(uuid string, old_host string, new_host string) {
	/* assert inventory.m.Lock() */
	_, present := inventory.hosts[old_host]
	if (present && old_host != new_host) {
		/*
		 *  we seem to have changed hosts, which normally follows a VmEvent of a resumed migrated domain:
		 *  (e.Event == libvirt.DOMAIN_EVENT_RESUMED && e.Detail == libvirt.DOMAIN_EVENT_RESUMED_MIGRATED)
		 */
		delete(inventory.hosts[old_host].Vms, uuid)
	}
	_, present = inventory.hosts[new_host]
	if (present) {
		/* add the VM to the new host */
		inventory.hosts[new_host].Vms[uuid] = nothing{}
	} else {
		logger.Log("VM %s refers to unknown host %s", uuid, new_host)
	}
}

func delete_hostdata_vm(uuid string, old_host string, new_host string) {
	/* assert inventory.m.Lock() */
	_, present := inventory.hosts[old_host]
	if (present) {
		delete(inventory.hosts[old_host].Vms, uuid)
	} else {
		logger.Log("deleted VM %s in unknown old host %s", uuid, old_host)
	}
	if (old_host == new_host) {
		return /* most usual scenario */
	}
	/* weird case of a VM being deleted after migrating to a new host */
	_, present = inventory.hosts[new_host]
	if (present) {
		delete(inventory.hosts[new_host].Vms, uuid)
	} else {
		logger.Log("deleted VM %s in unknown new host %s", uuid, new_host)
	}
}
