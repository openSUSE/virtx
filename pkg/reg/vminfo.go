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
	"encoding/json"
	"fmt"
	"os"
	"suse.com/virtx/pkg/inventory"
	. "suse.com/virtx/pkg/constants"
)

/* VmInfo of all VMs of a host. Ts is the host VI_ts when written. */
type reg_vminfo_file struct {
	Ts int64 `json:"ts"`
	Vms []inventory.VmInfo `json:"vms"`
}

func reg_vminfo(host_uuid string) string {
	return fmt.Sprintf("%s/%s/%s", REG_DIR, host_uuid, "vminfo")
}

/* the host directory must already exist (see Save_cpumodels) */
func Save_vminfo(host_uuid string, ts int64, vms []inventory.VmInfo) error {
	var (
		err error
		data []byte
	)
	data, err = json.MarshalIndent(reg_vminfo_file{ ts, vms }, "", "\t")
	if (err != nil) {
		return err
	}
	return reg_save_file(reg_vminfo(host_uuid), append(data, '\n'))
}

func Load_vminfo(host_uuid string) (int64, []inventory.VmInfo, error) {
	var (
		err error
		data []byte
		f reg_vminfo_file
	)
	data, err = os.ReadFile(reg_vminfo(host_uuid))
	if (err != nil) {
		return 0, nil, err
	}
	err = json.Unmarshal(data, &f)
	if (err != nil) {
		return 0, nil, err
	}
	return f.Ts, f.Vms, nil
}
