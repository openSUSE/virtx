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
)

func vm_update_req(arg0 string, arg1 string) {
	read_json(arg1, &virtx.vm_update_options.Vmdef)
	virtx.path = fmt.Sprintf("/vms/%s", arg0)
	virtx.method = "PUT"
	virtx.arg = &virtx.vm_update_options
	virtx.result = nil
}

func vm_update() {
}
