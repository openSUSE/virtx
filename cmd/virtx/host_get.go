package main

import (
	"fmt"
	"suse.com/virtx/pkg/model"
)

func host_get(host *openapi.Host) {
	var lcpus = host.Def.Cpudef.Sockets * host.Def.Cpudef.Cores * host.Def.Cpudef.Threads

	fmt.Fprintf(virtx.w, "NAME\tCPU\tVENDOR\tMODEL\tLCPUS\tTSC_FREQ\tFWVER\tFWDATE\tSTATE\t CPU_TOTAL\t  CPU_USED\t  CPU_FREE\tCPU_USE_OS\tCPU_RES_VM\tCPU_AVL_VM\t MEM_TOTAL\t  MEM_USED\t  MEM_FREE\tMEM_USE_OS\tMEM_RES_VM\tMEM_AVL_VM\n")
	fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\t%5d\t%d\t%s\t%s\t%s\t%7d MH\t%7d MH\t%7d MH\t%7d MH\t%7d MH\t%7d MH\t%7d MB\t%7d MB\t%7d MB\t%7d MB\t%7d MB\t%7d MB\n",
		host.Def.Name, host.Def.Cpuarch.Arch, host.Def.Cpuarch.Vendor, host.Def.Cpudef.Model, lcpus,
		host.Def.Tscfreq, host.Def.Sysinfo.Version, host.Def.Sysinfo.Date, host.State,

		host.Resources.Cpu.Total, host.Resources.Cpu.Used, host.Resources.Cpu.Free,
		host.Resources.Cpu.Usedos, host.Resources.Cpu.Reservedvms, host.Resources.Cpu.Availablevms,
		host.Resources.Memory.Total, host.Resources.Memory.Used, host.Resources.Memory.Free,
		host.Resources.Memory.Usedos, host.Resources.Memory.Reservedvms, host.Resources.Memory.Availablevms,
	)
}
