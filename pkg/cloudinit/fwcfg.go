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
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, see
 * <https://www.gnu.org/licenses/>
 */

package cloudinit

import (
	"suse.com/virtx/pkg/model"
)

/* fw_cfg entry namespace for the QemuFwCfg cloud-init datasource. */
const FWCFG_PREFIX = "opt/io.cloud-init/cloud-init"

/*
 * FwCfgSlot is a single fw_cfg entry: the full entry name and raw content.
 */
type FwCfgSlot struct {
	Name    string
	Content []byte
}

/*
 * Create_fwcfg_slots returns the fw_cfg entries to inject for the given options.
 * Slots are returned in the order cloud-init processes them:
 * meta-data, network-config, user-data, vendor-data.
 * A minimal meta-data is generated from vm_uuid when not provided.
 * Caller must have already called Validate_options.
 */
func Create_fwcfg_slots(ci []openapi.CloudInitOption, vm_uuid string) []FwCfgSlot {
	var slots []FwCfgSlot

	for _, name := range ci_names {
		value := find_option(ci, name)
		if (value == "" && name == "meta-data") {
			value = default_metadata(vm_uuid)
		}
		if (value == "") {
			continue
		}
		slots = append(slots, FwCfgSlot{
			Name:    FWCFG_PREFIX + "/" + name,
			Content: []byte(value),
		})
	}
	return slots
}
