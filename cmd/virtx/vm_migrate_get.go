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
