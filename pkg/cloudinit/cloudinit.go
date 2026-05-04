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
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, see
 * <https://www.gnu.org/licenses/>
 *
 * Package cloudinit provides support for creating NoCloud datasource ISOs
 * for use with cloud-init enabled VM images.
 *
 * The NoCloud datasource is described at:
 * https://cloudinit.readthedocs.io/en/latest/reference/datasources/nocloud.html
 *
 * The generated ISO has volume label "cidata" and contains:
 *   - meta-data  (auto-generated or caller-supplied)
 *   - user-data  (caller-supplied, or a minimal placeholder)
 *   - network-config (caller-supplied, optional)
 *
 * It is created using xorrisofs.
 */
package cloudinit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"errors"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/storage"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/lockman"
	. "suse.com/virtx/pkg/constants"
)

/*
 * Options holds the cloud-init file contents for a single boot.
 * Field names match the generated model.CloudInitOptions struct so that
 * callers can copy fields directly without translation.
 * At least one of UserData or NetworkConfig must be non-empty.
 */
type Options struct {
	/* cloud-init user-data file contents. May be empty if NetworkConfig is set. */
	UserData string
	/* cloud-init meta-data file contents. Minimal content auto-generated if empty */
	MetaData string
	/* cloud-init network-config file contents (v1 or v2). May be empty. */
	NetworkConfig string
}

func (o *Options) Validate() error {
	if (o == nil || !(o.UserData != "" || o.NetworkConfig != "")) {
		return fmt.Errorf("at least one of user_data or network_config must be provided")
	}
	return nil
}

/*
 * Stage CloudInit files from the input options into the Stage directory.
 * It stages currently up to 3 cloud-init files under stage_dir,
 * and returns the paths to the created files as a slice.
 */
func stage_files(o *Options, stage_dir string, vm_uuid string) ([]string, error) {
	var (
		err error
		files []string
	)
	err = o.Validate()
	if (err != nil) {
		return files, err
	}
	err = stage_ci_file(&files, "meta-data", o.MetaData, stage_dir, fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", vm_uuid, vm_uuid))
	if (err != nil) {
		return files, err
	}
	err = stage_ci_file(&files, "user-data", o.UserData, stage_dir, "")
	if (err != nil) {
		return files, err
	}
	err = stage_ci_file(&files, "network-config", o.NetworkConfig, stage_dir, "")
	if (err != nil) {
		return files, err
	}
	return files, err
}

func stage_ci_file(files *[]string, name string, value string, stage_dir string, def string) error {
	var f string

	f = filepath.Join(stage_dir, name)
	if (value == "") {
		value = def
	}
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
	var (
		err error
	)
	args := []string{
		"-output", iso_path,
		"-volid", "cidata",
		"-joliet",
		"-rock",
		"-quiet",
	}
	args = append(args, stage_files...)

	cmd := exec.Command("/usr/bin/xorrisofs", args...)
	out, err := cmd.CombinedOutput()
	if (err != nil) {
		return fmt.Errorf("/usr/bin/xorrisofs failed: %w\n%s",
			err, strings.TrimSpace(string(out)))
	}
	return nil
}

/*
 * Create a cloud init Disk. The passed pointer to the disk is filled,
 * and the function returns success if the ISO has been successfully
 * created, failure otherwise.
 */
func Create_disk(disk *openapi.Disk, uuid string, opts *Options) error {
	var (
		err error
		stage_dir string  /* temporary staging directory in /tmp */
		files []string    /* files to be staged in stage_dir */
	)
	init_disk(disk, uuid)
	err = opts.Validate()
	if (err != nil) {
		return err
	}
	/* we will create the ISO as /vms/ds/ci/<vm_uuid>/seed.iso */
	err = os.MkdirAll(filepath.Dir(disk.Path), 0750)
	if (err != nil) {
		return fmt.Errorf("creating directories for %s: %w", disk.Path, err)
	}
	/* create the staging directory in /tmp */
	stage_dir, err = os.MkdirTemp("", "virtx-ci-stage-*")
	if (err != nil) {
		return fmt.Errorf("creating stage dir: %w", err)
	}
	defer os.Remove(stage_dir)
	/* create and stage all files requested in the options */
	files, err = stage_files(opts, stage_dir, uuid)
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
		/* emit just a warning */
		logger.Log("failed to detect prov and size of %s: %w", disk.Path, err)
	}
	return nil
}

/*
 * Delete a cloud init Disk. Remove only the resource lease file for now, not the seed.iso.
 * Removing the seed.iso creates additional headaches, so leave it in place for now.
 */
func Delete_disk(uuid string) error {
	var (
		resource_name, resource_path string
		disk openapi.Disk
		err error
	)
	init_disk(&disk, uuid)
	resource_name = lockman.Get_resource_name(disk.Device, disk.Path)
	resource_path = lockman.Get_resource_path(resource_name)

	_, err = os.Stat(resource_path)
	if (err != nil) {
		if (errors.Is(err, os.ErrNotExist)) {
			/* no such resource, nothing to do */
			return nil
		}
		/* for some other reason we cannot stat resource_path */
		return fmt.Errorf("cannot stat %s: %w", resource_path, err)
	}
	/* ready to destroy */
	err = lockman.Delete_resource(resource_name, uuid)
	if (err != nil) {
		return fmt.Errorf("failed to delete %s: %w", resource_path, err)
	}
	return nil
}

func init_disk(disk *openapi.Disk, uuid string) {
	*disk = openapi.Disk{
		Path: fmt.Sprintf(CI_DIR + "%s/seed.iso", uuid),
		Device: openapi.DEVICE_CDROM,
		Bus: openapi.BUS_VIRTIO_SCSI,
		Man: openapi.DISK_MAN_MANAGED,
		Prov: openapi.DISK_PROV_NONE, /* to be detected */
		Size: 0,                      /* to be detected */
	}
}
