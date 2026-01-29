package main

import (
	"fmt"
)

func vm_delete_req(arg string) {
	virtx.path = fmt.Sprintf("/vms/%s", arg)
	virtx.method = "DELETE"
	virtx.arg = &virtx.vm_delete_options
	virtx.result = nil
}

func vm_delete() {
}
