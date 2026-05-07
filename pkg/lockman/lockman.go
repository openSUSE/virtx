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
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, see
 * <https://www.gnu.org/licenses/>
 */
package lockman

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"fmt"
	"strings"
	"strconv"
	"sync"
	"time"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"bytes"
	"errors"
	"golang.org/x/sys/unix"

	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	. "suse.com/virtx/pkg/constants"
)

const (
	LOCK_SPACE_FILE = LOCK_DIR + LOCK_SPACE
	SANLOCK = "/usr/sbin/sanlock"
	LOCK_JOIN_RETRIES = 10
	LOCK_INQ_RETRIES = 10
	HOST_ID_MAX = 2000 /* 1..2000 are valid */
	SANLK_HOSTID_BUSY = -262 /* sanlock_rv.h */
	LVB_SECTOR = 2002 /* see sanlock resource.c */
	BLOCK_SIZE = 512 /* on NFS, sanlock and libvirt always use 512 byte blocks */
)
type Lockman struct {
	m sync.RWMutex
	host_id uint16
}
var (
	lm = Lockman{}
)

func Init(host_uuid string) error {
	lm.m.Lock()
	defer lm.m.Unlock()
	var (
		err error
	)
	_ = unix.Umask(6)
	err = lm_init_lockdir()
	if (err != nil) {
		return errors.New("lm_init_lockdir: " + err.Error())
	}
	lm.host_id, err = lm_inq_lockspace()
	if (err != nil) {
		return errors.New("lm_inq_lockspace: " + err.Error())
	}
	if (lm.host_id == 0) {
		/* we are not yet in the lockspace */
		for i := 0; i < LOCK_JOIN_RETRIES; i++ {
			lm.host_id, err = lm_join_lockspace(host_uuid)
			if (err == nil) {
				break
			}
			logger.Log("lm_join_lockspace: %s", err.Error())
			time.Sleep(1 * time.Second)
		}
	}
	if (err != nil) {
		return errors.New("lm_join_lockspace: " + err.Error())
	}
	for i := 0; i < LOCK_INQ_RETRIES; i++ {
		_, err = lm_inq_lockspace()
		if (err == nil) {
			break
		}
		logger.Log("lm_inq_lockspace: %s", err.Error())
		time.Sleep(1 * time.Second)
	}
	if (err != nil) {
		return errors.New("lm_inq_lockspace: " + err.Error())
	}
	return nil
}

func lm_init_lockdir() error {
	var (
		err error
		sanlock_gid int
		sanlock_g *user.Group
		path string
	)
	sanlock_g, err = user.LookupGroup("sanlock")
	if (err != nil) {
		return err
	}
	sanlock_gid, err = strconv.Atoi(sanlock_g.Gid)
	if (err != nil) {
		return err
	}
	_, err = os.Stat(LOCK_DIR)
	if (err == nil) {
		/*
		 * if the lockdir already exists, we do not need to init it.
		 * This is just an optimization, and offers no guarantees,
		 * the non-empty temporary dir and the rename are used to guarantee
		 * one unique concurrent initialization is effective.
		 */
		return nil
	}
	/*
	 * create the new dir as a temporary directory,
	 * put the lockspace file in it, which also ensures no other rename can replace it,
	 * then initialize the lockspace and rename to the final directory.
	 * MkdirTemp has a small chance of collisions between hosts, but will retry internally
	 * 10000 times if a collision is detected.
	 *
	 * See Golang src/os/tempfile.go
	 */
	path, err = os.MkdirTemp(VMS_DIR, "lock-tmp")
	if (err != nil) {
		return err
	}
	err = unix.Chmod(path, 02771) /* setgid, so group sanlock owns files inside */
	if (err != nil) {
		_ = os.Remove(path)
		return err
	}
	err = unix.Lchown(path, -1, sanlock_gid)
	if (err != nil) {
		_ = os.Remove(path)
		return err
	}
	err = lm_init_lockspace(path + "/" + LOCK_SPACE)
	if (err != nil) {
		_ = os.Remove(path + "/" + LOCK_SPACE)
		_ = os.Remove(path)
		return err
	}
	err = os.Rename(path, LOCK_DIR)
	if (err != nil) {
		/* golang src/os/error_unix_test.go : this should cover EEXIST and ENOTEMPTY */
		if (errors.Is(err, os.ErrExist)) {
			/* another host initialized quicker than us, cleanup */
			_ = os.Remove(path + "/" + LOCK_SPACE)
			_ = os.Remove(path)
			return nil
		}
		return err
	}
	return nil
}

