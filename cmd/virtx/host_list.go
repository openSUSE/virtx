package main

import (
	"fmt"
	"suse.com/virtx/pkg/model"
)

func host_list(list *openapi.HostList) {

	fmt.Fprintf(virtx.w, "UUID\tNAME\tCPU\tVENDOR\tMODEL\tSOCKETS\t  CORES\tTHREADS\tSTATE\tMEM_AVL_VM\n")

	for _, item := range (list.Items) {
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\t%s\t%7d\t%7d\t%7d\t%s\t%7d MB\n", item.Uuid, item.Fields.Name,
			item.Fields.Cpuarch.Arch, item.Fields.Cpuarch.Vendor,
			item.Fields.Cpudef.Model, item.Fields.Cpudef.Sockets, item.Fields.Cpudef.Cores, item.Fields.Cpudef.Threads,
			item.Fields.Hoststate.String(), item.Fields.Memoryavailable)
	}
}
