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
	"os"
	"fmt"
	"strconv"
	"strings"
	"path/filepath"
	"errors"
	"syscall"
	"unsafe"
	"golang.org/x/sys/unix"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/vmdef"
	"suse.com/virtx/pkg/lockman"
	"suse.com/virtx/pkg/paths"

	. "suse.com/virtx/pkg/constants"
)

const (
	DISCARD_PATH = "/sys/block/%s/queue/discard_max_bytes"
)

func lun_create(disk *openapi.Disk, resource_name string, uuid string) error {
	var (
		err error
		clone_args [][]string
	)
	disk_driver := vmdef.Validate_disk_path(disk.Path)
	if (disk_driver != "raw") {
		return errors.New("invalid Disk Path")
	}
	if (disk.Source != "") {
		size, err := lun_detect_size(disk.Path)
		if (err != nil) {
			return err
		}
		clone_args, err = lun_clone_args(disk, size)
		if (err != nil) {
			return err
		}
	}
	args, err := lun_discard_args(disk.Path)
	if (err != nil) {
		return err
	}
	args = append(args, clone_args...)
	return lockman.Run(resource_name, uuid, args, false)
}

func lun_clone_args(disk *openapi.Disk, size int64) ([][]string, error) {
	var (
		err error
	)
	source_driver := vmdef.Validate_disk_source(disk.Source)
	if (source_driver == "") {
		return nil, errors.New("invalid Disk Source")
	}
	vsize, err := vdisk_detect_vsize(disk.Source, source_driver)
	if (err != nil) {
		return nil, err
	}
	if (size < vsize) {
		return nil, fmt.Errorf("disk Size is smaller than Source size")
	}
	args := [][]string{
		{
			paths.Get("QEMU_IMG"), "convert", "-n", "-t", "none", "-W", "-m", "8",
			"-f", source_driver, "-O", "raw", disk.Source, disk.Path,
		},
	}
	return args, nil
}

func lun_delete(disk *openapi.Disk, resource_name string, uuid string) error {
	var (
		err error
		args [][]string
	)
	disk_driver := vmdef.Validate_disk_path(disk.Path)
	if (disk_driver != "raw") {
		return errors.New("invalid Disk Path")
	}
	args, err = lun_discard_args(disk.Path)
	if (err != nil) {
		return err
	}
	resource_path := lockman.Get_resource_path(resource_name)
	args = append(args,
		[]string{ "/usr/bin/rm", "--", resource_path },
		[]string{ "/usr/bin/rmdir", "--", filepath.Dir(resource_path) },
	)
	return lockman.Run(resource_name, uuid, args, true)
}

/* return the blkdiscard or dd command appropriate for this device */
func lun_discard_args(path string) ([][]string, error) {
	var (
		raw []byte
		err error
		dev, discard_path string
		i int
	)
	dev, err = filepath.EvalSymlinks(path)
	if (err != nil) {
		return nil, fmt.Errorf("could not eval symlink: %w", err)
	}
	dev = strings.TrimPrefix(dev, "/dev")
	discard_path = fmt.Sprintf(DISCARD_PATH, dev)
	raw, err = os.ReadFile(discard_path)
	if (err != nil) {
		return nil, fmt.Errorf("could not read %s: %w", discard_path, err)
	}
	i, err = strconv.Atoi(strings.TrimSpace(string(raw)))
	if (err != nil) {
		return nil, fmt.Errorf("failed to parse %s: %w", discard_path, err)
	}
	args := [][]string{
		{ paths.Get("WIPEFS"), "-a", path },
	}
	if (i > 0) {
		args = append(args, []string{ paths.Get("BLKDISCARD"), path })
	} else {
		args = append(args, []string{ "/usr/bin/dd", "if=/dev/zero", "of=" + path, "bs=1M", "count=1" })
	}
	return args, nil
}

/* detect and set disk provisioning method and virtual size */
func lun_detect(disk *openapi.Disk) error {
	var (
		err error
		size int64
	)
	size, err = lun_detect_size(disk.Path)
	if (err != nil) {
		return err
	}
	disk.Size = int32(size / MiB)
	/* XXX we cannot know THIN vs THICK, depends on that the storage product is doing XXX */
	disk.Prov = openapi.DISK_PROV_THIN
	return nil
}

func lun_detect_size(path string) (int64, error) {
	var (
		err error
		size uint64
		errno syscall.Errno
	)
	f, err := os.Open(path)
	if (err != nil) {
		return 0, err
	}
	defer f.Close()
	/*
	 * XXX Golang is missing unix.IoctlGetUint64()
	 * see https://github.com/golang/go/issues/77311
	 */
	_, _, errno = unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(f.Fd()),
		uintptr(unix.BLKGETSIZE64),
		uintptr(unsafe.Pointer(&size)),
	)
	if (errno != 0) {
		return 0, errno
	}
	return int64(size), nil
}

func init() {
	storage_ops_map[openapi.DEVICE_LUN] = storage_ops{
		create: lun_create,
		delete: lun_delete,
		detect: lun_detect,
	}
}
