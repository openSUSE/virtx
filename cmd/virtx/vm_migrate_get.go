package main

import (
	"fmt"

	"suse.com/virtx/pkg/model"
)

func vm_migrate_get(info *openapi.MigrationInfo) {
	var p *openapi.TransferProgress = &info.Progress
	fmt.Fprintf(virtx.w, "STATE\tRAM TOTAL\tTRANSFERRED\tREMAINING\tRATE\n")
	fmt.Fprintf(virtx.w, "%s\t%d\t%d\t%d\t%f\n", info.State,
		p.Total, p.Transferred, p.Remaining, p.Rate)
}
