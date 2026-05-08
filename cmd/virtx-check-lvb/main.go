/*
 * Copyright (c) 2026 SUSE LLC
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
	"os"

	"suse.com/virtx/pkg/lockman"
)

/*
 * /usr/sbin/virtx-check-lvb resource_path vm_uuid
 *
 * This command reads the resource lock file LVB sector,
 * and compares it with the passed vm_uuid,
 * to ensure that the resource belongs to the specified VM.
 * It returns 0 if and only if the LVB == vm_uuid
 */
func main() {
	if (len(os.Args) != 3) {
		os.Exit(1)
	}
	resource_path := os.Args[1]
	uuid := os.Args[2]
	if (len(uuid) < 1) {
		os.Exit(1)
	}
	code := check_lvb(resource_path, uuid)
	os.Exit(code)
}

func check_lvb(resource_path string, uuid string) int {
	var (
		err error
		lvb string
	)
	lvb, err = lockman.Read_lvb(resource_path)
	if (err != nil) {
		return 1
	}
	if (uuid != lvb) {
		return 1
	}
	/* LVB checks ok! */
	return 0
}
