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
	"suse.com/virtx/pkg/lockman"
	"suse.com/virtx/pkg/vmdef"
	"suse.com/virtx/pkg/logger"
)

type storage_ops struct {
	create func(disk *openapi.Disk, resource_name string, uuid string) error
	delete func(disk *openapi.Disk, resource_name string, uuid string) error
	detect func(disk *openapi.Disk) error
}

type created_resource struct {
	disk *openapi.Disk
	resource_name string
}
type CreatedResources []created_resource /* for rollback */

var storage_ops_map = map[openapi.DiskDevice]storage_ops{}

func Rollback(created CreatedResources, uuid string) {
	var (
		rerr error
		c created_resource
	)
	for _, c = range created {
		rerr = lockman.Delete_resource(c.resource_name, uuid)
		if (rerr != nil) {
			logger.Log("Rollback failed to delete resource %s: %s", c.resource_name, rerr.Error())
		}
	}
}

/*
 * Create the managed storage that is in the vm definition.
 * If the operation is an update, do not create a disk that was already present in the old definition
 */
func Create(vm *openapi.Vmdef, old *openapi.Vmdef, uuid string) (CreatedResources, error) {
	var (
		err error
		resource_name string
		created CreatedResources
	)
	for _, disk := range vmdef.Disks(vm) {
		if (old != nil && vmdef.Has_path(old, disk.Path)) {
			continue
		}
		if (storage_is_managed_disk(disk)) {
			resource_name = lockman.Get_resource_name(disk.Device, disk.Path)
			err = lockman.Create_resource(resource_name, uuid)
			if (err != nil) {
				return created, err
			}
			created = append(created, created_resource{ disk, resource_name })
		}
		if (storage_is_managed_disk(disk) && disk.Prov != openapi.DISK_PROV_NONE) {
			err = storage_create_disk(disk, resource_name, uuid)
		} else {
			err = Detect(disk)
		}
		if (err != nil) {
			return created, err
		}
	}
	return created, nil
}

/*
 * Delete the managed storage.
 * If the operation is an update, do not delete a disk that is present in the new definition
 */
func Delete(vm *openapi.Vmdef, new *openapi.Vmdef, uuid string, delete bool) error {
	var (
		first_err, err error
		resource_name string
	)
	for _, disk := range vmdef.Disks(vm) {
		if (new != nil && vmdef.Has_path(new, disk.Path)) {
			continue
		}
		if (!storage_is_managed_disk(disk)) {
			continue
		}
		resource_name = lockman.Get_resource_name(disk.Device, disk.Path)
		if (delete) {
			err = storage_delete_disk(disk, resource_name, uuid)
		} else { /* storage_delete_disk also takes care of the resource file */
			err = lockman.Delete_resource(resource_name, uuid)
		}
		if (err != nil) {
			logger.Log("Delete error: %s, uuid:%s", err, uuid)
			if (first_err == nil) {
				first_err = err
			}
		}
	}
	return first_err
}

func storage_is_managed_disk(disk *openapi.Disk) bool {
	return disk.Man != openapi.DISK_MAN_UNMANAGED
}

func storage_create_disk(disk *openapi.Disk, resource_name string, uuid string) error {
	ops, ok := storage_ops_map[disk.Device]
	if (!ok || ops.create == nil) {
		return errors.New("storage_create_disk: invalid disk device")
	}
	return ops.create(disk, resource_name, uuid)
}

/* detect and set disk provisioning method */
func Detect(disk *openapi.Disk) error {
	ops, ok := storage_ops_map[disk.Device]
	if (!ok || ops.detect == nil) {
		return errors.New("storage_detect: invalid disk device")
	}
	return ops.detect(disk)
}

func storage_delete_disk(disk *openapi.Disk, resource_name string, uuid string) error {
	ops, ok := storage_ops_map[disk.Device]
	if (!ok || ops.delete == nil) {
		return errors.New("storage_delete: invalid disk device")
	}
	return ops.delete(disk, resource_name, uuid)
}
