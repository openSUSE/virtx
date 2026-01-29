package main

import (
	"fmt"
)

func vm_create_req(arg string) {
	read_json(arg, &virtx.vm_create_options.Vmdef)
	virtx.path = "/vms"
	virtx.method = "POST"
	virtx.arg = &virtx.vm_create_options
	virtx.result = new(string)
}

func vm_create(uuid *string) {
	fmt.Fprintf(virtx.w, "UUID\n")
	fmt.Fprintf(virtx.w, "%s\n", *uuid)
}
