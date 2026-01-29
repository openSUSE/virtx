package main

import (
	"fmt"
)

func vm_migrate_abort_req(arg string) {
	virtx.path = fmt.Sprintf("/vms/%s/runstate/migrate", arg)
	virtx.method = "DELETE"
	virtx.arg = nil
	virtx.result = nil
}

func vm_migrate_abort() {
}
