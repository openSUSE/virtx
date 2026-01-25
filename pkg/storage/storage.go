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
	"os"
	"os/exec"
	"path/filepath"
	"errors"
	"fmt"
	"encoding/json"
	"bytes"
	"golang.org/x/sys/unix"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/vmdef"
	. "suse.com/virtx/pkg/constants"
)

/*
 * Create the managed storage that is in the vm definition.
 * If the operation is an update, do not create a disk that was already present in the old definition
 */
func Create(vm *openapi.Vmdef, old *openapi.Vmdef) error {
	var err error
	for _, disk := range vmdef.Disks(vm) {
		if (!storage_is_managed_disk(disk)) {
			continue
		}
		if (old != nil && vmdef.Has_path(old, disk.Path)) {
			continue
		}
		if (disk.Prov == openapi.DISK_PROV_NONE) {
			err = storage_detect_prov(disk)
		} else {
			err = storage_create_disk(disk)
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
		if (!storage_is_managed_disk(disk)) {
			continue
		}
		if (new != nil && vmdef.Has_path(new, disk.Path)) {
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
	var (
		err error
		disk_driver, prealloc string
	)
	disk_driver = vmdef.Validate_disk_path(disk.Path)
	if (disk_driver == "") {
		return errors.New("invalid Disk Path")
	}
	err = os.MkdirAll(filepath.Dir(disk.Path), 0750)
	if (err != nil) {
		return errors.New("could not create path")
	}
	prealloc = func () string {
		if (disk_driver == "qcow2") {
			if (disk.Prov == openapi.DISK_PROV_THIN) {
				return "metadata"
			} else {
				return "falloc"
			}
		} else if (disk.Prov == openapi.DISK_PROV_THIN) {
			return "off"
		} else {
			return "falloc"
		}
	}()
	args := []string { "create", "-f", disk_driver, "-o", "preallocation=" + prealloc }
	if (disk_driver == "qcow2") {
		args = append(args, "-o", "lazy_refcounts=off")
	}
	args = append(args, disk.Path, fmt.Sprintf("%dM", disk.Size))
	logger.Debug("qemu-img %v", args)
	var cmd *exec.Cmd = exec.Command("/usr/bin/qemu-img", args...)
	var output []byte
	output, err = cmd.CombinedOutput()
	if (err != nil) {
		logger.Log("%s\n", string(output))
		return err
	}
	return nil
}

func storage_detect_raw_prov(path string) (openapi.DiskProvMode, int32, error) {
	var (
		err error
		stat unix.Stat_t
		prov openapi.DiskProvMode
	)
	err = unix.Lstat(path, &stat)
	if (err != nil) {
		return openapi.DISK_PROV_NONE, 0, err
	}
	if (stat.Blocks * 512 < stat.Size) {
		prov = openapi.DISK_PROV_THIN
	} else {
		prov = openapi.DISK_PROV_THICK
	}
	return prov, int32(stat.Size / MiB), nil
}

type qmap struct {
	//Start      uint64 `json:"start"`
	Length     uint64 `json:"length"`
	//Depth      int    `json:"depth"`
	//Present    bool   `json:"present"`
	Zero       bool   `json:"zero"`
	//Data       bool   `json:"data"`
	Compressed bool   `json:"compressed"`
	//Offset     uint64 `json:"offset"`
}

func storage_detect_qcow2_prov(path string) (openapi.DiskProvMode, int32, error) {
	var (
		err error
		prov openapi.DiskProvMode
		virtual_size uint64
		qmaps []qmap
	)
	args := []string { "map", "--output=json", path }
	logger.Debug("qemu-img %v", args)
	var cmd *exec.Cmd = exec.Command("/usr/bin/qemu-img", args...)
	var output []byte
	output, err = cmd.CombinedOutput()
	if (err != nil) {
		logger.Log("%s\n", string(output))
		return openapi.DISK_PROV_NONE, 0, err
	}
	err = json.NewDecoder(bytes.NewReader(output)).Decode(&qmaps)
	if (err != nil) {
		return openapi.DISK_PROV_NONE, 0, err
	}
	if (len(qmaps) == 0) {
		return openapi.DISK_PROV_NONE, 0, errors.New("image contains no extents")
	}
	prov = openapi.DISK_PROV_THICK
	for _, qmap := range qmaps {
		if (qmap.Compressed) {
			return openapi.DISK_PROV_NONE, 0, errors.New("unsupported compressed qcow2")
		}
		virtual_size += qmap.Length
		if (qmap.Zero) {
			prov = openapi.DISK_PROV_THIN
		}
	}
	if (virtual_size <= 0) {
		return openapi.DISK_PROV_NONE, 0, errors.New("invalid virtual size")
	}
	return prov, int32(virtual_size / MiB), nil
}

/* detect and set disk provisioning method */
func storage_detect_prov(disk *openapi.Disk) error {
	var (
		err error
		disk_driver string
	)
	disk_driver = vmdef.Validate_disk_path(disk.Path)
	if (disk_driver == "") {
		return errors.New("invalid Disk Path")
	}
	switch (disk_driver) {
	case "raw":
		disk.Prov, disk.Size, err = storage_detect_raw_prov(disk.Path)
	case "qcow2":
		disk.Prov, disk.Size, err = storage_detect_qcow2_prov(disk.Path)
	default:
		return errors.New("invalid Disk Path")
	}
	return err
}

func storage_delete_disk(disk *openapi.Disk) error {
	var (
		err error
		disk_driver string
	)
	disk_driver = vmdef.Validate_disk_path(disk.Path)
	if (disk_driver == "") {
		return errors.New("invalid Disk Path")
	}
	logger.Debug("deleting %s", disk.Path)
	err = os.Remove(disk.Path)
	if (err != nil) {
		return err
	}
	return nil
}
