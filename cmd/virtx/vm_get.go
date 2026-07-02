/*
 * Copyright (c) 2025-2026 SUSE LLC
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

package main

import (
	"fmt"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/ts"
)

func vm_get_req(arg string) {
	virtx.path = fmt.Sprintf("/vms/%s", arg)
	virtx.method = "GET"
	virtx.arg = nil
	virtx.result = &openapi.Vm{}
}

func vm_get(vm *openapi.Vm) {
	if (virtx.disk) {
		fmt.Fprintf(virtx.w, "PATH\tDEVICE\tBUS\tMAN\tPROV\n")
		vm_get_disk(&vm.Def.Osdisk);
		for _, disk := range (vm.Def.Disks) {
			vm_get_disk(&disk)
		}
	} else if (virtx.net) {
		fmt.Fprintf(virtx.w, "NAME\tTYPE\tMODEL\tMAC\n")
		for _, net := range (vm.Def.Nets) {
			vm_get_net(&net)
		}
	} else {
		var (
			boot_ts int64
			items []openapi.OplogItem = vm.Oplog.Items
			o openapi.Operation
		)
		for i := 0; i < len(items); i++ {
			if (o.Parse(items[i].Op) == nil && o == openapi.OpVmBoot) {
				boot_ts = items[i].Te
				break
			}
		}
		fmt.Fprintf(virtx.w, "NAME\tHOST\tVCPU\tSOCKETS\t CORES\tTHREADS\tVLAN\tCUSTOM\tLAST BOOT\tSTATE\n")
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%7d\t%7d\t%7d\t%4d\t%v\t%s\t%s\n",
			vm.Def.Name, vm.Runinfo.Host, vm.Def.Cpudef.Model,
			vm.Def.Cpudef.Sockets, vm.Def.Cpudef.Cores, vm.Def.Cpudef.Threads,
			vm.Def.Vlanid, vm.Def.Custom, ts.String(boot_ts), vm.Runinfo.Runstate)
	}
}

func vm_get_disk(disk *openapi.Disk) {
	fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\t%s\n", disk.Path, disk.Device, disk.Bus, disk.Man, disk.Prov)
}

func vm_get_net(net *openapi.Net) {
	fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\n", net.Name, net.Nettype, net.Model, net.Mac)
}
