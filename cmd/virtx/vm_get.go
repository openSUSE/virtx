package main

import (
	"fmt"
	"suse.com/virtx/pkg/model"
)

func vm_get(vm *openapi.Vm) {
	if (virtx.disk) {
		fmt.Fprintf(virtx.w, "PATH\tDEVICE\tBUS\tMODE\n")
		vm_get_disk(&vm.Def.Osdisk);
		for _, disk := range (vm.Def.Disks) {
			vm_get_disk(&disk)
		}
	} else if (virtx.net) {
		fmt.Fprintf(virtx.w, "NAME\tTYPE\tMODEL\tMAC\n")
		for _, net := range (vm.Def.Nets) {
			vm_get_net(&net)
		}
	} else if (virtx.stat_disk) {
		fmt.Fprintf(virtx.w, "DISK_CAP\t DISK_ALLOC\t  DISK_PHYS\n")
		fmt.Fprintf(virtx.w, "%7d MiB\t%7d MiB\t%7d MiB\n",
			vm.Stats.DiskCapacity, vm.Stats.DiskAllocation, vm.Stats.DiskPhysical,
		)
	} else if (virtx.stat_net) {
		fmt.Fprintf(virtx.w, "NETWORK_RX\t NETWORK_TX\n")
		fmt.Fprintf(virtx.w, "%7d KiB\t%7d KiB\n", vm.Stats.NetRxBw, vm.Stats.NetTxBw)

	} else if (virtx.stat_cpu) {
		fmt.Fprintf(virtx.w, "VCPU MODEL\tSOCKETS\t CORES\tTHREADS\t CPU%%\n")
		fmt.Fprintf(virtx.w, "%s\t%7d\t%7d\t%7d\t%5d\n", vm.Def.Cpudef.Model,
			vm.Def.Cpudef.Sockets, vm.Def.Cpudef.Cores, vm.Def.Cpudef.Threads, vm.Stats.CpuUtilization)
	} else if (virtx.stat_mem) {
		if (vm.Def.Memory.Hp) {
			fmt.Fprintf(virtx.w, " MEM_CAP_HP\t    USED_HP\t  USED_NORM\n")
			fmt.Fprintf(virtx.w, "%7d MiB\t%7d MiB\t%7d MiB\n",
				vm.Stats.MemoryCapacity, vm.Stats.MemoryCapacity, vm.Stats.MemoryUsed)
		} else {
			fmt.Fprintf(virtx.w, "    MEM_CAP\t   MEM_USED\n")
			fmt.Fprintf(virtx.w, "%7d MiB\t%7d MiB\n",
				vm.Stats.MemoryCapacity, vm.Stats.MemoryUsed)
		}
	} else {
		fmt.Fprintf(virtx.w, "NAME\tHOST\tSTATE\tVLAN\tCUSTOM\n")
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%4d\t%v\n",
			vm.Def.Name, vm.Runinfo.Host, vm.Runinfo.Runstate, vm.Def.Vlanid, vm.Def.Custom)
	}
}

func vm_get_disk(disk *openapi.Disk) {
	fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\n", disk.Path, disk.Device, disk.Bus, disk.Createmode)
}

func vm_get_net(net *openapi.Net) {
	fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\n", net.Name, net.Nettype, net.Model, net.Mac)
}
