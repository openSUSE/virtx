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
	"fmt"
	"os"
	"strings"
	"path/filepath"
	. "suse.com/virtx/pkg/constants"
)

func reg_cpumodels(host_uuid string) string {
	return fmt.Sprintf("%s/%s/%s", REG_DIR, host_uuid, "cpumodels")
}

func Save_cpumodels(host_uuid string, models []string) error {
	var (
		err error
		filename string
	)
	filename = reg_cpumodels(host_uuid)
	err = os.WriteFile(filename, []byte(strings.Join(models, "\n") + "\n"), 0640)
	if (err != nil) {
		return err
	}
	return reg_syncdir(filepath.Dir(filename))
}

func Load_cpumodels(host_uuid string) ([]string, error) {
	var (
		err error
		data []byte
		models []string
	)
	data, err = os.ReadFile(reg_cpumodels(host_uuid))
	if (err != nil) {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if (line != "") {
			models = append(models, line)
		}
	}
	return models, nil
}
