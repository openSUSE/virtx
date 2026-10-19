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
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/paths"

	. "suse.com/virtx/pkg/constants"
)

const (
	LOCK_SPACE_FILE = LOCK_DIR + LOCK_SPACE
	LOCK_JOIN_RETRIES = 10
	LOCK_INQ_RETRIES = 10
	HOST_ID_MAX = 2000 /* 1..2000 are valid */

	/* see sanlock_rv.h */
	SANLK_HOSTID_BUSY = -262

	LOCK_VALUE_SECTOR = 2002 /* see sanlock resource.c */
	BLOCK_SIZE = 512 /* on NFS, sanlock and libvirt always use 512 byte blocks */
)
type Lockman struct {
	m sync.RWMutex
	host_id uint16
}
var (
	lm = Lockman{}
)

/* The lockspace is never left on exit: libvirt can still own leases. */
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
		err = reg.Load_lockid(host_uuid, &lm.host_id)
		if (err != nil) {
			if (errors.Is(err, os.ErrNotExist)) {
				logger.Debug("lockid is not cached in reg yet")
			} else {
				return errors.New("reg: failed to Load_lockid: " + err.Error())
			}
		}
		for i := 0; i < LOCK_JOIN_RETRIES; i++ {
			if (lm.host_id == 0) {
				/* host_id is not cached so search for a free slot dynamically */
				lm.host_id, err = lm_search_join_lockspace(host_uuid)
				if (err == nil) {
					err = reg.Save_lockid(host_uuid, lm.host_id)
					if (err != nil) {
						return errors.New("reg: failed to Save_lockid: " + err.Error())
					}
					break /* SUCCESS! */
				}
			} else {
				/* host_id is coming from cache, so we only try this static slot */
				code := lm_join_lockspace(lm.host_id)
				if (code == 0) {
					break /* SUCCESS! */
				} else {
					err = fmt.Errorf("add_lockspace failed for host_id %d: %d", lm.host_id, code)
				}
			}
			logger.Log("error joining lockspace: %s", err.Error())
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
	/*
	 * The lockspace is joined, so the daemon knows this host's host_id: give
	 * back the resource leases this host still holds there from an earlier
	 * life but no longer owns (stale.go). It never blocks the startup, and
	 * the coordinator's first classification happens later. The host_id is
	 * passed in: we hold lm.m here, so the sweep must not read it itself.
	 */
	Release_stale_registrations(lm.host_id)
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

	cmd = exec.Command(paths.Get("SANLOCK"), args...)
	output, err = cmd.CombinedOutput()
	if (err != nil) {
		logger.Log("%s\n", string(output))
		return err
	}
	return nil
}

/*
 * attempt to join a lockspace using host_id for this host.
 * The function returns 0 on success, or the sanlock error code on failure.
 */
func lm_join_lockspace(host_id uint16) int {
	var (
		err error
		args []string
		sanlock_path string
		cmd *exec.Cmd
		output []byte
	)
	sanlock_path = fmt.Sprintf("%s:%d:%s:%d", LOCK_SPACE, host_id, LOCK_SPACE_FILE, 0)
	args = []string{ "client", "add_lockspace", "-s", sanlock_path }
	logger.Debug("sanlock %v", args)
	cmd = exec.Command(paths.Get("SANLOCK"), args...)
	output, err = cmd.CombinedOutput()
	if (err == nil) {
		return 0
	}
	logger.Log("%s\n", string(output))
	var code int = -1
	for _, line := range strings.Split(string(output), "\n") {
		n, _ := fmt.Sscanf(line, "add_lockspace done %d", &code)
		if (n == 1) {
			break
		}
	}
	return code
}

