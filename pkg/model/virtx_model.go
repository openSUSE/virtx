/*
 * Copyright (c) 2024-2025 SUSE LLC
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
package openapi

/* some adjustments to the model for virtx */

func (device DiskDevice) String() string {
	switch (device) {
	case DEVICE_DISK:
		return "disk"
	case DEVICE_CDROM:
		return "cdrom"
	}
	return ""
}

func (nettype NetType) String() string {
	switch (nettype) {
	case NET_BRIDGE:
		return "bridge"
	case NET_LIBVIRT:
		return "network"
	}
	return ""
}

func (netmodel NetModel) String() string {
	switch (netmodel) {
	case NET_MODEL_VIRTIO:
		return "virtio"
	case NET_MODEL_E1000E:
		return "e1000e"
	case NET_MODEL_E1000:
		return "e1000"
	}
	return ""
}

func (firmware FirmwareType) String() string {
	switch (firmware) {
	case FIRMWARE_BIOS:
		return "bios"
	case FIRMWARE_UEFI:
		return "efi"
	}
	return ""
}

func (firmware FirmwareType) Machine() string {
	switch (firmware) {
	case FIRMWARE_BIOS:
		return "pc"
	case FIRMWARE_UEFI:
		return "q35"
	}
	return ""
}

func custom_isalnum(s string) bool {
	for _, c := range s {
		if (c >= 'A' && c <= 'Z') {
			continue
		}
		if (c >= 'a' && c <= 'z') {
			continue
		}
		if (c >= '0' && c <= '9') {
			continue
		}
		if (c == '_') {
			continue
		}
		return false
	}
	return true
}

func (custom CustomField) IsAlnum() bool {
	return custom_isalnum(custom.Name) && custom_isalnum(custom.Value)
}
