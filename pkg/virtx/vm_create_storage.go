package virtx

import (
	"os/exec"
	"path/filepath"
	"errors"
	"strings"
	"fmt"

	"suse.com/virtx/pkg/model"
)

func vm_create_storage(vmdef *openapi.Vmdef) error {
	var err error
	for _, disk := range vmdef.Disks {
		/* ignore anything that is not a virtual disk to create */
		if (disk.Device != openapi.DEVICE_DISK || disk.Createmode == openapi.DISK_NOCREATE) {
			continue;
		}
		var path string = filepath.Clean(disk.Path)
		/* XXX disable for now
		path, err = filepath.EvalSymlinks(path)
		if (err != nil) {
			return errors.New("invalid Disk Path")
		}
		*/
		var disk_driver string = disk_driver_from_path(path)
		if (path != disk.Path || !strings.HasPrefix(disk.Path, VMS_DIR) ||
			(disk_driver != "qcow2" && disk_driver != "raw")) {
			/* symlink shenanigans, or not starting with /vms/ or invalid ext : bail */
			return errors.New("invalid Disk Path")
		}
		var prealloc string = func () string {
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
		var cmd *exec.Cmd = exec.Command("/usr/bin/qemu-img", args...)
		err = cmd.Run()
		if (err != nil) {
			return err
		}
	}
	return nil
}
