package main

import (
	"fmt"
	"suse.com/virtx/pkg/model"
)

func host_get(host *openapi.Host) {
	if (virtx.stat) {
		fmt.Fprintf(virtx.w, "  CPU_TOTAL\t   CPU_USED\t   CPU_FREE\tCPU_USED_OS\tCPU_USED_VM\t CPU_RES_VM\t CPU_AVL_VM\t  MEM_TOTAL\t   MEM_USED\t   MEM_FREE\tMEM_USED_OS\t MEM_RES_VM\t MEM_AVL_VM\n")
		fmt.Fprintf(virtx.w, "%7d MHz\t%7d MHz\t%7d MHz\t%7d MHz\t%7d MHz\t%7d MHz\t%7d MHz\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\n",
			host.Resources.Cpu.Total, host.Resources.Cpu.Used, host.Resources.Cpu.Free,
			host.Resources.Cpu.Usedos, host.Resources.Cpu.Usedvms,
			host.Resources.Cpu.Reservedvms, host.Resources.Cpu.Availablevms,
			host.Resources.Memory.Total, host.Resources.Memory.Used, host.Resources.Memory.Free,
			host.Resources.Memory.Usedos, host.Resources.Memory.Reservedvms, host.Resources.Memory.Availablevms,
		)
	} else {
		fmt.Fprintf(virtx.w, "NAME\tCPU\tVENDOR\tMODEL\tSOCKS\tCORES\tTH\tTSC_FREQ\tFWVER\tFWDATE\tSTATE\n")
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\t%5d\t%5d\t%2d\t%d\t%s\t%s\t%s\n",
			host.Def.Name, host.Def.Cpuarch.Arch, host.Def.Cpuarch.Vendor, host.Def.Cpudef.Model,
			host.Def.Cpudef.Sockets, host.Def.Cpudef.Cores, host.Def.Cpudef.Threads,
			host.Def.Tscfreq, host.Def.Sysinfo.Version, host.Def.Sysinfo.Date, host.State,
		)
	}
}
