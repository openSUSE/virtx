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
	case DEVICE_LUN:
		return "lun"
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
	case "lun":
		*device = DEVICE_LUN
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

func (bus DiskBus) String() string {
	switch (bus) {
	case BUS_VIRTIO_BLK:
		return "virtio"
	case BUS_SATA:
		return "sata"
	case BUS_VIRTIO_SCSI:
		return "virtio-scsi"
	case BUS_SCSI:
		return "scsi"
	}
	return ""
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

func (state Vmrunstate) String() string {
	switch (state) {
	case RUNSTATE_NONE:
		return "none"
	case RUNSTATE_DELETED:
		return "deleted"
	case RUNSTATE_POWEROFF:
		return "poweroff"
	case RUNSTATE_STARTUP:
		return "startup"
	case RUNSTATE_RUNNING:
		return "running"
	case RUNSTATE_PAUSED:
		return "paused"
	case RUNSTATE_MIGRATING:
		return "migrating"
	case RUNSTATE_TERMINATING:
		return "terminating"
	case RUNSTATE_PMSUSPENDED:
		return "pmsuspended"
	case RUNSTATE_CRASHED:
		return "crashed"
	}
	return ""
}

func (state *MigrationState) Parse(s string) error {
	switch (s) {
	case "none":
		*state = MIGRATION_NONE /* no migration in progress */
		return nil
	case "setup":
		*state = MIGRATION_SETUP
		return nil
	case "cancelling":
		*state = MIGRATION_CANCELLING
		return nil
	case "cancelled":
		*state = MIGRATION_CANCELLED
		return nil
	case "active":
		*state = MIGRATION_ACTIVE
		return nil
	case "completed":
		*state = MIGRATION_COMPLETED
		return nil
	case "failed":
		*state = MIGRATION_FAILED
		return nil
	case "pre-switchover":
		*state = MIGRATION_PRESWITCH
		return nil
	case "device":
		*state = MIGRATION_DEVICE
		return nil
	case "wait-unplug":
		*state = MIGRATION_WAIT_UNPLUG
		return nil
	}
	return errors.New("unknown migration state")
}

func (state MigrationState) String() string {
	switch (state) {
	case MIGRATION_NONE: /* no migration in progress */
		return "none"
	case MIGRATION_SETUP:
		return "setup"
	case MIGRATION_CANCELLING:
		return "cancelling"
	case MIGRATION_CANCELLED:
		return "cancelled"
	case MIGRATION_ACTIVE:
		return "active"
	case MIGRATION_COMPLETED:
		return "completed"
	case MIGRATION_FAILED:
		return "failed"
	case MIGRATION_PRESWITCH:
		return "pre-switchover"
	case MIGRATION_DEVICE:
		return "device"
	case MIGRATION_WAIT_UNPLUG:
		return "wait-unplug"
	}
	return ""
}

func (state OperationState) String() string {
	switch (state) {
	case OPERATION_STARTED:
		return "started"
	case OPERATION_FAILED:
		return "failed"
	case OPERATION_COMPLETED:
		return "completed"
	}
	return ""
}

func (state *OperationState) Parse(s string) error {
	switch (s) {
	case "started":
		*state = OPERATION_STARTED
		return nil
	case "failed":
		*state = OPERATION_FAILED
		return nil
	case "completed":
		*state = OPERATION_COMPLETED
		return nil
	}
	return errors.New("unknown operation state")
}

func (o Operation) String() string {
	return OperationToString[o]
}

func (o *Operation) Parse(s string) error {
	var present bool
	*o, present = OperationFromString[s]
	if (!present) {
		return errors.New("unknown operation")
	}
	return nil
}
