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

import (
	"errors"
)

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

func (device *DiskDevice) Parse(s string) error {
	switch (s) {
	case "cdrom":
		*device = DEVICE_CDROM
		return nil
	case "disk":
		*device = DEVICE_DISK
		return nil
	}
	return errors.New("could not parse disk device")
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

func (netmodel *NetModel) Parse(s string) error {
	switch (s) {
	case "virtio":
		*netmodel = NET_MODEL_VIRTIO
		return nil
	case "e1000e":
		*netmodel = NET_MODEL_E1000E
		return nil
	case "e1000":
		*netmodel = NET_MODEL_E1000
		return nil
	}
	return errors.New("could not parse net model")
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

func (firmware *FirmwareType) Parse(s string) error {
	switch (s) {
	case "bios":
		*firmware = FIRMWARE_BIOS
		return nil
	case "efi":
		*firmware = FIRMWARE_UEFI
		return nil
	}
	return errors.New("could not parse firmware type")
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

func (bus *DiskBus) Parse(ctrl_type string, ctrl_model string) error {
	switch (ctrl_type) {
	case "virtio":
		*bus = BUS_VIRTIO_BLK
		return nil
	case "sata":
		*bus = BUS_SATA
		return nil
	case "scsi":
		if (ctrl_model == "virtio-scsi") {
			*bus = BUS_VIRTIO_SCSI
		} else {
			*bus = BUS_SCSI
		}
		return nil
	}
	return errors.New("could not parse disk bus")
}

func (mode DiskCreateMode) String() string {
	switch (mode) {
	case DISK_NOCREATE:
		return "U"
	case DISK_CREATE_THIN:
		return "t"
	case DISK_CREATE_THICK:
		return "T"
	}
	return ""
}

func (mode *DiskCreateMode) Parse(c byte) error {
	switch (c) {
	case 'U':
		*mode = DISK_NOCREATE /* disk was not created via API, so Unknown */
		return nil
	case 't':
		*mode = DISK_CREATE_THIN
		return nil
	case 'T':
		*mode = DISK_CREATE_THICK
		return nil
	}
	return errors.New("could not parse disk creation mode")
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

func (state Hoststate) String() string {
	switch (state) {
	case HOST_INVALID:
		return "invalid"
	case HOST_ACTIVE:
		return "active"
	case HOST_EVICTING:
		return "evicting"
	case HOST_LEFT:
		return "left"
	case HOST_FAILED:
		return "failed"
	}
	return ""
}