/* try to find a free host_id and use it to register the lockspace */
func lm_search_join_lockspace(host_uuid string) (uint16, error) {
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
	cmd = exec.Command(paths.Get("SANLOCK"), args...)
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
		code := lm_join_lockspace(host_id)
		if (code == 0) {
			return host_id, nil /* SUCCESS! */
		} else if (code != SANLK_HOSTID_BUSY) {
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
	cmd = exec.Command(paths.Get("SANLOCK"), args...)
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
	cmd = exec.Command(paths.Get("SANLOCK"), args...)
	output, err = cmd.CombinedOutput()
	if (err != nil) {
		logger.Log("%s\n", string(output))
	}
	return err
}

/*
 * Get_coord_path is where the coordinator lease lives: one flat file directly
 * in the lock directory (the resource name is already unique), rather than the
 * per-resource directory layout the VM disk leases use. The decision layer's
 * text calls it a "resource file", and there is no reason to wrap it in
 * a directory of the same name.
 */
func Get_coord_path(resource_name string) string {
	return LOCK_DIR + resource_name
}

/*
 * tmp_resource_path is the temporary file one create attempt initializes before
 * it is linked into place under the final name.
 *
 * The name must be unique per attempt: every host creates the same resource on
 * the same shared storage, and with one fixed name two hosts delete each
 * other's half-written file, which makes the link(2) below fail with ENOENT
 * (measured 2026-10-07: "link /vms/lock/__VIRTX__COORD.tmp
 * /vms/lock/__VIRTX__COORD: no such file or directory"). The writer's uuid, the
 * pid and the nanosecond clock keep it unique without any coordination and say
 * who left it behind, the same way Create_resource's MkdirTemp does for the
 * per-resource directory layout.
 */
func tmp_resource_path(final_path string, uuid string) string {
	return fmt.Sprintf("%s.tmp.%s.%d.%d", final_path, uuid, os.Getpid(), time.Now().UnixNano())
}

/*
 * Create_resource_flat initializes one resource file directly in LOCK_DIR.
 * It mirrors Create_resource (initialize under a temporary name, then link it
 * into place so that parallel creations cannot replace each other); a flat file
 * cannot use rename for that, because rename would silently overwrite, so it
 * uses link(2), which fails with EEXIST instead.
 *
 * A file that already exists is not an error here: the coordinator's rule is
 * "create first, then take" (row 5a), and losing that race just
 * means someone else created it.
 */
func Create_resource_flat(resource_name string, uuid string) error {
	return create_resource_flat(Get_coord_path(resource_name), resource_name, uuid, lm_init_resource_file)
}

/*
 * create_resource_flat is Create_resource_flat with the final path and the
 * initializer passed in, so that the create race can be tested with real files
 * and without sanlock or the lock directory.
 *
 * The temporary file is unique per attempt (tmp_resource_path), so two hosts
 * creating the same resource cannot disturb each other: each one initializes
 * its own file and only the link(2) into the final name decides the winner.
 * That link either succeeds, or fails with EEXIST because another host got
 * there first - and EEXIST is the only outcome that means "it exists". ENOENT
 * means our own temporary file went away, which is a real failure and is
 * reported as such (it must never be read as "already exists").
 *
 * The temporary file is removed on every path this function can take (a failed
 * initializer, a lost race, a successful link). A process that is killed in
 * between leaves one uniquely named file behind: nothing ever reuses that name,
 * so it is litter rather than a hazard, and only an operator should remove it -
 * a sweep cannot tell a dead writer's file from a live one's.
 */
func create_resource_flat(final_path string, resource_name string, uuid string,
                          init func(resource_path string, resource_name string, uuid string) error) error {
	var (
		err error
		lerr error
		tmp_path, lvb string
	)
	_, err = os.Stat(final_path)
	if (err == nil) {
		lvb, lerr = Read_lvb(final_path)
		if (lerr != nil) {
			return nil /* it exists, which is all we need to know */
		}
		logger.Debug("lockman: %s already exists (LOCK_VALUE %q)", resource_name, lvb)
		return nil
	}
	tmp_path = tmp_resource_path(final_path, uuid)
	err = init(tmp_path, resource_name, uuid)
	if (err != nil) {
		_ = os.Remove(tmp_path)
		return err
	}
	err = os.Link(tmp_path, final_path)
	_ = os.Remove(tmp_path)
	if (err == nil) {
		return nil
	}
	if (errors.Is(err, os.ErrExist)) {
		/* another host linked its file in first: that file is the resource */
		logger.Debug("lockman: %s was created by someone else first", resource_name)
		return nil
	}
	if (errors.Is(err, os.ErrNotExist)) {
		return fmt.Errorf("cannot put %s in place: its temporary file is gone (not \"already exists\"): %w",
		                  resource_name, err)
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
		path, resource_path, lvb string
	)
	resource_path = Get_resource_path(resource_name)
	_, err = os.Stat(resource_path)
	if (err == nil) {
		lvb, err = Read_lvb(resource_path)
		if (err != nil) {
			logger.Log("failed to Read LOCK_VALUE: %s", err)
			return fmt.Errorf("(0) cannot create %s for vm %s: it already exists", resource_name, uuid)
		}
		return fmt.Errorf("(1) cannot create %s for vm %s: it already exists for vm %s", resource_name, uuid, lvb)
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
			lvb, err = Read_lvb(resource_path)
			if (err != nil) {
				logger.Log("failed to Read LOCK_VALUE: %s", err)
				return fmt.Errorf("(2) cannot create %s for vm %s: it already exists", resource_name, uuid)
			}
			return fmt.Errorf("(3) cannot create %s for vm %s: it already exists for vm %s", resource_name, uuid, lvb)
		}
		return err
	}
	return nil
}

