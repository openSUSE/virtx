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
	if (virtx.stat_cpu) {
		fmt.Fprintf(virtx.w, "  CPU_TOTAL\t   CPU_USED\t   CPU_FREE\tCPU_USED_OS\tCPU_USED_VM\t CPU_RES_VM\t CPU_AVL_VM\n")
		fmt.Fprintf(virtx.w, "%7d MHz\t%7d MHz\t%7d MHz\t%7d MHz\t%7d MHz\t%7d MHz\t%7d MHz\n",
			host.Resources.Cpu.Total, host.Resources.Cpu.Used, host.Resources.Cpu.Free,
			host.Resources.Cpu.Usedos, host.Resources.Cpu.Usedvms,
			host.Resources.Cpu.Reservedvms, host.Resources.Cpu.Availablevms,
		)
	} else if (virtx.stat_mem) {
		fmt.Fprintf(virtx.w, "  MEM_TOTAL\t   MEM_USED\t   MEM_FREE\tMEM_USED_OS\tMEM_USED_VM\t MEM_RES_VM\t MEM_AVL_VM\n")
		fmt.Fprintf(virtx.w, "%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\n",
			host.Resources.Memory.Total, host.Resources.Memory.Used, host.Resources.Memory.Free,
			host.Resources.Memory.Usedos, host.Resources.Memory.Usedvms,
			host.Resources.Memory.Reservedvms, host.Resources.Memory.Availablevms,
		)
		fmt.Fprintf(virtx.w, "  HPG_TOTAL\t   HPG_USED\t   HPG_FREE\tHPG_USED_OS\tHPG_USED_VM\t HPG_RES_VM\t HPG_AVL_VM\n")
		fmt.Fprintf(virtx.w, "%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\n",
			host.Resources.Hp.Total, host.Resources.Hp.Used, host.Resources.Hp.Free,
			host.Resources.Hp.Usedos, host.Resources.Hp.Usedvms,
			host.Resources.Hp.Reservedvms, host.Resources.Hp.Availablevms,
		)
	} else {
		fmt.Fprintf(virtx.w, "NAME\tOS\tVERSION\tCPU\tVENDOR\tMODEL\tNODES\tSOCKS\tCORES\tTH\tMEM_AVL_VM\tHPG_AVL_VM\tTSC_FREQ\tFWVER\tFWDATE\tSTATE\n")
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\t%s\t%s\t%5d\t%5d\t%5d\t%2d\t%7d MiB\t%7d MiB\t%d\t%s\t%s\t%s\n",
			host.Def.Name, host.Def.Osid, host.Def.Osv, host.Def.Cpuarch.Arch, host.Def.Cpuarch.Vendor,
			host.Def.Cpudef.Model, host.Def.Cpudef.Nodes, host.Def.Cpudef.Sockets, host.Def.Cpudef.Cores, host.Def.Cpudef.Threads,
			host.Resources.Memory.Availablevms, host.Resources.Hp.Availablevms,
			host.Def.Tscfreq, host.Def.Sysinfo.Version, host.Def.Sysinfo.Date, host.State,
		)
	}
}
