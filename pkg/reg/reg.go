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

package reg

import (
	"fmt"
	"os"
	"path/filepath"
	. "suse.com/virtx/pkg/constants"
)

/*
 * Each VM has its own directory in <REG_DIR>/<host_uuid>/<vm_uuid>/ .
 * The processed libvirt xml (dumpxml from a defined, not run domain)
 * is stored inside it as:
 * <REG_DIR>/<host_uuid>/<vm_uuid>/<vm_uuid>.xml
 */

/* get the path of the per-VM directory registered for this VM */
func Vmdir(host_uuid string, vm_uuid string) string {
	return fmt.Sprintf("%s/%s/%s", REG_DIR, host_uuid, vm_uuid)
}

/* get the path of the actual processed xml file in shared storage registered for this VM */
func reg_file(host_uuid string, vm_uuid string) string {
	return fmt.Sprintf("%s/%s/%s/%s.xml", REG_DIR, host_uuid, vm_uuid, vm_uuid)
}

func reg_dir(host_uuid string) string {
	return fmt.Sprintf("%s/%s", REG_DIR, host_uuid)
}

func Syncdir(dirname string) error {
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
 * We try to atomically write, to avoid corruption of a pre-existing file,
 * or a half-written new file.
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
	/* ensure the per-VM directory exists */
	err = os.MkdirAll(dirname, 0750)
	if (err != nil) {
		return err
	}
	/*
	 * on failure, remove the per-VM directory if it is left empty, so a failed
	 * save does not leave behind a VM dir with no xml. os.Remove only removes an
	 * empty directory, so an existing registration or a concurrent successful
	 * save (which has placed the xml) is never disturbed.
	 */
	defer func() {
		if (err != nil) {
			os.Remove(dirname)
		}
	}()
	/* create temporary file */
	tmp, err = os.CreateTemp(dirname, fmt.Sprintf("%s.tmp-*", vm_uuid))
	if (err != nil) {
		return err
	}
	tmpname = tmp.Name()
	defer func() {
		if (err != nil) {
			tmp.Close()
			os.Remove(tmpname)
		}
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
	/*
	 * now try the atomic rename. This is the commit point,
	 * so we use a separate error variable after this (serr),
	 * so that the deferred cleanups do not delete our directories.
	 */
	err = os.Rename(tmpname, filename)
	if (err != nil) {
		return err
	}
	/* sync the VM dir to persist the xml file then host for the vm entry */
	serr := Syncdir(dirname)
	if (serr != nil) {
		return serr
	}
	serr = Syncdir(filepath.Dir(dirname))
	if (serr != nil) {
		return serr
	}
	return nil
}

/*
 * We try to atomically move to another host, to avoid corruption.
 */
func Move(new_host string, old_host string, uuid string) error {
	var (
		err error
		vmdir, vmdir_old string
		hostdir, hostdir_old string
	)
	/* destination and source per-VM directories */
	vmdir = Vmdir(new_host, uuid)
	vmdir_old = Vmdir(old_host, uuid)
	hostdir = filepath.Dir(vmdir)
	hostdir_old = filepath.Dir(vmdir_old)

	/* ensure the destination host directory exists */
	err = os.MkdirAll(hostdir, 0750)
	if (err != nil) {
		return err
	}
	/* try the atomic rename of the whole VM directory */
	err = os.Rename(vmdir_old, vmdir)
	if (err != nil) {
		return err
	}
	err = Syncdir(hostdir)
	if (err != nil) {
		return err
	}
	err = Syncdir(hostdir_old)
	if (err != nil) {
		return err
	}
	return nil
}

func Delete(host_uuid string, vm_uuid string) error {
	var (
		err error
		vmdir string
	)
	vmdir = Vmdir(host_uuid, vm_uuid)
	err = os.RemoveAll(vmdir)
	if (err != nil) {
		return err
	}
	err = Syncdir(filepath.Dir(vmdir))
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
		/* each VM is a directory named by its uuid; host option files are skipped */
		if (!entries[i].IsDir()) {
			continue
		}
		name = entries[i].Name()
		length = len(name)
		if (length != 36) {
			continue
		}
		uuids = append(uuids, name)
	}
	return uuids, nil
}

/* get all the Hosts present in reg */
func Hosts() ([]string, error) {
	var (
		uuids []string
		err error
		entries []os.DirEntry
		i, length int
		name string
	)
	entries, err = os.ReadDir(REG_DIR)
	if (err != nil) {
		return nil, err
	}
	for i, _ = range(entries) {
		if (!entries[i].IsDir()) {
			continue
		}
		name = entries[i].Name()
		length = len(name)
		if (length != 36) {
			continue
		}
		uuids = append(uuids, name)
	}
	return uuids, nil
}
