package main

import (
	"fmt"
	"suse.com/virtx/pkg/model"
)

func vm_runstate_get(runinfo *openapi.Vmruninfo) {
	fmt.Fprintf(virtx.w, "HOST\tSTATE\n")
	fmt.Fprintf(virtx.w, "%s\t%s\n", runinfo.Host, runinfo.Runstate)
}
