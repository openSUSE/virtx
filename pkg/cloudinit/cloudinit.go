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
 *
 * Package cloudinit provides support for cloud-init datasource delivery.
 *
 * user-data is MANDATORY. meta-data is auto-generated if absent.
 * network-config and vendor-data are optional.
 */
package cloudinit

import (
	"fmt"

	"suse.com/virtx/pkg/model"
)

/* ci_names lists the valid cloud-init file slot option names, in the order
 * they are processed by cloud-init. */
var ci_names = []string{"meta-data", "network-config", "user-data", "vendor-data"}

func find_option(ci []openapi.CloudInitOption, name string) string {
	for _, opt := range ci {
		if (opt.Name == name) {
			return opt.Value
		}
	}
	return ""
}

func validate_option_name(opt openapi.CloudInitOption) error {
	for _, name := range ci_names {
		if (opt.Name == name) {
			return nil
		}
	}
	return fmt.Errorf("unknown cloud-init option: %s", opt.Name)
}

/*
 * Validate_options checks that the cloud-init options are valid.
 * user-data is required; meta-data is auto-generated if absent.
 * Validation is method-agnostic; the method itself is validated by the
 * JSON deserializer (CloudInitMethod.UnmarshalJSON).
 */
func Validate_options(ci []openapi.CloudInitOption) error {
	var (
		err error
		has_ud bool
	)
	for _, opt := range ci {
		err = validate_option_name(opt)
		if (err != nil) {
			return err
		}
		if (opt.Name == "user-data") {
			has_ud = true
		}
	}
	if (!has_ud) {
		return fmt.Errorf("user-data is required")
	}
	return nil
}

/* default_metadata returns a minimal meta-data string for the given VM UUID. */
func default_metadata(vm_uuid string) string {
	return fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", vm_uuid, vm_uuid)
}
