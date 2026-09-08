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
)

func host_get_req(arg string) {
	virtx.path = fmt.Sprintf("/hosts/%s", arg)
	virtx.method = "GET"
	virtx.arg = nil
	virtx.result = &openapi.Host{}
}

func host_get(host *openapi.Host) {
	if (virtx.cpu) {
		fmt.Fprintf(virtx.w, "ARCH\tVENDOR\tMODEL\tNODES\tSOCKS\tCORES\tTH\tTSC_FREQ\n")
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%5d\t%5d\t%5d\t%2d\t%d\n",
			host.Def.Cpudef.Arch, host.Def.Cpudef.Vendor, host.Def.Cpudef.Model,
			host.Def.Cpudef.Nodes, host.Def.Cpudef.Sockets, host.Def.Cpudef.Cores, host.Def.Cpudef.Threads,
			host.Def.Tscfreq,
		)
	} else if (virtx.net) {
		fmt.Fprintf(virtx.w, "MGMT_IFACE\tMGMT_ADDR\tMIG_IFACE\tMIG_ADDR\n")
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\n",
			host.Net.ManagementIface, host.Net.ManagementAddr,
			host.Net.MigrationIface, host.Net.MigrationAddr,
		)
	} else if (virtx.sys) {
		fmt.Fprintf(virtx.w, "OS\tVERSION\tFWVER\tFWDATE\n")
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\n",
			host.Def.Osid, host.Def.Osv, host.Def.Sysinfo.Version, host.Def.Sysinfo.Date,
		)
	} else {
		fmt.Fprintf(virtx.w, "NAME\tCSTATE\tLOCKID\n")
		fmt.Fprintf(virtx.w, "%s\t%s\t %4d\n",
			host.Def.Name, host.Cstate, host.Lockid,
		)
	}
}
