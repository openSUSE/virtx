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
	"errors"
	"path/filepath"
	. "suse.com/virtx/pkg/constants"
)

func reg_lockid(host_uuid string) string {
	return fmt.Sprintf("%s/%s/%s", REG_DIR, host_uuid, "lockid")
}

/* Save the sanlock host_id; we call it "lockid" here to avoid semantic clashes */
func Save_lockid(host_uuid string, lockid uint16) error {
	var (
		err error
		dirname, filename, value string
	)
	/* target file for the save */
	filename = reg_lockid(host_uuid)
	dirname = filepath.Dir(filename)
	value = fmt.Sprintf("%d\n", lockid)
	err = os.WriteFile(filename, []byte(value), 0640)
	if (err != nil) {
		return err
	}
	err = reg_syncdir(dirname)
	if (err != nil) {
		return err
	}
	return nil
}

/* Load the sanlock host_id; we call it "lockid" here to avoid semantic clashes */
func Load_lockid(host_uuid string, lockid *uint16) error {
	var (
		err error
		data []byte
		value uint16
	)
	data, err = os.ReadFile(reg_lockid(host_uuid))
	if (err != nil) {
		return err
	}
	n, err := fmt.Sscanf(string(data), "%d", &value)
	if (err != nil) {
		return err
	}
	if (n != 1) {
		return errors.New("failed to convert lockid argument")
	}
	*lockid = value
	return nil
}
