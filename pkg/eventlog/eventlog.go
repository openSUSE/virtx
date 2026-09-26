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

package eventlog

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"suse.com/virtx/pkg/encoding/sbinary"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/reglog"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/ts"
)

/*
 * Sister to pkg/oplog, but for VM state changes NOT requested via the VirtX
 * API: guest/qemu-side failures (watchdog, guest panic, qemu process death,
 * storage errors) rather than VirtX-driven operations.
 *
 * Unlike oplog, these are single-shot observed facts, not start/end-paired
 * operations, so there is one append-only file pair per VM (not one per
 * operation type):
 * <REG_DIR>/<host_uuid>/<vm_uuid>/eventlog
 * <REG_DIR>/<host_uuid>/<vm_uuid>/eventlog.msg
 *
 * Only the host owning the VM writes these files. Within that host, a
 * per-VM mutex (see eventlog_get_lock) serializes writers for that VM,
 * while the msg file is written with O_APPEND, which the kernel serializes
 * on its own.
 */

/*
 * The record size is 32 bytes. Msg_off is an absolute offset into the .msg
 * file.
 */
type record struct {
	Ts int64 /* must stay first: reglog.Range() reads it at byte offset 0 */
	Class openapi.EventClass
	Code openapi.EventCode
	Reserved1 int32
	Msg_off int64
	Reserved2 int64
}
/* RECORD_SIZE is the sbinary encoded size of a record. */
const RECORD_SIZE = 8 + 2 + 2 + 4 + 8 + 8

/*
 * eventlog_locks holds one RWMutex per VM, created lazily, mirroring oplog.
 */
var eventlog_locks reglog.LockMap

func eventlog_get_lock(vm_uuid string) *sync.RWMutex {
	return eventlog_locks.Get(vm_uuid)
}

func init() {
	/* drops the lock for a VM whose directory has left this host */
	reg.Register_vmdir_leave_callback(eventlog_locks.Delete)
}

func eventlog_file(vm_uuid string) string {
	return fmt.Sprintf("%s/eventlog", reglog.Dir(vm_uuid))
}
func eventlog_msg_file(vm_uuid string) string {
	return fmt.Sprintf("%s/eventlog.msg", reglog.Dir(vm_uuid))
}

/* eventlog_msg returns the message of a record. */
func eventlog_msg(rec *record, vm_uuid string) (string, error) {
	var (
		err error
		f *os.File
		msg string
	)
	f, err = os.Open(eventlog_msg_file(vm_uuid))
	if (err != nil) {
		return "", err
	}
	defer f.Close()
	msg, err = reglog.Read_msg(f, rec.Msg_off)
	if (err != nil) {
		return "", err
	}
	return msg, nil
}

func eventlog_read(rec *record, f *os.File, offset int64) error {
	var (
		buf [RECORD_SIZE]byte
		err error
	)
	_, err = f.ReadAt(buf[:], offset)
	if (err != nil) {
		return err
	}
	_, err = sbinary.Decode(buf[:], binary.LittleEndian, rec)
	if (err != nil) {
		return err
	}
	return nil
}

/*
 * eventlog_append writes a record at the next aligned offset, mirroring
 * oplog_append. Unlike oplog, records are never updated in place.
 */
func eventlog_append(rec *record, vm_uuid string) error {
	m := eventlog_get_lock(vm_uuid)
	m.Lock()
	defer m.Unlock()
	var (
		err error
		buf [RECORD_SIZE]byte
	)
	/* set Ts under the lock so the record order matches write order */
	rec.Ts = ts.Now()
	_, err = sbinary.Encode(buf[:], binary.LittleEndian, rec)
	if (err != nil) {
		return err
	}
	_, err = reglog.Append_record(eventlog_file(vm_uuid), buf[:], RECORD_SIZE)
	return err
}

/*
 * eventlog_open opens the eventlog file and returns it along with its
 * current record count. A missing file returns a nil File; the caller
 * treats this as an empty log, not an error.
 */
