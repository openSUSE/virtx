/*
 * Copyright (c) 2024-2026 SUSE LLC
 *
 * This program is free software; you can redistribute it and/or
 * modify it under the terms of the GNU General Public License
 * as published by the Free Software Foundation; either version 2
 * of the License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, see
 * <https://www.gnu.org/licenses/>
 */

package main

import (
	"fmt"
	"os"

	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
)

func vm_boot_req(arg string, ud_path string, md_path string, nc_path string) {
	virtx.path = fmt.Sprintf("/vms/%s/runstate/boot", arg)
	virtx.method = "POST"
	virtx.arg = &virtx.vm_boot_options
	virtx.result = nil

	read_opt("ci-userdata", ud_path)
	read_opt("ci-metadata", md_path)
	read_opt("ci-networkconfig", nc_path)
}

func vm_boot() {
}

func read_opt(name string, path string) {
	if (path != "") {
		data, err := os.ReadFile(path)
		if (err != nil) {
			logger.Fatal("%s: %v\n", name, err)
		}
		virtx.vm_boot_options.CloudInit = append(virtx.vm_boot_options.CloudInit,
			openapi.CloudInitOption{ Name: name, Value: string(data) },
		)
	}
}