func lm_init_lockspace(path string) error {
	var (
		err error
		args []string
		sanlock_path string
		cmd *exec.Cmd
		output []byte
		fd int
	)
	/*
	 * this code mirrors what libvirt does in virLockManagerSanlockSetupLockspace(),
	 * but it does better because it is protected by the init_lockdir mechanism
	 * to defend against parallel initializations.
	 * The lock directory is setgid, so the file will be owned by qemu:sanlock when created
	 */
	fd, err = unix.Open(path, unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL, 0660)
	if (err != nil) {
		return err /* likely permissions */
	}
	_ = unix.Close(fd)

	/* Initialize the lockspace */
	sanlock_path = fmt.Sprintf("%s:%d:%s:%d", LOCK_SPACE, 0, path, 0)
	args = []string{ "client", "init", "-s", sanlock_path }
	logger.Debug("sanlock %v", args)

	cmd = exec.Command(SANLOCK, args...)
	output, err = cmd.CombinedOutput()
	if (err != nil) {
		logger.Log("%s\n", string(output))
		return err
	}
	return nil
}

func lm_join_lockspace(host_uuid string) (uint16, error) {
	var (
		err error
		args []string
		sanlock_path string
		cmd *exec.Cmd
		output []byte
		h [16]byte
		host_id uint16
		busy_ids [HOST_ID_MAX + 1]bool
	)
	h = md5.Sum([]byte(host_uuid))
	host_id = uint16(binary.BigEndian.Uint32(h[:4]) % 2000 + 1)

	/* get the status of busy IDs from the daemon perspective */
	sanlock_path = fmt.Sprintf("%s:%d:%s:%d", LOCK_SPACE, 0, LOCK_SPACE_FILE, 0)
	args = []string{ "client", "host_status", "-s", sanlock_path }
	logger.Debug("sanlock %v", args)
	cmd = exec.Command(SANLOCK, args...)
	output, err = cmd.CombinedOutput()
	if (err != nil && len(output) != 0) { /* sanlock exits with error if there are no hosts in the list */
		return 0, err
	}
	/* build the table of busy ids */
	var id, ts, i int
	for _, line := range strings.Split(string(output), "\n") {
		n, _ := fmt.Sscanf(line, "%d timestamp %d", &id, &ts)
		if (n == 2 && ts != 0) {
			busy_ids[id] = true
		}
	}
	/* linear search1 */
	for i = 0; i <= HOST_ID_MAX; host_id_next(&host_id) {
		i++ /* no comma operator in Golang, ouch. */
		if (!busy_ids[host_id]) {
			break
		}
	}
	if (i > HOST_ID_MAX) {
		return 0, errors.New("could not join, all slots busy")
	}
	for i = 0; i <= HOST_ID_MAX; host_id_next(&host_id) {
		i++ /* no comma operator in Golang, ouch. */
		if (busy_ids[host_id]) { /* do not try slots that the daemon considers busy */
			continue
		}
		sanlock_path = fmt.Sprintf("%s:%d:%s:%d", LOCK_SPACE, host_id, LOCK_SPACE_FILE, 0)
		args = []string{ "client", "add_lockspace", "-s", sanlock_path }
		logger.Debug("sanlock %v", args)
		cmd = exec.Command(SANLOCK, args...)
		output, err = cmd.CombinedOutput()
		if (err == nil) {
			return host_id, nil /* SUCCESS ! */
		}
		logger.Log("%s\n", string(output))
		var code int
		for _, line := range strings.Split(string(output), "\n") {
			n, _ := fmt.Sscanf(line, "add_lockspace done %d", &code)
			if (n == 1) {
				break
			}
		}
		if (code != SANLK_HOSTID_BUSY) {
			return 0, fmt.Errorf("add_lockspace failed for host_id %d: %d", host_id, code)
		}
	}
	/* unlikely code path, tried 2000 host_ids */
	return 0, errors.New("could not join, all slots busy2")
}

func lm_inq_lockspace() (uint16, error) {
	var (
		err error
		args []string
		cmd *exec.Cmd
		output []byte
		host_id uint16
		fmts string = fmt.Sprintf("s %s:%%d:%s:%d", LOCK_SPACE, LOCK_SPACE_FILE, 0)
	)
	args = []string{ "client", "gets" }
	logger.Debug("sanlock %v", args)
	cmd = exec.Command(SANLOCK, args...)
	output, err = cmd.CombinedOutput()
	if (err != nil) {
		logger.Log("%s\n", string(output))
		return 0, err
	}
	for _, line := range strings.Split(string(output), "\n") {
		/* fmts := "s __VIRTX__DISKS__:%d:/vms/lock/__VIRTX__DISKS__:0" */
		n, _ := fmt.Sscanf(line, fmts, &host_id)
		if (n == 1) {
			break
		}
	}
	/* NB: 0 is a valid return value if there is no error and no match */
	return host_id, nil
}

