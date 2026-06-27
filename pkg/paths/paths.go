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
package paths

import (
	"path/filepath"

	"golang.org/x/sys/unix"
	"suse.com/virtx/pkg/logger"
)

var search_dirs = []string{
	"/usr/sbin", "/usr/bin",
	"/sbin", "/bin",
}

var prog_paths = map[string]string{
	"SANLOCK":         "/usr/sbin/sanlock",
	"WIPEFS":          "/usr/sbin/wipefs",
	"BLKDISCARD":      "/usr/sbin/blkdiscard",
	"QEMU_IMG":        "/usr/bin/qemu-img",
	"XORRISOFS":       "/usr/bin/xorrisofs",
	"VIRTX_CHECK_LVB": "/usr/sbin/virtx-check-lvb",
}

func Init() {
	for key, default_path := range prog_paths {
		var (
			name string = filepath.Base(default_path)
			err  error
			found bool
		)
		for _, dir := range search_dirs {
			candidate := filepath.Join(dir, name)
			err = unix.Access(candidate, unix.X_OK)
			if (err == nil) {
				prog_paths[key] = candidate
				found = true
				break
			}
		}
		if (!found) {
			logger.Fatal("required binary not found: %s", name)
		}
	}
}

func Get(name string) string {
	return prog_paths[name]
}
