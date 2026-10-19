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
 * <https://www.gnu.org/licenses/>.
 */
package reg

import (
	"fmt"
	"os"

	. "suse.com/virtx/pkg/constants"
)

/* Hastate_file returns the path of a host's self report. */
func Hastate_file(host_uuid string) string {
	return fmt.Sprintf("%s/%s", reg_dir(host_uuid), HASTATE)
}

/*
 * Save_hastate atomically replaces a host's self report: a reader on another
 * host sees either the old file or the new one, never a partial one. Only the
 * host itself writes its own file.
 */
func Save_hastate(host_uuid string, data []byte) error {
	return reg_save_file(Hastate_file(host_uuid), data)
}

/*
 * Load_hastate reads a host's self report. A missing file is reported as
 * os.ErrNotExist, which the caller treats as "this host never wrote one".
 */
func Load_hastate(host_uuid string) ([]byte, error) {
	return os.ReadFile(Hastate_file(host_uuid))
}