func eventlog_open(vm_uuid string) (*os.File, int64, error) {
	return reglog.Open_log(eventlog_file(vm_uuid), RECORD_SIZE)
}

/*
 * eventlog_fetch_records reads up to limit records (0=no limit) in
 * range [from, to] (found via binary search), optionally filtered to a
 * single class and/or code (0 = wildcard for either). backward walks
 * most-recent-first; otherwise oldest-first. Since there is a single file
 * (unlike oplog's per-Op split), a class/code filter is applied by skipping
 * non-matching records while walking the Ts-bounded range, rather than by
 * opening a separate pre-filtered file.
 */
func eventlog_fetch_records(vm_uuid string, class openapi.EventClass, code openapi.EventCode,
	from int64, to int64, limit int, backward bool) ([]record, error) {
	m := eventlog_get_lock(vm_uuid)
	m.RLock()
	defer m.RUnlock()
	var (
		err error
		f *os.File
		nrec, lo, hi, i int64
		rec record
		recs []record
	)
	f, nrec, err = eventlog_open(vm_uuid)
	if (err != nil) {
		return nil, err
	}
	if (f == nil) {
		return nil, nil
	}
	defer f.Close()
	lo, hi, err = reglog.Range(f, RECORD_SIZE, nrec, from, to)
	if (err != nil) {
		return nil, err
	}
	if (backward) {
		i = hi - 1
	} else {
		i = lo
	}
	for ((backward && i >= lo) || (!backward && i < hi)) {
		if (limit != 0 && len(recs) >= limit) {
			break
		}
		err = eventlog_read(&rec, f, i * RECORD_SIZE)
		if (err != nil) {
			return recs, err
		}
		if ((class == 0 || rec.Class == class) && (code == 0 || rec.Code == code)) {
			recs = append(recs, rec)
		}
		if (backward) {
			i--
		} else {
			i++
		}
	}
	return recs, nil
}

/*
 * Log appends a new eventlog entry for vm_uuid. Used by the libvirt
 * lifecycle event handler when it observes a VM state change that was not
 * requested via the VirtX API.
 */
func Log(vm_uuid string, class openapi.EventClass, code openapi.EventCode, msg string) error {
	var (
		err error
		msg_off int64
	)
	msg_off, err = reglog.Append_msg(eventlog_msg_file(vm_uuid), msg)
	if (err != nil) {
		return err
	}
	rec := record{
		Class: class,
		Code: code,
		Msg_off: msg_off,
	}
	return eventlog_append(&rec, vm_uuid)
}

/*
 * List returns the eventlog entries matching the passed options.
 */
func List(vm_uuid string, o *openapi.VmEventlogListOptions) (openapi.EventlogList, error) {
	var (
		err error
		recs []record
		list openapi.EventlogList
		backward bool
		limit int
	)
	if (o.Head != 0 && o.Tail != 0) {
		return list, errors.New("eventlog: head and tail are mutually exclusive")
	}
	switch {
	case (o.Tail != 0):
		backward, limit = true, int(o.Tail)
	case (o.Head != 0):
		backward, limit = false, int(o.Head)
	default:
		backward, limit = true, 0
	}
	recs, err = eventlog_fetch_records(vm_uuid, openapi.EventClass(o.Class), openapi.EventCode(o.Code), o.From, o.To, limit, backward)
	if (err != nil) {
		return list, err
	}
	/* backward yields most-recent-first, forward yields oldest-first; flip
	 * only when that doesn't already match the requested display order */
	if (backward != o.Reverse) {
		slices.Reverse(recs)
	}
	list.Items = make([]openapi.EventlogItem, len(recs))
	for i := range recs {
		var msg string
		msg, err = eventlog_msg(&recs[i], vm_uuid)
		if (err != nil) {
			return list, err
		}
		list.Items[i] = openapi.EventlogItem{
			Ts: recs[i].Ts,
			Class: recs[i].Class,
			Code: recs[i].Code,
			Msg: msg,
		}
	}
	return list, nil
}
