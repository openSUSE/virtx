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
package storage

import (
	"errors"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/vmdef"
)

/*
 * Create the managed storage that is in the vm definition.
 * If the operation is an update, do not create a disk that was already present in the old definition
 */
func Create(vm *openapi.Vmdef, old *openapi.Vmdef) error {
	var err error
	for _, disk := range vmdef.Disks(vm) {
		if (old != nil && vmdef.Has_path(old, disk.Path)) {
			continue
		}
		if (storage_is_managed_disk(disk) && disk.Prov != openapi.DISK_PROV_NONE) {
			err = storage_create_disk(disk)
		} else {
			err = storage_detect_prov(disk)
		}
		if (err != nil) {
			return err
		}
	}
	return nil
}

/*
 * Delete the managed storage.
 * If the operation is an update, do not delete a disk that is present in the new definition
 */
func Delete(vm *openapi.Vmdef, new *openapi.Vmdef) error {
	var err error
	for _, disk := range vmdef.Disks(vm) {
		if (new != nil && vmdef.Has_path(new, disk.Path)) {
			continue
		}
		if (!storage_is_managed_disk(disk)) {
			continue
		}
		err = storage_delete_disk(disk)
		if (err != nil) {
			return err
		}
	}
	return nil
}

/* is this is a virtual disk managed by virtx, created using the API ? */
func storage_is_managed_disk(disk *openapi.Disk) bool {
	return disk.Device == openapi.DEVICE_DISK && disk.Man != openapi.DISK_MAN_UNMANAGED
}

func storage_create_disk(disk *openapi.Disk) error {
	switch (disk.Device) {
	case openapi.DEVICE_DISK:
		return vdisk_create(disk)
	default:
		return errors.New("storage_create_disk: invalid disk device")
	}
}

/* detect and set disk provisioning method */
func storage_detect_prov(disk *openapi.Disk) error {
	switch (disk.Device) {
	case openapi.DEVICE_LUN:
		/* not implemented yet */
	case openapi.DEVICE_DISK:
		fallthrough
	case openapi.DEVICE_CDROM:
		return vdisk_detect_prov(disk)
	default:
		return errors.New("storage_detect_prov: invalid disk device")
	}
	return nil
}

func storage_delete_disk(disk *openapi.Disk) error {
	switch (disk.Device) {
	case openapi.DEVICE_DISK:
		return vdisk_delete(disk)
	default:
		return errors.New("invalid disk device")
	}
}
