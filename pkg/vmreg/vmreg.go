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

package vmreg

import (
	"fmt"
	"os"
	"path/filepath"
	. "suse.com/virtx/pkg/constants"
)

/*
 * get the path of the actual processed xml file in shared storage registered for this VM
 */
func reg_file(host_uuid string, vm_uuid string) string {
	return fmt.Sprintf("%s/%s/%s.xml", REG_DIR, host_uuid, vm_uuid)
}

func reg_syncdir(dirname string) error {
	dir, err := os.Open(dirname)
	if (err != nil) {
		return err
	}
	err = dir.Sync()
	if (err != nil) {
		dir.Close()
		return err
	}
	err = dir.Close()
	if (err != nil) {
		return err
	}
	return nil
}

func Load(host_uuid string, vm_uuid string) (string, error) {
	var (
		err error
		data []byte
	)
	data, err = os.ReadFile(reg_file(host_uuid, vm_uuid))
	if (err != nil) {
		return "", err
	}
	return string(data), nil
}

/*
 * we split into subdirs to avoid bottlenecks with a single directory
 * containing a large number of files in NFS.
 * We try to atomically replace any preexisting file, to avoid corruption.
 */
func Save(host_uuid string, vm_uuid string, xml string) error {
	var (
		err error
		tmp *os.File
		tmpname, dirname, filename string
	)
	/* target file for the save */
	filename = reg_file(host_uuid, vm_uuid)
	dirname = filepath.Dir(filename)
	/* create temporary file */
	tmp, err = os.CreateTemp(dirname, fmt.Sprintf("%s.tmp-*", vm_uuid))
	tmpname = tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpname)
	}()
	/* write the data, sync, close, set permissions */
	_, err = tmp.Write([]byte(xml))
	if (err != nil) {
		return err
	}
	err = tmp.Sync()
	if (err != nil) {
		return err
	}
	err = tmp.Close()
	if (err != nil) {
		return err
	}
	err = os.Chmod(tmpname, 0640)
	if (err != nil) {
		return err
	}
	/* now try the atomic rename */
	err = os.Rename(tmpname, filename)
	if (err != nil) {
		return err
	}
	err = reg_syncdir(dirname)
	if (err != nil) {
		return err
	}
	return nil
}

func Delete(host_uuid string, vm_uuid string) error {
	var (
		err error
		filename string
	)
	filename = reg_file(host_uuid, vm_uuid)
	err = os.Remove(filename)
	if (err != nil) {
		return err
	}
	err = reg_syncdir(filepath.Dir(filename))
	if (err != nil) {
		return err
	}
	return nil
}
