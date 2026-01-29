package main

import (
	"fmt"
)

func vm_register_req(arg string) {
	virtx.path = fmt.Sprintf("/vms/%s/register", arg)
	virtx.method = "PUT"
	virtx.arg = &virtx.vm_register_options
	virtx.result = nil
}

func vm_register() {
}
