/*
 * Copyright (c) 2025-2026 SUSE LLC
 *
 * This program is free software; you can redistribute it and/or
 * modify it under the terms of the GNU General Public License
 * as published by the Free Software Foundation; either version 2
 * of the License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, see
 * <https://www.gnu.org/licenses/>
 */

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
