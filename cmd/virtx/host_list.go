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

func host_list_req() {
	virtx.path = "/hosts"
	virtx.method = "GET"
	virtx.arg = &virtx.host_list_options
	virtx.result = &openapi.HostList{}
}

func host_list(list *openapi.HostList) {

	fmt.Fprintf(virtx.w, "UUID\tNAME\tOS\tVERSION\tCPU\tVENDOR\tMODEL\tTHREADS\t MEM_AVL_VM\t HPG_AVL_VM\tCSTATE\tAGE\n")

	for _, item := range (list.Items) {
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%7d\t%7d MiB\t%7d MiB\t%s\t%s\n",
			item.Uuid, item.Fields.Name, item.Fields.Osid, item.Fields.Osv,
			item.Fields.Cpudef.Arch, item.Fields.Cpudef.Vendor, item.Fields.Cpudef.Model,
			item.Fields.Cpudef.Nodes * item.Fields.Cpudef.Sockets * item.Fields.Cpudef.Cores * item.Fields.Cpudef.Threads,
			item.Fields.Memoryavailable, item.Fields.Hpavailable,
			item.Fields.Cstate, ts.Since(item.Fields.Ts))
	}
}