func lm_leave_lockspace() error {
	var (
		err error
		args []string
		sanlock_path string
		cmd *exec.Cmd
		output []byte
	)
	sanlock_path = fmt.Sprintf("%s:%d:%s:%d", LOCK_SPACE, lm.host_id, LOCK_SPACE_FILE, 0)
	args = []string{ "client", "rem_lockspace", "-s", sanlock_path }
	logger.Debug("sanlock %v", args)
	cmd = exec.Command(SANLOCK, args...)
	output, err = cmd.CombinedOutput()
	if (err != nil) {
		logger.Log("%s\n", string(output))
	}
	return err
}

/*
 * Create and initialize a new resource file that doesn't exist.
 * If the resource already exists, fail.
 */
func Create_resource(resource_name string, uuid string) error {
	var (
		err error
		path string
	)
	_, err = os.Stat(Get_resource_path(resource_name))
	if (err == nil) {
		return fmt.Errorf("cannot create %s for vm %s: it already exists1", resource_name, uuid)
	}
	/*
	 * create the new dir as a temporary directory,
	 * put the resource file in it, which also ensures no other rename can replace it,
	 * then initialize the lockspace and rename to the final directory.
	 */
	path, err = os.MkdirTemp(LOCK_DIR, resource_name + "-tmp")
	if (err != nil) {
		return err
	}
	err = unix.Chmod(path, 02771) /* this is necessary because MkdirTemp always creates 0700 */
	if (err != nil) {
		_ = os.Remove(path)
		return err
	}
	err = lm_init_resource_file(path + "/" + resource_name, resource_name, uuid)
	if (err != nil) {
		_ = os.Remove(path + "/" + resource_name)
		_ = os.Remove(path)
		return err
	}
	err = os.Rename(path, LOCK_DIR + resource_name)
	if (err != nil) {
		/* golang src/os/error_unix_test.go : this should cover EEXIST and ENOTEMPTY */
		if (errors.Is(err, os.ErrExist)) {
			/* another host initialized quicker than us, cleanup */
			_ = os.Remove(path + "/" + resource_name)
			_ = os.Remove(path)
			return fmt.Errorf("cannot create resource %s for vm %s: it already exists2", resource_name, uuid)
		}
		return err
	}
	return nil
}

/*
 * Delete the resource lock file and directory while holding the lease.
 */
func Delete_resource(resource_name string, uuid string) error {
	var (
		err error
	)
	resource_path := Get_resource_path(resource_name)

	args := [][]string{
		{ "/usr/bin/rm", "--", resource_path },
		{ "/usr/bin/rmdir", "--", filepath.Dir(resource_path) },
	}
	err = Run(resource_name, uuid, args, true)
	if (err != nil) {
		return err
	}
	return nil
}

func lm_pwrite(fd int, buf []byte, offset int64) error {
	var (
		err error
		size int
	)
	for {
		size, err = unix.Pwrite(fd, buf, offset)
		if (err != nil) {
			if (errors.Is(err, unix.EINTR)) {
				continue
			}
			return fmt.Errorf("pwrite at offset %d: %w", offset, err)
		}
		if (size != len(buf)) {
			return fmt.Errorf("pwrite incomplete at offset %d: got %d/%d", offset, size, len(buf))
		}
		return nil
	}
}

func lm_pread(fd int, buf []byte, offset int64) error {
	var (
		err error
		size int
	)
	for {
		size, err = unix.Pread(fd, buf, offset)
		if (err != nil) {
			if (errors.Is(err, unix.EINTR)) {
				continue
			}
			return fmt.Errorf("pread at offset %d: %w", offset, err)
		}
		if (size != len(buf)) {
			return fmt.Errorf("pread incomplete at offset %d: got %d/%d", offset, size, len(buf))
		}
		return nil
	}
}

/*
 * set the LVB (Lock Value Block). Ideally we would have a sanlock client command for this,
 * but this works too.
 */
func lm_set_lvb(fd int, uuid string) error {
	var (
		err error
		buf [BLOCK_SIZE]byte
	)
	copy(buf[:], []byte(uuid))
	err = lm_pwrite(fd, buf[:], LVB_SECTOR * BLOCK_SIZE)
	if (err != nil) {
		return err
	}
	err = unix.Fsync(fd)
	if (err != nil) {
		return err
	}
	return nil
}

func Read_lvb(fd int) (string, error) {
	var (
		err error
		buf [BLOCK_SIZE]byte
		uuid string
	)
	err = lm_pread(fd, buf[:], LVB_SECTOR * BLOCK_SIZE)
	if (err != nil) {
		return uuid, err
	}
	/* strip zeroes from string */
	i := bytes.IndexByte(buf[:], 0)
    if (i < 0) {
        uuid = string(buf[:])
    } else {
		uuid = string(buf[:i])
	}
	return uuid, nil
}

