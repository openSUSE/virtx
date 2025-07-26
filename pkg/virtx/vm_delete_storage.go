package virtx

import (
	"os"
	"path/filepath"
	"errors"
	"strings"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
)

func vm_delete_storage(vmdef *openapi.Vmdef) error {
	var err error
	for _, disk := range vmdef.Disks {
		/* ignore anything that is not a virtual disk to delete */
		if (disk.Device != openapi.DEVICE_DISK) {
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
		logger.Log("deleting %s", path)
		err = os.Remove(path)
		if (err != nil) {
			return err
		}
	}
	return nil
}
