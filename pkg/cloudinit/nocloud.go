/*
 * Copyright (c) 2026 SUSE LLC
 *
 * This program is free software; you can redistribute it and/or
 * modify it under the terms of the GNU General Public License
 * as published by the Free Software Foundation; either version 2
 * of the License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, see
 * <https://www.gnu.org/licenses/>
 */

package cloudinit

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"suse.com/virtx/pkg/lockman"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/paths"
	"suse.com/virtx/pkg/storage"

	. "suse.com/virtx/pkg/constants"
)

/*
 * Stage CloudInit files from the input options into the Stage directory.
 * It stages currently up to 4 cloud-init files under stage_dir,
 * and returns the paths to the created files as a slice.
 */
func stage_files(ci []openapi.CloudInitOption, stage_dir string, vm_uuid string) ([]string, error) {
	var (
		err   error
		files []string
	)
	for _, name := range ci_names {
		value := find_option(ci, name)
		if (value == "" && name == "meta-data") {
			value = default_metadata(vm_uuid)
		}
		err = stage_ci_file(&files, name, value, stage_dir)
		if (err != nil) {
			return files, err
		}
	}
	return files, nil
}

func stage_ci_file(files *[]string, name string, value string, stage_dir string) error {
	var f string

	f = filepath.Join(stage_dir, name)
	if (value == "") {
		return nil
	}
	*files = append(*files, f)
	return os.WriteFile(f, []byte(value), 0640)
}

/*
 * Invokes xorrisofs to produce the ISO.
 */
func build_iso(iso_path string, stage_files []string) error {
	args := []string{
		"-output", iso_path,
		"-volid", "cidata",
		"-joliet",
		"-rock",
		"-quiet",
	}
	args = append(args, stage_files...)

	cmd := exec.Command(paths.Get("XORRISOFS"), args...)
	out, err := cmd.CombinedOutput()
	if (err != nil) {
		return fmt.Errorf("%s failed: %w\n%s", paths.Get("XORRISOFS"),
			err, strings.TrimSpace(string(out)))
	}
	return nil
}

/*
 * Create_disk creates a cloud-init NoCloud ISO and fills in the Disk descriptor.
 * The ISO is created at /vms/ds/ci/<uuid>/seed.iso.
 * Caller must have already called Validate_options.
 */
func Create_disk(disk *openapi.Disk, uuid string, ci []openapi.CloudInitOption) error {
	var (
		err       error
		stage_dir string
		files     []string
	)
	init_disk(disk, uuid)
	err = os.MkdirAll(filepath.Dir(disk.Path), 0750)
	if (err != nil) {
		return fmt.Errorf("creating directories for %s: %w", disk.Path, err)
	}
	stage_dir, err = os.MkdirTemp("", "virtx-ci-stage-*")
	if (err != nil) {
		return fmt.Errorf("creating stage dir: %w", err)
	}
	defer os.Remove(stage_dir)
	files, err = stage_files(ci, stage_dir, uuid)
	defer func() {
		for _, f := range files {
			os.Remove(f)
		}
	}()
	if (err != nil) {
		return fmt.Errorf("failed to stage files in %s: %w", stage_dir, err)
	}
	resource_name := lockman.Get_resource_name(disk.Device, disk.Path)
	err = lockman.Create_resource(resource_name, uuid)
	if (err != nil) {
		return err
	}
	err = build_iso(disk.Path, files)
	if (err != nil) {
		lockman.Delete_resource(resource_name, uuid)
		return err
	}
	err = storage.Detect(disk)
	if (err != nil) {
		logger.Log("failed to detect prov and size of %s: %s", disk.Path, err)
	}
	return nil
}

/*
 * Delete_disk removes the resource lease for a cloud-init ISO.
 * The seed.iso itself is left in place.
 */
func Delete_disk(uuid string) error {
	var (
		resource_name, resource_path string
		disk                         openapi.Disk
		err                          error
	)
	init_disk(&disk, uuid)
	resource_name = lockman.Get_resource_name(disk.Device, disk.Path)
	resource_path = lockman.Get_resource_path(resource_name)

	_, err = os.Stat(resource_path)
	if (err != nil) {
		if (errors.Is(err, os.ErrNotExist)) {
			return nil
		}
		return fmt.Errorf("cannot stat %s: %w", resource_path, err)
	}
	err = lockman.Delete_resource(resource_name, uuid)
	if (err != nil) {
		return fmt.Errorf("failed to delete %s: %w", resource_path, err)
	}
	return nil
}

func init_disk(disk *openapi.Disk, uuid string) {
	*disk = openapi.Disk{
		Path:   fmt.Sprintf(CI_DIR+"%s/seed.iso", uuid),
		Device: openapi.DEVICE_CDROM,
		Bus:    openapi.BUS_VIRTIO_SCSI,
		Man:    openapi.DISK_MAN_MANAGED,
		Prov:   openapi.DISK_PROV_NONE,
		Size:   0,
	}
}
