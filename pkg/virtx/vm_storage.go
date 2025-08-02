package virtx

import (
	"os"
	"os/exec"
	"path/filepath"
	"errors"
	"strings"
	"fmt"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	. "suse.com/virtx/pkg/constants"
)


func vm_storage_create_disk(disk *openapi.Disk) error {
	var (
		err error
		path, disk_driver, prealloc string
	)
	path = filepath.Clean(disk.Path)
	disk_driver = disk_driver_from_path(path)

	if (path != disk.Path || !strings.HasPrefix(disk.Path, DS_DIR) ||
		(disk_driver != "qcow2" && disk_driver != "raw")) {
		/* symlink shenanigans, or not starting with /vms/ or invalid ext : bail */
		return errors.New("invalid Disk Path")
	}
	prealloc = func () string {
		if (disk_driver == "qcow2") {
			if (disk.Createmode == openapi.DISK_CREATE_THIN) {
				return "metadata"
			} else {
				return "falloc"
			}
		} else if (disk.Createmode == openapi.DISK_CREATE_THIN) {
			return "off"
		} else {
			return "falloc"
		}
	}()
	args := []string { "create", "-f", disk_driver, "-o", "preallocation=" + prealloc }
	if (disk_driver == "qcow2") {
		args = append(args, "-o", "lazy_refcounts=off")
	}
	args = append(args, path, fmt.Sprintf("%dM", disk.Size))
	logger.Log("qemu-img %v", args)
	var cmd *exec.Cmd = exec.Command("/usr/bin/qemu-img", args...)
	var output []byte
	output, err = cmd.CombinedOutput()
	if (err != nil) {
		logger.Log("%s\n", string(output))
		return err
	}
	return nil
}

func vm_storage_delete_disk(disk *openapi.Disk) error {
	var (
		err error
		path, disk_driver string
	)
	path = filepath.Clean(disk.Path)
	disk_driver = disk_driver_from_path(path)
	if (path != disk.Path || !strings.HasPrefix(disk.Path, DS_DIR) ||
		(disk_driver != "qcow2" && disk_driver != "raw")) {
		/* symlink shenanigans, or not starting with /vms/ or invalid ext : bail */
		return errors.New("invalid Disk Path")
	}
	logger.Log("deleting %s", path)
	err = os.Remove(path)
	if (err != nil) {
		return err
	}
	return nil
}

func vm_storage_create(vmdef *openapi.Vmdef) error {
	var err error
	for _, disk := range vmdef.Disks {
		/* ignore anything that is not a virtual disk to create */
		if (disk.Device != openapi.DEVICE_DISK || disk.Createmode == openapi.DISK_NOCREATE) {
			continue;
		}
		err = vm_storage_create_disk(&disk)
		if (err != nil) {
			return err
		}
	}
	return nil
}

func vm_storage_delete(vmdef *openapi.Vmdef) error {
	var err error
	for _, disk := range vmdef.Disks {
		/* ignore anything that is not a virtual disk to delete */
		if (disk.Device != openapi.DEVICE_DISK) {
			continue;
		}
		err = vm_storage_delete_disk(&disk)
		if (err != nil) {
			return err
		}
	}
	return nil
}

func vm_storage_update(vmdef *openapi.Vmdef, old *openapi.Vmdef) error {
	var err error
	for _, disk := range vmdef.Disks {
		/* ignore anything that is not a virtual disk to create */
		if (disk.Device != openapi.DEVICE_DISK || disk.Createmode == openapi.DISK_NOCREATE) {
			continue;
		}
		if (vmdef_has_path(old, disk.Path)) {
			/* already in the previous definition, not new */
			continue
		}
		err = vm_storage_create_disk(&disk)
		if (err != nil) {
			return err
		}
	}
	return nil
}
