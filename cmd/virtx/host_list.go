package main

import (
	"fmt"
	"suse.com/virtx/pkg/model"
)

func host_list(list *openapi.HostList) {

	fmt.Fprintf(virtx.w, "UUID\tNAME\tCPU\tVENDOR\tMODEL\tNODES\tSOCKS\tCORES\tTH\tSTATE\t MEM_AVL_VM\n")

	for _, item := range (list.Items) {
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\t%s\t%5d\t%5d\t%5d\t%2d\t%s\t%7d MiB\n", item.Uuid, item.Fields.Name,
			item.Fields.Cpuarch.Arch, item.Fields.Cpuarch.Vendor, item.Fields.Cpudef.Model,
			item.Fields.Cpudef.Nodes, item.Fields.Cpudef.Sockets, item.Fields.Cpudef.Cores, item.Fields.Cpudef.Threads,
			item.Fields.Hoststate, item.Fields.Memoryavailable)
	}
}
