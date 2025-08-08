package main

import (
	"fmt"
	"suse.com/virtx/pkg/model"
)

func vm_get(vm *openapi.Vm) {

	fmt.Fprintf(virtx.w, "NAME\tHOST\tSTATE\tVLAN\t \t" +
		"CPU MODEL\tVCPUS\t CPU%%\t" +
		"   MEM_CAP\t  MEM_USED\t  DISK_CAP\tDISK_ALLOC\t DISK_PHYS\tNETWORK RX\tNETWORK TX\n")

	fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%4d\t%v\t" +
		"%s\t%5d\t%5d\t" +
		"%7d MB\t%7d MB\t%7d MB\t%7d MB\t%7d MB\t%7d KB\t%7d KB\n",
		vm.Def.Name, vm.Runinfo.Host, vm.Runinfo.Runstate, vm.Def.Vlanid, vm.Def.Custom,
		vm.Def.Cpudef.Model, vm.Stats.Vcpus, vm.Stats.CpuUtilization,
		vm.Stats.MemoryCapacity, vm.Stats.MemoryUsed, vm.Stats.DiskCapacity, vm.Stats.DiskAllocation, vm.Stats.DiskPhysical, vm.Stats.NetRxBw, vm.Stats.NetTxBw)
}
