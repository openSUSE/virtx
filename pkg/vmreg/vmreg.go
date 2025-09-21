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

func reg_dir(host_uuid string) string {
	return fmt.Sprintf("%s/%s", REG_DIR, host_uuid)
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

/*
 * We try to atomically move to another host, to avoid corruption.
 */
func Move(new_host string, old_host string, uuid string) error {
	var (
		err error
		dirname, filename string
		dirname_old, filename_old string
	)
	/* target file for the save */
	filename = reg_file(new_host, uuid)
	dirname = filepath.Dir(filename)
	/* source file for the move */
	filename_old = reg_file(old_host, uuid)
	dirname_old = filepath.Dir(filename_old)

	/* try the atomic rename */
	err = os.Rename(filename_old, filename)
	if (err != nil) {
		return err
	}
	err = reg_syncdir(dirname)
	if (err != nil) {
		return err
	}
	err = reg_syncdir(dirname_old)
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

/*
 * returns nil if file exists and is accessible, error otherwise.
 * Caller can check os.IsNotExist(err) to distinguish the cases.
 */
func Access(host_uuid string, vm_uuid string) error {
	_, err := os.Stat(reg_file(host_uuid, vm_uuid))
	if (err == nil) {
		return nil
	}
	return err
}

/* get all the VM Uuids for a host */
func Uuids(host_uuid string) ([]string, error) {
	var (
		uuids []string
		err error
		entries []os.DirEntry
		i, length int
		name string
	)
	entries, err = os.ReadDir(reg_dir(host_uuid))
	if (err != nil) {
		return nil, err
	}
	for i, _ = range(entries) {
		if (entries[i].IsDir()) {
			continue
		}
		name = entries[i].Name()
		length = len(name)
		if (length != 40) {
			continue
		}
		if (name[length - 4:] != ".xml") {
			continue
		}
		uuids = append(uuids, name[:length - 4])
	}
	return uuids, nil
}
