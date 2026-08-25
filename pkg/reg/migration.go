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
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, see
 * <https://www.gnu.org/licenses/>
 */

package reg

import (
	"os"
	"errors"
	"strings"
	. "suse.com/virtx/pkg/constants"
)

/* Load the cluster-wide migration network (CIDR subnet). Returns empty string if not configured. */
func Load_migration_network() (string, error) {
	var (
		err error
		data []byte
		s string
	)
	data, err = os.ReadFile(REG_DIR + "migration_network")
	if (err != nil) {
		if (errors.Is(err, os.ErrNotExist)) {
			/* not configured, it's ok */
			return "", nil
		}
		return "", err
	}
	s = strings.TrimSpace(string(data))
	return s, nil
}
