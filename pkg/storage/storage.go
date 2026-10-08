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
package storage

import (
	"errors"
	"fmt"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/lockman"
	"suse.com/virtx/pkg/vmdef"
	"suse.com/virtx/pkg/logger"
)

type storage_ops struct {
	create func(disk *openapi.Disk, resource_name string, uuid string) error
	delete func(disk *openapi.Disk, resource_name string, uuid string) error
	detect func(disk *openapi.Disk) error
	detect_size func(disk *openapi.Disk) error
	resize func(disk *openapi.Disk, old *openapi.Disk, resource_name string, uuid string) error /* grow to disk.Size */
}

type created_resource struct {
	disk *openapi.Disk
	resource_name string
	provisioned bool /* created by this specific operation: delete on rollback */
}
type CreatedResources []created_resource /* for rollback */

var storage_ops_map = map[openapi.DiskDevice]storage_ops{}

func Rollback(created CreatedResources, uuid string) {
	var (
		rerr error
		c created_resource
	)
	for _, c = range created {
		if (c.provisioned) {
			rerr = storage_delete_disk(c.disk, c.resource_name, uuid)
		} else {
			rerr = lockman.Delete_resource(c.resource_name, uuid)
		}
		if (rerr != nil) {
			logger.Log("Rollback failed to delete resource %s: %s", c.resource_name, rerr.Error())
		}
	}
}

/*
 * Prepare a vm update: for the disks already present in the old definition,
 * inherit the osdisk Source, inherit Prov when it is set as NONE,
 * and detect the current size of those requesting a Size, so that old describes the actual storage.
 * Must be called before vmdef.Diff and Create. Does not modify any storage.
 */
func Prepare_update(vm *openapi.Vmdef, old *openapi.Vmdef) error {
	var (
		err error
		old_disk *openapi.Disk
	)
	for _, disk := range vmdef.Disks(vm) {
		old_disk = vmdef.Find_disk(old, disk.Path)
		if (old_disk == nil) {
			continue
		}
		/* Source describes the osdisk only: a disk demoted to data disk loses it */
		if (disk == &vm.Osdisk && disk.Source == "") {
			disk.Source = old_disk.Source
		}
		if (disk.Prov == openapi.DISK_PROV_NONE) { /* NONE means keep the current provisioning mode */
			disk.Prov = old_disk.Prov
		}
		if (disk.Size != 0) { /* 0 means keep the current size */
			err = Detect_size(old_disk)
			if (err != nil) {
				return fmt.Errorf("disk %s: %w", disk.Path, err)
			}
		}
	}
	return nil
}

/*
 * Validate a vm update: check that the changes to the disks already present in the old definition
 * are supported. Must be called after Prepare_update. Does not modify any storage.
 */
func Validate_update(vm *openapi.Vmdef, old *openapi.Vmdef) error {
	var (
		err error
		old_disk *openapi.Disk
	)
	for _, disk := range vmdef.Disks(vm) {
		old_disk = vmdef.Find_disk(old, disk.Path)
		if (old_disk == nil) {
			continue
		}
		err = storage_validate_update(disk, old_disk, disk == &vm.Osdisk)
		if (err != nil) {
			return fmt.Errorf("disk %s: %w", disk.Path, err)
		}
	}
	return nil
}

/*
 * check that the update of a disk already present in the old definition is supported.
 * osdisk tells if disk is the osdisk in the new definition.
 */
func storage_validate_update(disk *openapi.Disk, old *openapi.Disk, osdisk bool) error {
	/* Bus can change freely, it only affects the domain XML and not the storage */
	if (disk.Device != old.Device) {
		return errors.New("changing the device type is not supported")
	}
	if (disk.Man != old.Man) {
		return errors.New("changing the management mode is not supported")
	}
	if (disk.Prov != old.Prov) {
		return errors.New("changing the provisioning mode is not supported")
	}
	/* Source describes the osdisk only, a data disk has none (see Validate) */
	if (osdisk && disk.Source != old.Source) {
		return errors.New("changing the source is not supported")
	}
	if (disk.Size == 0 || disk.Size == old.Size) {
		return nil
	}
	if (disk.Size < old.Size) {
		return errors.New("shrinking a disk is not supported")
	}
	if (!storage_is_managed_disk(old)) {
		return errors.New("cannot resize an unmanaged disk")
	}
	ops, ok := storage_ops_map[disk.Device]
	if (!ok || ops.resize == nil) {
		return errors.New("resize not supported for this disk device")
	}
	return nil
}

