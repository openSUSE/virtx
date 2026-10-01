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
	"suse.com/virtx/pkg/logger"
)

/*
 * Each VM has its own directory in <REG_DIR>/<host_uuid>/<vm_uuid>/ .
 * The processed libvirt xml (dumpxml from a defined, not run domain)
 * is stored inside it as:
 * <REG_DIR>/<host_uuid>/<vm_uuid>/<vm_uuid>.xml
 */

/*
 * vmdir_leave_callbacks are invoked with vm_uuid whenever the per-VM
 * directory leaves this host's registration tree: Delete removes it, Move
 * moves it to another host. Registered by packages that keep their own
 * per-VM state (e.g. oplog's lock map) needing cleanup at that point.
 */
var vmdir_leave_callbacks []func(vm_uuid string)

func Register_vmdir_leave_callback(fn func(vm_uuid string)) {
	vmdir_leave_callbacks = append(vmdir_leave_callbacks, fn)
}

func vmdir_left(vm_uuid string) {
	for _, fn := range vmdir_leave_callbacks {
		fn(vm_uuid)
	}
}

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
 * reg_save_file writes data to a temporary file in the same directory, syncs
 * it, and renames it over filename, so that a reader (possibly on another
 * host) sees either the old file or the new one, never a partial one.
 *
 * The directory containing filename must already exist.
 */
func reg_save_file(filename string, data []byte) error {
	var (
		err error
		tmp *os.File
		tmpname, dirname string
	)
	dirname = filepath.Dir(filename)
	/* create temporary file */
	tmp, err = os.CreateTemp(dirname, fmt.Sprintf("%s.tmp-*", filepath.Base(filename)))
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
	_, err = tmp.Write(data)
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
	 * now the atomic rename, the commit point. No directory sync is needed:
	 * NFS servers commit metadata operations (rename, mkdir, remove) to stable
	 * storage before replying (requires the export to be "sync", the default).
	 * The data itself is already committed by tmp.Sync() above.
	 */
	err = os.Rename(tmpname, filename)
	if (err != nil) {
		return err
	}
	return nil
}

/*
 * Create the per-VM directory and save the domain XML in it.
 * Fails if the per-VM directory already exists in this host directory.
 * The caller must ensure that the VM is not registered in another host directory.
 */
func Create(host_uuid string, vm_uuid string, xml string) error {
	var (
		err error
		dirname, filename string
	)
	filename = reg_file(host_uuid, vm_uuid)
	dirname = filepath.Dir(filename)
	/* the host directory is created at startup, the per-VM directory must not exist */
	err = os.Mkdir(dirname, 0750)
	if (err != nil) {
		return err
	}
	/* on failure, remove the per-VM directory we just created, so we leave no VM dir with no xml */
	defer func() {
		if (err != nil) {
			os.Remove(dirname)
		}
	}()
	err = reg_save_file(filename, []byte(xml))
	if (err != nil) {
		return err
	}
	return nil
}

/*
 * Save the domain XML in the existing per-VM directory.
 * Never creates the per-VM directory: if the VM has been moved to another host
 * or deleted, this fails with ENOENT instead of creating a second registration.
 */
func Save(host_uuid string, vm_uuid string, xml string) error {
	return reg_save_file(reg_file(host_uuid, vm_uuid), []byte(xml))
}

/*
 * Atomically move the per-VM directory to another host: the rename is the commit point,
 * and the VM is registered in exactly one host directory at any time.
 * Requires NFSv4.1+: its sessions guarantee exactly-once execution, so a retransmitted
 * RENAME (lost reply) cannot fail with ENOENT after the first one succeeded.
 */
func Move(new_host string, old_host string, uuid string) error {
	var (
		err error
		vmdir, vmdir_old string
	)
	/* destination and source per-VM directories */
	vmdir = Vmdir(new_host, uuid)
	vmdir_old = Vmdir(old_host, uuid)

	/* ensure the destination host directory exists */
	err = os.MkdirAll(filepath.Dir(vmdir), 0750)
	if (err != nil) {
		return err
	}
	/* the atomic rename of the whole VM directory */
	err = os.Rename(vmdir_old, vmdir)
	if (err != nil) {
		return err
	}
	vmdir_left(uuid)
	return nil
}

/*
 * Atomically delete the per-VM directory: rename it to a tombstone in the same host
 * directory (the commit point), then remove the tombstone.
 * The tombstone name is not 36 chars long, so Uuids skips it.
 */
func Delete(host_uuid string, vm_uuid string) error {
	var (
		err error
		vmdir, tombstone string
	)
	vmdir = Vmdir(host_uuid, vm_uuid)
	tombstone = fmt.Sprintf("%s/%s/.%s.deleted", REG_DIR, host_uuid, vm_uuid)
	/* remove the leftovers of a previous Delete, interrupted after the rename */
	err = os.RemoveAll(tombstone)
	if (err != nil) {
		return err
	}
	err = os.Rename(vmdir, tombstone)
	if (err != nil) {
		return err
	}
	vmdir_left(vm_uuid)
	/* the VM is not registered anymore, a failure here only leaves garbage behind */
	err = os.RemoveAll(tombstone)
	if (err != nil) {
		logger.Log("reg.Delete: could not remove %s: %s", tombstone, err.Error())
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
