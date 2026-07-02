/*
 * Copyright (c) 2026 SUSE LLC
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

func vm_stats_get_req(arg string) {
	virtx.path = fmt.Sprintf("/vms/%s/stats", arg)
	virtx.method = "GET"
	virtx.arg = nil
	virtx.result = &openapi.Vmstats{}
}

func vm_stats_get(stats *openapi.Vmstats) {
	var (
		all bool
	)
	if (!virtx.cpu && !virtx.mem && !virtx.disk && !virtx.net) {
		all = true
	}
	if (virtx.cpu || all) {
		fmt.Fprintf(virtx.w, "CPU%%\t   MHZ_USED\n")
		fmt.Fprintf(virtx.w, "%5d\t%7d MHz\n", stats.CpuUtilization, stats.MhzUsed)
		fmt.Fprintf(virtx.w, "------------------\n")
	}
	if (virtx.mem || all) {
		fmt.Fprintf(virtx.w, "    MEM_CAP\t   RSS_USED\n")
		fmt.Fprintf(virtx.w, "%7d MiB\t%7d MiB\n", stats.MemoryCapacity, stats.MemoryUsed)
		fmt.Fprintf(virtx.w, "------------------------\n")
	}
	if (virtx.disk || all) {
		fmt.Fprintf(virtx.w, "DISK_CAP\t DISK_ALLOC\t  DISK_PHYS\t READ_KiBs\tWRITE_KiBs\tREAD_IOPS\tWRITE_IOPS\n")
		fmt.Fprintf(virtx.w, "%7d MiB\t%7d MiB\t%7d MiB\t%10d\t%10d\t%10d\t%10d\n",
			stats.DiskCapacity, stats.DiskAllocation, stats.DiskPhysical,
			stats.DiskRdBw, stats.DiskWrBw, stats.DiskRdIops, stats.DiskWrIops,
		)
		fmt.Fprintf(virtx.w, "-------------------------------------------------------------------------------------\n")
	}
	if (virtx.net || all) {
		fmt.Fprintf(virtx.w, "NETWORK_RX\t NETWORK_TX\n")
		fmt.Fprintf(virtx.w, "%7d KiB\t%7d KiB\n", stats.NetRxBw, stats.NetTxBw)
		fmt.Fprintf(virtx.w, "------------------------\n")
	}
}
