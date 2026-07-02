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

func host_stats_get_req(arg string) {
	virtx.path = fmt.Sprintf("/hosts/%s/stats", arg)
	virtx.method = "GET"
	virtx.arg = nil
	virtx.result = &openapi.Hoststats{}
}

func host_stats_get(stats *openapi.Hoststats) {
	var (
		all bool
	)
	if (!virtx.cpu && !virtx.mem && !virtx.disk && !virtx.net) {
		all = true
	}
	if (virtx.cpu || all) {
		fmt.Fprintf(virtx.w, "  CPU_TOTAL\t   CPU_USED\t   CPU_FREE\tCPU_USED_OS\tCPU_USED_VM\t CPU_RES_VM\t CPU_AVL_VM\n")
		fmt.Fprintf(virtx.w, "%7d MHz\t%7d MHz\t%7d MHz\t%7d MHz\t%7d MHz\t%7d MHz\t%7d MHz\n",
			stats.Cpu.Total, stats.Cpu.Used, stats.Cpu.Free, stats.Cpu.Usedos, stats.Cpu.Usedvms, stats.Cpu.Reservedvms, stats.Cpu.Availablevms,
		)
		fmt.Fprintf(virtx.w, "------------------------------------------------------------------------------------------\n")
	}
	if (virtx.mem || all) {
		fmt.Fprintf(virtx.w, "  MEM_TOTAL\t   MEM_USED\t   MEM_FREE\tMEM_USED_OS\tMEM_USED_VM\t MEM_RES_VM\t MEM_AVL_VM\n")
		fmt.Fprintf(virtx.w, "%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\n",
			stats.Memory.Total, stats.Memory.Used, stats.Memory.Free, stats.Memory.Usedos, stats.Memory.Usedvms, stats.Memory.Reservedvms, stats.Memory.Availablevms,
		)
		fmt.Fprintf(virtx.w, "------------------------------------------------------------------------------------------\n")

		fmt.Fprintf(virtx.w, "  HPG_TOTAL\t   HPG_USED\t   HPG_FREE\tHPG_USED_OS\tHPG_USED_VM\t HPG_RES_VM\t HPG_AVL_VM\n")
		fmt.Fprintf(virtx.w, "%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\n",
			stats.Hp.Total, stats.Hp.Used, stats.Hp.Free, stats.Hp.Usedos, stats.Hp.Usedvms, stats.Hp.Reservedvms, stats.Hp.Availablevms,
		)
		fmt.Fprintf(virtx.w, "------------------------------------------------------------------------------------------\n")
	}
}
