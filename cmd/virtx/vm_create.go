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
