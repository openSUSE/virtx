package main

import (
	"fmt"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/ts"
)

func host_list_req() {
	virtx.path = "/hosts"
	virtx.method = "GET"
	virtx.arg = &virtx.host_list_options
	virtx.result = &openapi.HostList{}
}

func host_list(list *openapi.HostList) {

	fmt.Fprintf(virtx.w, "UUID\tNAME\tOS\tVERSION\tCPU\tVENDOR\tMODEL\tTHREADS\t MEM_AVL_VM\t HPG_AVL_VM\tSTATE\tAGE\n")

	for _, item := range (list.Items) {
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%7d\t%7d MiB\t%7d MiB\t%s\t%s\n",
			item.Uuid, item.Fields.Name, item.Fields.Osid, item.Fields.Osv,
			item.Fields.Cpuarch.Arch, item.Fields.Cpuarch.Vendor, item.Fields.Cpudef.Model,
			item.Fields.Cpudef.Nodes * item.Fields.Cpudef.Sockets * item.Fields.Cpudef.Cores * item.Fields.Cpudef.Threads,
			item.Fields.Memoryavailable, item.Fields.Hpavailable,
			item.Fields.Hoststate, ts.Since(item.Fields.Ts))
	}
}
