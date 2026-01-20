package virtx

import (
	"os"
	"os/exec"
	"path/filepath"
	"errors"
	"fmt"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/vmdef"
)

/* is this is a virtual disk managed by virtx, created using the API ? */
func vm_storage_is_managed_disk(disk *openapi.Disk) bool {
	return disk.Device == openapi.DEVICE_DISK && disk.Createmode != openapi.DISK_NOCREATE
}

func vm_storage_create_disk(disk *openapi.Disk) error {
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

func vm_storage_delete_disk(disk *openapi.Disk) error {
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

func vm_storage_create(vm *openapi.Vmdef) error {
	var err error
	for _, disk := range vmdef.Disks(vm) {
		if (vm_storage_is_managed_disk(&disk)) {
			err = vm_storage_create_disk(&disk)
			if (err != nil) {
				return err
			}
		}
	}
	return nil
}

func vm_storage_delete(vm *openapi.Vmdef) error {
	var err error
	for _, disk := range vmdef.Disks(vm) {
		if (vm_storage_is_managed_disk(&disk)) {
			err = vm_storage_delete_disk(&disk)
			if (err != nil) {
				return err
			}
		}
	}
	return nil
}

/* create the managed storage that is in the new definition and not in the old */
func vm_storage_update_create(new *openapi.Vmdef, old *openapi.Vmdef) error {
	var err error
	for _, disk := range vmdef.Disks(new) {
		if (vm_storage_is_managed_disk(&disk) && !vmdef.Has_path(old, disk.Path)) {
			err = vm_storage_create_disk(&disk)
			if (err != nil) {
				return err
			}
		}
	}
	return nil
}

/* delete the managed storage that is in the old definition and not in the new */
func vm_storage_update_delete(new *openapi.Vmdef, old *openapi.Vmdef) error {
	var err error
	for _, disk := range vmdef.Disks(old) {
		if (vm_storage_is_managed_disk(&disk) && !vmdef.Has_path(new, disk.Path)) {
			err = vm_storage_delete_disk(&disk)
			if (err != nil) {
				return err
			}
		}
	}
	return nil
}
