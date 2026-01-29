package main

import (
	"fmt"
	"suse.com/virtx/pkg/model"
)

func vm_runstate_get_req(arg string) {
	virtx.path = fmt.Sprintf("/vms/%s/runstate", arg)
	virtx.method = "GET"
	virtx.arg = nil
	virtx.result = &openapi.Vmruninfo{}
}

func vm_runstate_get(runinfo *openapi.Vmruninfo) {
	fmt.Fprintf(virtx.w, "HOST\tSTATE\n")
	fmt.Fprintf(virtx.w, "%s\t%s\n", runinfo.Host, runinfo.Runstate)
}