func Check_resource(resource_name string, uuid string) error {
	var (
		err error
		resource_path, lvb string
	)
	resource_path = Get_resource_path(resource_name)
	_, err = os.Stat(resource_path)
	if (err != nil) {
		if (errors.Is(err, os.ErrNotExist)) {
			return err
		} else {
			return fmt.Errorf("could not Stat: %s", err.Error())
		}
	}
	lvb, err = Read_lvb(resource_path)
	if (err != nil) {
		return fmt.Errorf("failed to Read LOCK_VALUE: %s", err.Error())
	}
	if (lvb != uuid) {
		return fmt.Errorf("LOCK_VALUE %s does not match vm %s", lvb, uuid)
	}
	/* Ok, the path exists, and ownership is ok */
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
		{ paths.Get("RM"), "--", resource_path },
		{ paths.Get("RMDIR"), "--", filepath.Dir(resource_path) },
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
 * write an LOCK_VALUE payload: it lives in one block at LOCK_VALUE_SECTOR, NUL padded, and is
 * written directly while the lease is held - the technique the resource
 * creation path already uses for the uuid.
 */
func lm_write_lvb(fd int, payload []byte) error {
	var (
		err error
		buf [BLOCK_SIZE]byte
	)
	if (len(payload) >= BLOCK_SIZE) {
		return fmt.Errorf("LOCK_VALUE payload of %d bytes does not fit in %d", len(payload), BLOCK_SIZE)
	}
	copy(buf[:], payload)
	err = lm_pwrite(fd, buf[:], LOCK_VALUE_SECTOR * BLOCK_SIZE)
	if (err != nil) {
		return err
	}
	err = unix.Fsync(fd)
	if (err != nil) {
		return err
	}
	return nil
}

func lm_set_lvb(fd int, uuid string) error {
	return lm_write_lvb(fd, []byte(uuid))
}

/*
 * Write_lvb replaces the Lock Value Block of one resource with payload. The
 * coordinator lease keeps its epoch and mask deadline there: those two items,
 * and nothing else.
 */
func Write_lvb(resource_path string, payload string) error {
	var (
		err error
		fd int
	)
	fd, err = unix.Open(resource_path, unix.O_RDWR, 0)
	if (err != nil) {
		return err
	}
	defer unix.Close(fd)
	return lm_write_lvb(fd, []byte(payload))
}

func Read_lvb(resource_path string) (string, error) {
	var (
		err error
		buf [BLOCK_SIZE]byte
		uuid string
		fd int
	)
	fd, err = unix.Open(resource_path, unix.O_RDONLY, 0)
	if (err != nil) {
		return uuid, err
	}
	defer unix.Close(fd)

	err = lm_pread(fd, buf[:], LOCK_VALUE_SECTOR * BLOCK_SIZE)
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
	fd, err = unix.Open(resource_path, unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_SYNC, 0660)
	if (err != nil) {
		return err /* likely permissions */
	}
	defer unix.Close(fd)
	/* Initialize the resource */
	sanlock_path = fmt.Sprintf("%s:%s:%s:%d", LOCK_SPACE, resource_name, resource_path, 0)
	args = []string{ "client", "init", "-r", sanlock_path }
	logger.Debug("sanlock %v", args)

	cmd = exec.Command(paths.Get("SANLOCK"), args...)
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
		"-c", "3", paths.Get("VIRTX_CHECK_LVB"), resource_path, uuid,
	}
	for _, cmd := range args {
		sanlock_args = append(sanlock_args, "-c", strconv.Itoa(len(cmd)))
		sanlock_args = append(sanlock_args, cmd...)
	}
	if (no_disk) {
		sanlock_args = append(sanlock_args, "-d", "1")
	}
	logger.Debug("sanlock %v", sanlock_args)

	cmd = exec.Command(paths.Get("SANLOCK"), sanlock_args...)
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
	lm.m.RLock()
	defer lm.m.RUnlock()
	return int16(lm.host_id)
}
