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

package reg

import (
	"fmt"
	"os"
	"path/filepath"
	. "suse.com/virtx/pkg/constants"
)

func reg_claim(host_uuid string, vm_uuid string) string {
	return fmt.Sprintf("%s/%s/%s/claim", REG_DIR, host_uuid, vm_uuid)
}

/*
 * Save_claim records that this host has claimed a VM from another host, and from
 * which one. The record lives inside the VM's own registration directory, so it
 * travels with the move and is read by whoever holds the registration next -
 * which is what makes a claim survive a change of coordinator. It is removed once
 * the VM runs here.
 */
func Save_claim(host_uuid string, vm_uuid string, from_host string) error {
	var (
		filename string
		err error
	)
	filename = reg_claim(host_uuid, vm_uuid)
	err = os.WriteFile(filename, []byte(from_host + "\n"), 0640)
	if (err != nil) {
		return err
	}
	return nil
}

/* Load_claim answers which host this VM was claimed from, "" when it was not. */
func Load_claim(host_uuid string, vm_uuid string) (string, error) {
	var (
		data []byte
		err error
		text string
	)
	data, err = os.ReadFile(reg_claim(host_uuid, vm_uuid))
	if (err != nil) {
		return "", err
	}
	text = string(data)
	for len(text) > 0 && (text[len(text) - 1] == '\n' || text[len(text) - 1] == '\r') {
		text = text[:len(text) - 1]
	}
	return text, nil
}

/* Clear_claim removes the record: the VM of that claim runs here now. */
func Clear_claim(host_uuid string, vm_uuid string) error {
	var (
		filename string
	)
	filename = reg_claim(host_uuid, vm_uuid)
	if (filepath.Dir(filename) == "") {
		return nil
	}
	return os.Remove(filename)
}
