package main

import (
	"fmt"
)

func vm_pause_req(arg string) {
	virtx.path = fmt.Sprintf("/vms/%s/runstate/pause", arg)
	virtx.method = "POST"
	virtx.arg = nil
	virtx.result = nil
}

func vm_pause() {
}
