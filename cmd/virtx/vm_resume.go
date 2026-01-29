package main

import (
	"fmt"
)

func vm_resume_req(arg string) {
	virtx.path = fmt.Sprintf("/vms/%s/runstate/pause", arg)
	virtx.method = "DELETE"
	virtx.arg = nil
	virtx.result = nil
}

func vm_resume() {
}
