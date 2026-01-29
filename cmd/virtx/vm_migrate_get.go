package main

import (
	"fmt"
	"suse.com/virtx/pkg/model"
)

func vm_migrate_get_req(arg string) {
	virtx.path = fmt.Sprintf("/vms/%s/runstate/migrate", arg)
	virtx.method = "GET"
	virtx.arg = nil
	virtx.result = &openapi.MigrationInfo{}
}

func vm_migrate_get(info *openapi.MigrationInfo) {
	var p *openapi.TransferProgress = &info.Progress
	fmt.Fprintf(virtx.w, "STATE\tRAM TOTAL\tTRANSFERRED\tREMAINING\tRATE\n")
	fmt.Fprintf(virtx.w, "%s\t%d\t%d\t%d\t%f\n", info.State,
		p.Total, p.Transferred, p.Remaining, p.Rate)
}
