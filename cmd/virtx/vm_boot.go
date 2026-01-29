package main

import (
	"fmt"
)

func vm_boot_req(arg string) {
	virtx.path = fmt.Sprintf("/vms/%s/runstate/boot", arg)
	virtx.method = "POST"
	virtx.arg = nil
	virtx.result = nil
}

func vm_boot() {
}