/*
 * Create the managed storage that is in the vm definition.
 * If the operation is an update (old passed through Prepare_update and Validate_update),
 * do not create the disks already present in the old definition, but grow them if a bigger size
 * is requested. Resizing comes last, since unlike creation it cannot be rolled back.
 */
func Create(vm *openapi.Vmdef, old *openapi.Vmdef, uuid string) (CreatedResources, error) {
	var (
		err error
		resource_name string
		created CreatedResources
		old_disk *openapi.Disk
	)
	for _, disk := range vmdef.Disks(vm) {
		if (old != nil && vmdef.Find_disk(old, disk.Path) != nil) {
			continue    /* skip disks that are in both old and new vmdef */
		}
		/*
		 * NOTE! this is a valid DISK_PROV_NONE check only because old disks are skipped
		 * above, and Detect() has not run yet. Prepare_update() and Detect() overwrite
		 * Prov with the old and detected value respectively.
		 */
		provisioned := storage_is_managed_disk(disk) && disk.Prov != openapi.DISK_PROV_NONE
		if (storage_is_managed_disk(disk)) {
			resource_name = lockman.Get_resource_name(disk.Device, disk.Path)
			err = lockman.Create_resource(resource_name, uuid)
			if (err != nil) {
				return created, err
			}
			created = append(created, created_resource{ disk, resource_name, provisioned })
		}
		if (provisioned) {
			err = storage_create_disk(disk, resource_name, uuid)
		} else {
			err = Detect(disk)
		}
		if (err != nil) {
			return created, fmt.Errorf("disk %s: %w", disk.Path, err)
		}
	}
	if (old != nil) {
		for _, disk := range vmdef.Disks(vm) {
			old_disk = vmdef.Find_disk(old, disk.Path)
			if (old_disk == nil || disk.Size <= old_disk.Size) {
				continue
			}
			err = storage_resize(disk, old_disk, uuid)
			if (err != nil) {
				return created, fmt.Errorf("disk %s: %w", disk.Path, err)
			}
		}
	}
	return created, nil
}

/*
 * Check all the storage that is in the vm definition, to ensure paths are accessible,
 * and resources exist and are owned by the vm. Does not modify any storage:
 * a missing resource is an error, its repair requires deciding the owner of the disk.
 */
func Check(vm *openapi.Vmdef, uuid string) error {
	var (
		err error
		resource_name string
	)
	for _, disk := range vmdef.Disks(vm) {
		if (storage_is_managed_disk(disk)) {
			resource_name = lockman.Get_resource_name(disk.Device, disk.Path)
			err = lockman.Check_resource(resource_name, uuid)
			if (err != nil) {
				return fmt.Errorf("disk %s: %w", disk.Path, err)
			}
		}
		err = Detect(disk)
		if (err != nil) {
			return fmt.Errorf("disk %s: %w", disk.Path, err)
		}
	}
	return nil
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
		if (new != nil && vmdef.Find_disk(new, disk.Path) != nil) {
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

/* detect and set disk provisioning method and size */
func Detect(disk *openapi.Disk) error {
	ops, ok := storage_ops_map[disk.Device]
	if (!ok || ops.detect == nil) {
		return errors.New("Detect: invalid disk device")
	}
	return ops.detect(disk)
}

/* detect size only */
func Detect_size(disk *openapi.Disk) error {
	ops, ok := storage_ops_map[disk.Device]
	if (!ok || ops.detect_size == nil) {
		return errors.New("Detect_size: invalid disk device")
	}
	return ops.detect_size(disk)
}

/*
 * grow an existing disk to disk.Size. old is the disk as it is in the current definition,
 * with the size detected by Prepare_update. The change must be checked by Validate_update.
 */
func storage_resize(disk *openapi.Disk, old *openapi.Disk, uuid string) error {
	ops, ok := storage_ops_map[disk.Device]
	if (!ok || ops.resize == nil) {
		return errors.New("storage_resize: resize not supported for this disk device")
	}
	return ops.resize(disk, old, lockman.Get_resource_name(disk.Device, disk.Path), uuid)
}

func storage_delete_disk(disk *openapi.Disk, resource_name string, uuid string) error {
	ops, ok := storage_ops_map[disk.Device]
	if (!ok || ops.delete == nil) {
		return errors.New("storage_delete_disk: invalid disk device")
	}
	return ops.delete(disk, resource_name, uuid)
}
