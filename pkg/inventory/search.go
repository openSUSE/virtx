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
	"strings"

	"suse.com/virtx/pkg/model"
)

/* filters: [name, cpuarch, cpudef, hoststate, memoryavailable] */

func Search_hosts(f openapi.HostListFields) openapi.HostList {
	inventory.m.RLock()
	defer inventory.m.RUnlock()
	var (
		hostdata Hostdata
		host openapi.Host
		list openapi.HostList
	)
	for _, hostdata = range inventory.hosts {
		host = hostdata.host
		if (f.Name != "" && !strings.Contains(host.Def.Name, f.Name)) {
			continue
		}
		if (f.Cpuarch.Arch != "" && (host.Def.Cpuarch.Arch != f.Cpuarch.Arch)) {
			continue
		}
		if (f.Cpuarch.Vendor != "" && (host.Def.Cpuarch.Vendor != f.Cpuarch.Vendor)) {
			continue
		}
		if (f.Cpudef.Model != "" && (host.Def.Cpudef.Model != f.Cpudef.Model)) {
			continue
		}
		if (f.Cpudef.Sockets > 0 && (host.Def.Cpudef.Sockets < f.Cpudef.Sockets)) {
			continue
		}
		if (f.Cpudef.Cores > 0 && (host.Def.Cpudef.Cores < f.Cpudef.Cores)) {
			continue
		}
		if (f.Cpudef.Threads > 0 && (host.Def.Cpudef.Threads < f.Cpudef.Threads)) {
			continue
		}
		if (f.Hoststate != openapi.HOST_INVALID && (host.State != f.Hoststate)) {
			continue
		}
		if (f.Memoryavailable > 0 && (host.Resources.Memory.Availablevms < f.Memoryavailable)) {
			continue
		}
		var item openapi.HostListItem = openapi.HostListItem{
			Uuid: host.Uuid,
			Fields: openapi.HostListFields{
				Name: host.Def.Name,
				Cpuarch: host.Def.Cpuarch,
				Cpudef: host.Def.Cpudef,
				Hoststate: host.State,
				Memoryavailable: host.Resources.Memory.Availablevms,
			},
		}
		list.Items = append(list.Items, item)
	}
	return list
}

func Search_vms(f openapi.VmListFields) openapi.VmList {
	inventory.m.RLock()
	defer inventory.m.RUnlock()
	var (
		vm Vmdata
		list openapi.VmList
	)
	for _, vm = range inventory.vms {
		if (f.Name != "" && !strings.Contains(vm.Name, f.Name)) {
			continue
		}
		if (f.Host != "" && (vm.Runinfo.Host != f.Host)) {
			continue
		}
		if (f.Runstate > 0 && (vm.Runinfo.Runstate != f.Runstate)) {
			continue
		}
		if (f.Vlanid > 0 && (vm.Vlanid != f.Vlanid)) {
			continue
		}
		if (f.Custom.Name != "") {
			var found bool
			for _, custom := range vm.Custom {
				if (custom.Name == f.Custom.Name) {
					if (custom.Value == f.Custom.Value) {
						found = true
						break
					}
				}
			}
			if (!found) {
				continue
			}
		}
		var item openapi.VmListItem = openapi.VmListItem{
			Uuid: vm.Uuid,
			Fields: openapi.VmListFields{
				Name: vm.Name,
				Host: vm.Runinfo.Host,
				Runstate: vm.Runinfo.Runstate,
				Vlanid: vm.Vlanid,
				Custom: f.Custom,
			},
		}
		list.Items = append(list.Items, item)
	}
	return list
}
