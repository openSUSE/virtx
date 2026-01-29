package main

import (
	"fmt"
)

func vm_shutdown_req(arg string) {
	virtx.vm_shutdown_options.Force = int16(virtx.force)
	virtx.path = fmt.Sprintf("/vms/%s/runstate/boot", arg)
	virtx.method = "DELETE"
	virtx.arg = &virtx.vm_shutdown_options
	virtx.result = nil
}

func vm_shutdown() {
}
