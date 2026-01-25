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
	. "suse.com/virtx/pkg/constants"
)

const (
	DISCARD_PATH = "/sys/block/%s/queue/discard_max_bytes"
)

func lun_wipe(path string, resource_name string, uuid string, delete bool) error {
	var (
		raw []byte
		err error
		dev, discard_path, resource_path string
		i int
	)
	resource_path = lockman.Get_resource_path(resource_name)
	args := [][]string{
		{ "/usr/sbin/wipefs", "-a", path },
	}
	/* check if device supports blkdiscard, and if so add blkdiscard to the commands, else dd */
	dev, err = filepath.EvalSymlinks(path)
	if (err != nil) {
		return fmt.Errorf("could not eval symlink: %w", err)
	}
	dev = strings.TrimPrefix(dev, "/dev")
	discard_path = fmt.Sprintf(DISCARD_PATH, dev)
	raw, err = os.ReadFile(discard_path)
	if (err != nil) {
		return fmt.Errorf("could not read %s: %w", discard_path, err)
	}
	i, err = strconv.Atoi(strings.TrimSpace(string(raw)))
	if (err != nil) {
		return fmt.Errorf("failed to parse %s: %w", discard_path, err)
	}
	if (i > 0) {
		args = append(args, []string{ "/usr/sbin/blkdiscard", path })
	} else {
		args = append(args, []string{ "/usr/bin/dd", "if=/dev/zero", "of=" + path, "bs=1M", "count=1" })
	}
	if (delete) {
		args = append(args, []string{ "/usr/bin/rm", "--", resource_path })
		args = append(args, []string{ "/usr/bin/rmdir", "--", filepath.Dir(resource_path) })
	}
	/* run under lease lock */
	return lockman.Run(resource_name, uuid, args, delete)
}

func lun_create(disk *openapi.Disk, resource_name string, uuid string) error {
	var (
		err error
	)
	disk_driver := vmdef.Validate_disk_path(disk.Path)
	if (disk_driver != "raw") {
		return errors.New("invalid Disk Path")
	}
	/* wipe existing signatures, zero 1MB of data and blkdiscard, to avoid booting from previously stored data */
	err = lun_wipe(disk.Path, resource_name, uuid, false)
	return err
}

func lun_delete(disk *openapi.Disk, resource_name string, uuid string) error {
	var (
		err error
	)
	disk_driver := vmdef.Validate_disk_path(disk.Path)
	if (disk_driver != "raw") {
		return errors.New("invalid Disk Path")
	}
	/* clear the disk and remove resource lock */
	err = lun_wipe(disk.Path, resource_name, uuid, true)
	return err
}

/* detect and set disk provisioning method and virtual size */
func lun_detect_prov(disk *openapi.Disk) error {
	var (
		err error
		size uint64
		errno syscall.Errno
	)
	f, err := os.Open(disk.Path)
	if (err != nil) {
		return err
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
		return errno
	}
	disk.Size = int32(size / MiB)
	/* XXX we cannot know THIN vs THICK, depends on that the storage product is doing XXX */
	disk.Prov = openapi.DISK_PROV_THIN
	return nil
}
