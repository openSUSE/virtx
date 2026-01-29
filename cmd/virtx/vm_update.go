package main

import (
	"fmt"
)

func vm_update_req(arg0 string, arg1 string) {
	read_json(arg1, &virtx.vm_update_options.Vmdef)
	virtx.path = fmt.Sprintf("/vms/%s", arg0)
	virtx.method = "PUT"
	virtx.arg = &virtx.vm_update_options
	virtx.result = nil
}

func vm_update() {
}
