package main

import (
	"fmt"
	"suse.com/virtx/pkg/model"
)

func vm_migrate_req(arg string) {
	if (virtx.live) {
		virtx.vm_migrate_options.MigrationType = openapi.MIGRATION_LIVE
	} else {
		virtx.vm_migrate_options.MigrationType = openapi.MIGRATION_COLD
	}
	virtx.path = fmt.Sprintf("/vms/%s/runstate/migrate", arg)
	virtx.method = "POST"
	virtx.arg = &virtx.vm_migrate_options
	virtx.result = nil
}

func vm_migrate() {
}
