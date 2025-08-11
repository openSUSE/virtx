package main

import (
	"fmt"
	"suse.com/virtx/pkg/model"
)

func vm_list(list *openapi.VmList) {

	fmt.Fprintf(virtx.w, "UUID\tNAME\tHOST\tSTATE\tVLANID\t \n")

	for _, item := range (list.Items) {
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\t%6d\t%v\n", item.Uuid, item.Fields.Name, item.Fields.Host,
			item.Fields.Runstate, item.Fields.Vlanid, item.Fields.Custom)
	}
}
