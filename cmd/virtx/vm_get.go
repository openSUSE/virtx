package main

import (
	"fmt"
	"suse.com/virtx/pkg/model"
)

func vm_get(vm *openapi.Vm) {
	if (virtx.stat) {
		fmt.Fprintf(virtx.w, "VCPUS\t CPU%%\t    MEM_CAP\t   MEM_USED\t   DISK_CAP\t DISK_ALLOC\t  DISK_PHYS\t NETWORK_RX\t NETWORK_TX\n")
		fmt.Fprintf(virtx.w, "%5d\t%5d\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d MiB\t%7d KiB\t%7d KiB\n",
			vm.Stats.Vcpus, vm.Stats.CpuUtilization,
			vm.Stats.MemoryCapacity, vm.Stats.MemoryUsed,
			vm.Stats.DiskCapacity, vm.Stats.DiskAllocation, vm.Stats.DiskPhysical,
			vm.Stats.NetRxBw, vm.Stats.NetTxBw,
		)
	} else if (virtx.disk) {
		fmt.Fprintf(virtx.w, "PATH\tDEVICE\tBUS\tMODE\n")
		vm_get_disk(&vm.Def.Osdisk);
		for _, disk := range (vm.Def.Disks) {
			vm_get_disk(&disk)
		}
	} else {
		fmt.Fprintf(virtx.w, "NAME\tHOST\tSTATE\tVLAN\tCPU_MODEL\tVCPUS\t \n")
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%4d\t%s\t%5d\t%v\n",
			vm.Def.Name, vm.Runinfo.Host, vm.Runinfo.Runstate, vm.Def.Vlanid,
			vm.Def.Cpudef.Model, vm.Stats.Vcpus, vm.Def.Custom)
	}
}

func vm_get_disk(disk *openapi.Disk) {
	fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\n", disk.Path, disk.Device, disk.Bus, disk.Createmode)
}
