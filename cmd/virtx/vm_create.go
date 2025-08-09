package main

import (
	"fmt"
)

func vm_create(uuid *string) {
	fmt.Fprintf(virtx.w, "UUID\n")
	fmt.Fprintf(virtx.w, "%s\n", *uuid)
}