func lm_init_resource_file(resource_path string, resource_name string, uuid string) error {
	var (
		err error
		args []string
		cmd *exec.Cmd
		output []byte
		fd int
		sanlock_path string
	)
	/*
	 * this code mirrors what libvirt does in virLockManagerSanlockCreateLease(),
	 * but it does better because it is protected by the init_resource_dir mechanism
	 * to defend against parallel initializations.
	 * The lock directory is setgid, so the file will be owned by qemu:sanlock when created
	 */
	fd, err = unix.Open(resource_path, unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_DIRECT | unix.O_SYNC, 0660)
	if (err != nil) {
		return err /* likely permissions */
	}
	defer unix.Close(fd)
	/* Initialize the resource */
	sanlock_path = fmt.Sprintf("%s:%s:%s:%d", LOCK_SPACE, resource_name, resource_path, 0)
	args = []string{ "client", "init", "-r", sanlock_path }
	logger.Debug("sanlock %v", args)

	cmd = exec.Command(SANLOCK, args...)
	output, err = cmd.CombinedOutput()
	if (err != nil) {
		logger.Log("%s\n", string(output))
		return err
	}
	return lm_set_lvb(fd, uuid)
}

/* run a set of commands under resource lock, the first failure stops the chain */
func Run(resource_name string, uuid string, args [][]string, no_disk bool) error {
	var (
		err error
		sanlock_args []string
		sanlock_path, resource_path string
		cmd *exec.Cmd
		output []byte
	)
	resource_path = Get_resource_path(resource_name)
	sanlock_path = fmt.Sprintf("%s:%s:%s:%d", LOCK_SPACE, resource_name, resource_path, 0)
	/*
	 * check-lvb is always prepended, to verify VM ownership
	 */
	sanlock_args = []string{
		"client", "spawn", "-P" , "1", "-r", sanlock_path,
		"-c", "3", "/usr/sbin/virtx-check-lvb", resource_path, uuid,
	}
	for _, cmd := range args {
		sanlock_args = append(sanlock_args, "-c", strconv.Itoa(len(cmd)))
		sanlock_args = append(sanlock_args, cmd...)
	}
	if (no_disk) {
		sanlock_args = append(sanlock_args, "-d", "1")
	}
	logger.Debug("sanlock %v", sanlock_args)

	cmd = exec.Command(SANLOCK, sanlock_args...)
	output, err = cmd.CombinedOutput()
	if (err != nil) {
		logger.Log("%s\n", string(output))
		return err
	}
	return nil
}

func host_id_next(host_id *uint16) {
	*host_id += 1
	if (*host_id > HOST_ID_MAX) {
		*host_id = 1
	}
}

func Shutdown() {
	lm.m.Lock()
	defer lm.m.Unlock()
	var err error

	logger.Debug("shutdown started...")
	if (lm.host_id != 0) {
		err = lm_leave_lockspace()
		if (err != nil) {
			logger.Log("failed to leave lockspace")
		}
	}
	logger.Debug("shutdown complete.")
}

var dev_disks_prefixes = []string{ "dm-uuid-mpath-", "scsi-", "nvme-", "wwn-0x" }

func Get_resource_name(device openapi.DiskDevice, path string) string {
	var (
		key string
		h [16]byte
	)
	switch (device) {
	case openapi.DEVICE_LUN:
		key = filepath.Base(path) /* wwn-0x6001405d594364bf2bb455eaceb80934 or dm-... */
		for _, prefix := range dev_disks_prefixes {
			if (strings.HasPrefix(key, prefix)) {
				key = strings.TrimPrefix(key, prefix)
				if (prefix == "wwn-0x") {
					return "3" + key /* normalize to match scsi- and dm-uuid-mpath- */
				}
				return key
			}
		}
		logger.Log("unexpected dev_disks key: %s", key)
		return key
	case openapi.DEVICE_DISK:
		fallthrough
	case openapi.DEVICE_CDROM:
		key = strings.TrimPrefix(path, DS_DIR)
		h = md5.Sum([]byte(key))
		key = hex.EncodeToString(h[:])
		return key
	default:
		logger.Log("only DISK, CDROM and LUN can have leases")
		return ""
	}
}

func Get_resource_path(resource_name string) string {
	return LOCK_DIR + resource_name + "/" + resource_name
}

func Lockid() int16 {
	lm.m.Lock()
	defer lm.m.Unlock()
	return int16(lm.host_id)
}
