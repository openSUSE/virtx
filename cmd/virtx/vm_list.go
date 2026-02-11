package main

import (
	"fmt"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/ts"
)

func vm_list_req() {
	virtx.path = "/vms"
	virtx.method = "GET"
	virtx.arg = &virtx.vm_list_options
	virtx.result = &openapi.VmList{}
}

func vm_list(list *openapi.VmList) {

	fmt.Fprintf(virtx.w, "UUID\tNAME\tHOST\tVLANID\t \tSTATE\tAGE\n")

	for _, item := range (list.Items) {
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%6d\t%v\t%s\t%s\n", item.Uuid, item.Fields.Name, item.Fields.Host,
			item.Fields.Vlanid, item.Fields.Custom, item.Fields.Runstate, ts.Since(item.Fields.Ts))
	}
}
