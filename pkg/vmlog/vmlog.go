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

package vmlog

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"suse.com/virtx/pkg/encoding/sbinary"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/ts"
	. "suse.com/virtx/pkg/constants"
)

/*
 * The VM log records both the VirtX operations (Start, End, Complete, StartEnd)
 * and the VM state changes not requested via the API (Event). Operation codes
 * and event codes share one flat namespace, the class being derived from the
 * code (see openapi.LogClass.From_code).
 *
 * Each code has two append-only files in the VM reg dir:
 * <REG_DIR>/<host_uuid>/<vm_uuid>/<code_name>.log
 * <REG_DIR>/<host_uuid>/<vm_uuid>/<code_name>.msg
 *
 * .log stores fixed-size records to keep the file seekable.
 * .msg stores the messages (strings of variable length), newline-terminated,
 * which are referenced by absolute and relative offsets from the records.
 *
 * With Start(), a record is appended, and then with End() or Complete(), the
 * record is updated in place with the result. Event() appends a single record
 * with Te = Ts and state OPERATION_NONE.
 *
 * The msg file is always written and synced before the record that points
 * into it. A crash can therefore leave an unreferenced string behind, which is
 * harmless, but never a record pointing past the end of the strings file.
 *
 * Only the host owning the VM writes these files. Within that host, a
 * per-VM mutex (see vmlog_get_lock) serializes the writers for that VM,
 * while the msg file is written with O_APPEND, which the kernel serializes
 * on its own.
 *
 * A crash or a partial write can leave a partial trailing record, so the log
 * file is not guaranteed to be RECORD_SIZE-aligned. Nothing here assumes it is:
 * readers compute the record count as fi.Size() / RECORD_SIZE, discarding any
 * partial tail, and vmlog_append rounds the same way, so the next record
 * written overwrites those bytes.
 */

/* MSG_MAX is the maximum message length in bytes (room for a trailing newline). */
const MSG_MAX = (4 * KiB) - 1

/*
 * The record size is 32 bytes, so no record spans two blocks.
 *
 * A record refers to two messages, the Start msg and the End msg.
 * Both go into the same .msg file, the end after the start, though not
 * necessarily right after it: concurrent operations could interleave msgs.
 *
 * Msg_start_off is an absolute file offset, and Msg_end_roff, if non-zero,
 * is a relative offset from Msg_start_off.
 */
type record struct {
	Ts int64 /* must stay first: ts_at() reads it at byte offset 0 */
	Te int64
	Code int16 /* operation or event code, so the record self-describes */
	State uint8 /* openapi.OperationState */
	Flags uint8 /* reserved, 0 */
	Msg_start_off int64 /* offset of the start message in the .msg file */
	Msg_end_roff int32 /* offset of the end message, relative to Msg_start_off */
}
/* RECORD_SIZE is the sbinary encoded size of a record. */
const RECORD_SIZE = 8 + 8 + 2 + 1 + 1 + 8 + 4

/* vmlog_ops lists every operation code that ever writes to a VM's log. */
var vmlog_ops = []openapi.OperationCode{
	openapi.OpVmCreate,
	openapi.OpVmUpdate,
	openapi.OpVmDelete,
	openapi.OpVmRegister,
	openapi.OpVmUnregister,
	openapi.OpVmBoot,
	openapi.OpVmShutdown,
	openapi.OpVmPause,
	openapi.OpVmResume,
	openapi.OpVmMigrate,
	openapi.OpVmMigrateAbort,
	openapi.OpVmConsoleVnc,
	openapi.OpVmConsoleSerial,
}

/*
 * vmlog_codes lists every code that can have log files: vmlog_ops, and all
 * the event codes. Used by the "merge all codes" query path, so it knows
 * which files to open without listing the directory.
 */
var vmlog_codes []int16 = func() []int16 {
	var codes []int16
	for _, op := range vmlog_ops {
		codes = append(codes, int16(op))
	}
	for _, code := range openapi.AllowedEventCodeEnumValues {
		codes = append(codes, int16(code))
	}
	return codes
}()

/*
 * Is_valid_filter reports whether class and code (the raw wire values of a
 * List filter, 0 meaning all) are usable: a known class, a code that can have
 * log files, and if both are set, a code belonging to that class.
 */
func Is_valid_filter(class int16, code int16) bool {
	var code_class openapi.LogClass
	if (class != 0 && !openapi.LogClass(class).IsValid()) {
		return false
	}
	if (code != 0 && !slices.Contains(vmlog_codes, code)) {
		return false
	}
	if (class != 0 && code != 0) {
		code_class.From_code(code)
		if (code_class != openapi.LogClass(class)) {
			return false
		}
	}
	return true
}

/* vmlog_class_codes returns the codes of vmlog_codes in class, 0 meaning all. */
func vmlog_class_codes(class int16) []int16 {
	if (class == 0) {
		return vmlog_codes
	}
	var (
		codes []int16
		code_class openapi.LogClass
	)
	for _, code := range vmlog_codes {
		code_class.From_code(code)
		if (code_class == openapi.LogClass(class)) {
			codes = append(codes, code)
		}
	}
	return codes
}

/*
 * vmlog_locks holds one RWMutex per VM, created lazily, so serializing
 * access to one VM's files never blocks another VM's. Only the host owning
 * the VM writes these files: writers (vmlog_append, vmlog_update) take the
 * lock exclusively, readers that can observe a concurrent in-place update
 * or an in-progress append take it for read.
 */
var vmlog_locks sync.Map /* vm_uuid string -> *sync.RWMutex */

func vmlog_get_lock(vm_uuid string) *sync.RWMutex {
	v, _ := vmlog_locks.LoadOrStore(vm_uuid, &sync.RWMutex{})
	return v.(*sync.RWMutex)
}

/* vmlog_forget_vm drops the lock for a VM whose directory has left this host. */
func vmlog_forget_vm(vm_uuid string) {
	vmlog_locks.Delete(vm_uuid)
}

func init() {
	reg.Register_vmdir_leave_callback(vmlog_forget_vm)
}

/* vmlog_code_name returns the name of the files of a code, ie "VmBoot" or "watchdog". */
func vmlog_code_name(code int16) string {
	var code_class openapi.LogClass
	code_class.From_code(code)
	if (code_class == openapi.LOG_CLASS_OP) {
		return openapi.OperationCode(code).String()
	}
	return openapi.EventCode(code).String()
}
func vmlog_file(vm_uuid string, code int16) string {
	return fmt.Sprintf("%s/%s.log", reg.Vmdir(machine.Uuid(), vm_uuid), vmlog_code_name(code))
}
func vmlog_msg_file(vm_uuid string, code int16) string {
	return fmt.Sprintf("%s/%s.msg", reg.Vmdir(machine.Uuid(), vm_uuid), vmlog_code_name(code))
}

/* sanitize_msg collapses whitespace to plain spaces, so a message cannot break the one-message-per-newline layout of a msg file. */
func sanitize_msg(msg string) string {
	return strings.Map(func(r rune) rune {
		if (r >= '\t' && r <= '\r') {
			return ' '
		}
		return r
	}, msg)
}

/*
 * vmlog_append_msg appends msg (sanitized, truncated to MSG_MAX) to the msg
 * file of code, creating it if needed, and returns the offset it was written at.
 * The caller must append msg before writing the record that refers to it.
 */
func vmlog_append_msg(vm_uuid string, code int16, msg string) (int64, error) {
	var (
		err error
		f *os.File
		buf []byte
		offset int64
		path string = vmlog_msg_file(vm_uuid, code)
	)
	msg = sanitize_msg(msg)
	if (len(msg) > MSG_MAX) {
		msg = msg[:MSG_MAX]
	}
	buf = []byte(msg + "\n")
	f, err = os.OpenFile(path, os.O_WRONLY | os.O_CREATE | os.O_APPEND, 0640)
	if (err != nil) {
		return 0, err
	}
	defer f.Close() /* note: double close is harmless in Golang */
	/*
	 * O_APPEND places the write at the end of the file, and leaves this fd
	 * positioned right after it, so SEEK_CUR gives back where it landed.
	 */
	_, err = f.Write(buf)
	if (err != nil) {
		return 0, err
	}
	offset, err = f.Seek(0, io.SeekCurrent)
	if (err != nil) {
		return 0, err
	}
	err = f.Sync()
	if (err != nil) {
		return 0, err
	}
	offset -= int64(len(buf))
	err = f.Close()
	if (err != nil) {
		return 0, err
	}
	/* no dir sync needed for a new file, NFS commits CREATE before replying */
	return offset, nil
}

/* vmlog_read_msg returns the message stored at offset in the open msg file f. */
func vmlog_read_msg(f *os.File, offset int64) (string, error) {
	var (
		err error
		buf []byte = make([]byte, MSG_MAX + 1)
		n, end int
	)
	/* a short read is expected, especially for the last records we don't expect MSG_MAX bytes */
	n, err = f.ReadAt(buf, offset)
	if (err != nil && !errors.Is(err, io.EOF)) {
		return "", err
	}
	end = bytes.IndexByte(buf[:n], '\n')
	if (end < 0) {
		return "", fmt.Errorf("unterminated message at offset %d of %s", offset, f.Name())
	}
	return string(buf[:end]), nil
}

/*
 * vmlog_msg returns the messages of a record: the one logged when the operation
 * started, and the one logged when it ended, if there is one.
 */
func vmlog_msg(rec *record, vm_uuid string) (string, string, error) {
	var (
		err error
		f *os.File
		msgs, msge string
	)
	f, err = os.Open(vmlog_msg_file(vm_uuid, rec.Code))
	if (err != nil) {
		return "", "", err
	}
	defer f.Close()
	msgs, err = vmlog_read_msg(f, rec.Msg_start_off)
	if (err != nil) {
		return "", "", err
	}
	if (rec.Msg_end_roff == 0) {
		return msgs, "", nil
	}
	msge, err = vmlog_read_msg(f, rec.Msg_start_off + int64(rec.Msg_end_roff))
	if (err != nil) {
		return "", "", err
	}
	return msgs, msge, nil
}

func vmlog_read(rec *record, f *os.File, offset int64) error {
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

func vmlog_write(rec *record, f *os.File, offset int64) error {
	var (
		buf [RECORD_SIZE]byte
		err error
	)
	_, err = sbinary.Encode(buf[:], binary.LittleEndian, rec)
	if (err != nil) {
		return err
	}
	_, err = f.WriteAt(buf[:], offset)
	if (err != nil) {
		return err
	}
	return nil
}

/*
 * vmlog_append writes a record at an aligned offset rounding the file size
 * down, creating the file if needed, and returns its offset. A partial write
 * becomes harmless, as the next write will just overwrite the extra bytes.
 * The directory is synced only when this is the very first record (offset 0).
 */
func vmlog_append(rec *record, vm_uuid string) (int64, error) {
	m := vmlog_get_lock(vm_uuid)
	m.Lock()
	defer m.Unlock()
	var (
		err error
		f *os.File
		fi os.FileInfo
		offset int64
		path string = vmlog_file(vm_uuid, rec.Code)
	)
	/* set Ts under the lock so the record order matches write order */
	if (rec.State == uint8(openapi.OPERATION_STARTED) || rec.State == uint8(openapi.OPERATION_NONE)) {
		rec.Ts = ts.Now()
	}
	if (rec.State == uint8(openapi.OPERATION_NONE)) {
		rec.Te = rec.Ts /* an event is a single point in time */
	}
	f, err = os.OpenFile(path, os.O_WRONLY | os.O_CREATE, 0640)
	if (err != nil) {
		return 0, err
	}
	defer f.Close()
	fi, err = f.Stat()
	if (err != nil) {
		return 0, err
	}
	offset = (fi.Size() / RECORD_SIZE) * RECORD_SIZE
	err = vmlog_write(rec, f, offset)
	if (err != nil) {
		return 0, err
	}
	err = f.Sync()
	if (err != nil) {
		return 0, err
	}
	err = f.Close()
	if (err != nil) {
		return 0, err
	}
	/* no dir sync needed for a new file, NFS commits CREATE before replying */
	return offset, nil
}

/*
 * vmlog_open opens the log file of code and returns it along with its record
 * count. A missing file returns a nil File; the caller treats this as an empty
 * log, not an error.
 */
func vmlog_open(vm_uuid string, code int16) (*os.File, int64, error) {
	var (
		err error
		f *os.File
		fi os.FileInfo
	)
	f, err = os.Open(vmlog_file(vm_uuid, code))
	if (err != nil) {
		if (os.IsNotExist(err)) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	fi, err = f.Stat()
	if (err != nil) {
		f.Close()
		return nil, 0, err
	}
	return f, fi.Size() / RECORD_SIZE, nil
}

/*
 * ts_at reads the Ts field stored at the very start (byte offset 0) of the
 * record at index, so binary search never needs to decode the rest of the
 * record just to compare timestamps.
 */
func ts_at(f *os.File, index int64) (int64, error) {
	var buf [8]byte
	_, err := f.ReadAt(buf[:], index * RECORD_SIZE)
	if (err != nil) {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(buf[:])), nil
}

/* bsearch_lo returns the first index in [0, nrec) whose record has Ts >= from, or nrec if there is none. */
func bsearch_lo(f *os.File, nrec int64, from int64) (int64, error) {
	var (
		err error
		t int64
		lo, hi, mid int64
	)
	lo, hi = 0, nrec
	for (lo < hi) {
		mid = (lo + hi) / 2
		t, err = ts_at(f, mid)
		if (err != nil) {
			return 0, err
		}
		if (t < from) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, nil
}

/* bsearch_hi returns the first index in [0, nrec) whose record has Ts > to, or nrec if there is none. */
func bsearch_hi(f *os.File, nrec int64, to int64) (int64, error) {
	var (
		err error
		t int64
		lo, hi, mid int64
	)
	lo, hi = 0, nrec
	for (lo < hi) {
		mid = (lo + hi) / 2
		t, err = ts_at(f, mid)
		if (err != nil) {
			return 0, err
		}
		if (t <= to) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, nil
}

/* vmlog_range resolves [lo, hi), the index range of records whose Ts falls in [from, to], where 0 means unbounded. */
func vmlog_range(f *os.File, nrec int64, from int64, to int64) (int64, int64, error) {
	var (
		err error
		lo, hi int64
	)
	lo = 0
	if (from != 0) {
		lo, err = bsearch_lo(f, nrec, from)
		if (err != nil) {
			return 0, 0, err
		}
	}
	hi = nrec
	if (to != 0) {
		hi, err = bsearch_hi(f, nrec, to)
		if (err != nil) {
			return 0, 0, err
		}
	}
	return lo, hi, nil
}

/*
 * vmlog_fetch_records_code reads up to limit records (0 means no cap) from a
 * single code's log file, restricted to Ts in [from, to] (found via binary
 * search). backward walks most-recent-first; otherwise oldest-first.
 *
 * This is the K == 1 case: a single code, nothing to merge.
 */
func vmlog_fetch_records_code(vm_uuid string, code int16, from int64, to int64, limit int, backward bool) ([]record, error) {
	m := vmlog_get_lock(vm_uuid)
	m.RLock()
	defer m.RUnlock()
	var (
		err error
		f *os.File
		nrec, lo, hi, i int64
		rec record
		recs []record
	)
	f, nrec, err = vmlog_open(vm_uuid, code)
	if (err != nil) {
		return nil, err
	}
	if (f == nil) {
		return nil, nil
	}
	defer f.Close()
	lo, hi, err = vmlog_range(f, nrec, from, to)
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
		err = vmlog_read(&rec, f, i * RECORD_SIZE)
		if (err != nil) {
			return recs, err
		}
		recs = append(recs, rec)
		if (backward) {
			i--
		} else {
			i++
		}
	}
	return recs, nil
}

/*
 * vmlog_cursor walks one log file's records inside [lo, hi), advancing
 * backward (tail-oriented, most-recent-first) or forward (head-oriented,
 * oldest-first). peek caches the next unread record so vmlog_fetch_records_merge
 * can compare cursors without re-reading; has_data reports whether peek
 * holds a valid record or the cursor ran out of range.
 */
type vmlog_cursor struct {
	f *os.File
	lo, hi, i int64
	peek record
	has_data bool
}

func vmlog_cursor_open(vm_uuid string, code int16, from int64, to int64, backward bool) (*vmlog_cursor, error) {
	var (
		err error
		f *os.File
		nrec, lo, hi int64
	)
	f, nrec, err = vmlog_open(vm_uuid, code)
	if (err != nil) {
		return nil, err
	}
	if (f == nil) {
		return nil, nil
	}
	lo, hi, err = vmlog_range(f, nrec, from, to)
	if (err != nil) {
		f.Close()
		return nil, err
	}
	c := &vmlog_cursor{ f: f, lo: lo, hi: hi }
	if (backward) {
		c.i = hi - 1
	} else {
		c.i = lo
	}
	err = c.fill(backward)
	if (err != nil) {
		f.Close()
		return nil, err
	}
	return c, nil
}

func (c *vmlog_cursor) fill(backward bool) error {
	if (backward && c.i < c.lo) {
		c.has_data = false
		return nil
	}
	if (!backward && c.i >= c.hi) {
		c.has_data = false
		return nil
	}
	err := vmlog_read(&c.peek, c.f, c.i * RECORD_SIZE)
	if (err != nil) {
		return err
	}
	c.has_data = true
	return nil
}

func (c *vmlog_cursor) advance(backward bool) error {
	if (backward) {
		c.i--
	} else {
		c.i++
	}
	return c.fill(backward)
}

/*
 * vmlog_fetch_records_merge performs a k-way merge across the log files of
 * codes, each individually Ts-ascending. backward walks from the most recent
 * record of each file toward the oldest (tail-oriented); forward walks oldest
 * toward most recent (head-oriented). Only records with Ts in [from, to] are
 * ever visited: each file's range is found directly via binary search, never
 * by scanning past it.
 *
 * limit caps how many records are returned; 0 means no cap. The result is
 * ordered by the scan direction: backward is most-recent-first, forward is
 * oldest-first.
 */
func vmlog_fetch_records_merge(vm_uuid string, codes []int16, from int64, to int64, limit int, backward bool) ([]record, error) {
	m := vmlog_get_lock(vm_uuid)
	m.RLock()
	defer m.RUnlock()
	var (
		err error
		cursors []*vmlog_cursor
		recs []record
	)
	for _, code := range codes {
		var c *vmlog_cursor
		c, err = vmlog_cursor_open(vm_uuid, code, from, to, backward)
		if (err != nil) {
			for _, c2 := range cursors {
				c2.f.Close()
			}
			return nil, err
		}
		if (c != nil) {
			cursors = append(cursors, c)
		}
	}
	defer func() {
		for _, c := range cursors {
			c.f.Close()
		}
	}()
	comes_before := func(a *record, b *record) bool {
		if (backward) {
			return a.Ts > b.Ts
		}
		return a.Ts < b.Ts
	}
	for (limit == 0 || len(recs) < limit) {
		var next *vmlog_cursor
		for _, c := range cursors {
			if (!c.has_data) {
				continue
			}
			if (next == nil || comes_before(&c.peek, &next.peek)) {
				next = c
			}
		}
		if (next == nil) {
			break
		}
		recs = append(recs, next.peek)
		err = next.advance(backward)
		if (err != nil) {
			return recs, err
		}
	}
	return recs, nil
}

/*
 * vmlog_fetch_records resolves the record set for a query: when code != 0 it
 * reads that single code's file (vmlog_fetch_records_code), skipping the
 * merge machinery entirely; when code == 0 it merges the files of all codes
 * in class, 0 meaning all classes (vmlog_fetch_records_merge).
 *
 * head and tail are mutually exclusive. reverse selects the display order:
 * false is chronological (oldest first), true is reverse chronological
 * (newest first).
 */
func vmlog_fetch_records(vm_uuid string, class int16, code int16, from int64, to int64, head int, tail int, reverse bool) ([]record, error) {
	if (head != 0 && tail != 0) {
		return nil, errors.New("vmlog: head and tail are mutually exclusive")
	}
	var (
		err error
		recs []record
		backward bool
		limit int
	)
	switch {
	case (tail != 0):
		backward, limit = true, tail
	case (head != 0):
		backward, limit = false, head
	default:
		backward, limit = true, 0
	}
	if (code != 0) {
		recs, err = vmlog_fetch_records_code(vm_uuid, code, from, to, limit, backward)
	} else {
		recs, err = vmlog_fetch_records_merge(vm_uuid, vmlog_class_codes(class), from, to, limit, backward)
	}
	if (err != nil) {
		return nil, err
	}
	/* backward yields most-recent-first, forward yields oldest-first; flip
	 * only when that doesn't already match the requested display order */
	if (backward != reverse) {
		slices.Reverse(recs)
	}
	return recs, nil
}

/*
 * vmlog_last reads the last record of code into rec and returns its offset, or -1 if there
 * is none. Older records are never considered: a newer operation supersedes a STARTED record
 * which was never ended (ie a vm_shutdown via ACPI ignored by the guest).
 */
func vmlog_last(vm_uuid string, code int16, rec *record) (int64, error) {
	m := vmlog_get_lock(vm_uuid)
	m.RLock()
	defer m.RUnlock()
	var (
		err error
		f *os.File
		nrec int64
	)
	f, nrec, err = vmlog_open(vm_uuid, code)
	if (err != nil) {
		return -1, err
	}
	if (f == nil) {
		return -1, nil
	}
	defer f.Close()
	if (nrec == 0) {
		return -1, nil
	}
	err = vmlog_read(rec, f, (nrec - 1) * RECORD_SIZE)
	if (err != nil) {
		return -1, err
	}
	return (nrec - 1) * RECORD_SIZE, nil
}

/* vmlog_update fills in the result of the operation started at offset. */
func vmlog_update(vm_uuid string, code int16, state uint8, msg string, offset int64, te int64) error {
	m := vmlog_get_lock(vm_uuid)
	m.Lock()
	defer m.Unlock()
	var (
		err error
		f *os.File
		rec record
		msg_off int64
	)
	f, err = os.OpenFile(vmlog_file(vm_uuid, code), os.O_RDWR, 0)
	if (err != nil) {
		return err
	}
	defer f.Close() /* double close is fine in Golang */
	err = vmlog_read(&rec, f, offset)
	if (err != nil) {
		return err
	}
	rec.State = state
	rec.Te = te
	if (msg != "") {
		msg_off, err = vmlog_append_msg(vm_uuid, code, msg)
		if (err != nil) {
			return err
		}
		rec.Msg_end_roff = int32(msg_off - rec.Msg_start_off)
	}
	err = vmlog_write(&rec, f, offset)
	if (err != nil) {
		return err
	}
	err = f.Sync()
	if (err != nil) {
		return err
	}
	err = f.Close()
	if (err != nil) {
		return err
	}
	return nil
}

/*
 * Start records the beginning of an operation. It appends a STARTED record to
 * the local host's per-VM per-op log and returns the byte offset of that
 * record. The caller passes this offset to End when the operation finishes,
 * so the record can be updated in place.
 */
func Start(vm_uuid string, op openapi.OperationCode, client_ip string, msg string) (int64, error) {
	var (
		err error
		msg_off int64
	)
	msg = fmt.Sprintf("[%s] ", client_ip) + msg
	msg_off, err = vmlog_append_msg(vm_uuid, int16(op), msg)
	if (err != nil) {
		return 0, err
	}
	rec := record{
		Code: int16(op),
		State: uint8(openapi.OPERATION_STARTED),
		Msg_start_off: msg_off,
	}
	return vmlog_append(&rec, vm_uuid)
}

/*
 * StartEnd appends a single already-finished record for operations that cannot
 * write a STARTED record first (ie VmCreate): no Vmdir exists at that time.
 * ts_start should be captured by the caller at the real start time, so that the
 * information is preserved and passed here.
 */
func StartEnd(vm_uuid string, op openapi.OperationCode, state openapi.OperationState, client_ip string, msgs string, msge string, ts_start int64) error {
	var (
		err error
		msgs_off, msge_off int64
	)
	msgs = fmt.Sprintf("[%s] ", client_ip) + msgs
	msgs_off, err = vmlog_append_msg(vm_uuid, int16(op), msgs)
	if (err != nil) {
		return err
	}
	rec := record{
		Ts: ts_start,
		Te: ts.Now(),
		Code: int16(op),
		State: uint8(state),
		Msg_start_off: msgs_off,
	}
	if (msge != "") {
		msge_off, err = vmlog_append_msg(vm_uuid, int16(op), msge)
		if (err != nil) {
			return err
		}
		rec.Msg_end_roff = int32(msge_off - msgs_off)
	}
	_, err = vmlog_append(&rec, vm_uuid)
	return err
}

/*
 * End updates the STARTED record at the given offset with the final outcome.
 * state should be OPERATION_COMPLETED or OPERATION_FAILED.
 * msg is stored after the message the STARTED record already has.
 */
func End(vm_uuid string, op openapi.OperationCode, state openapi.OperationState, msg string, offset int64) error {
	return vmlog_update(vm_uuid, int16(op), uint8(state), msg, offset, ts.Now())
}

/*
 * Complete is used by the libvirt lifecycle event handler, which knows only
 * the operation type and a completion message, not the record offset. It updates
 * the last record of op for this VM on the local host, which must be STARTED.
 * msg is stored after the message the STARTED record already has.
 */
func Complete(vm_uuid string, op openapi.OperationCode, msg string) error {
	var (
		err error
		offset int64
		rec record
	)
	offset, err = vmlog_last(vm_uuid, int16(op), &rec)
	if (err != nil) {
		return err
	}
	if (offset < 0 || rec.State != uint8(openapi.OPERATION_STARTED)) {
		return errors.New("vmlog.Complete: no pending started record found")
	}
	return vmlog_update(vm_uuid, int16(op), uint8(openapi.OPERATION_COMPLETED), msg, offset, ts.Now())
}

/*
 * Pending returns whether the last record of op for this VM on the local host is STARTED.
 * Used by the libvirt event handlers to tell the outcome of an operation from an event
 * not requested via the VirtX API (ie a guest-initiated shutdown).
 */
func Pending(vm_uuid string, op openapi.OperationCode) (bool, error) {
	var (
		err error
		offset int64
		rec record
	)
	offset, err = vmlog_last(vm_uuid, int16(op), &rec)
	if (err != nil) {
		return false, err
	}
	return (offset >= 0 && rec.State == uint8(openapi.OPERATION_STARTED)), nil
}

/*
 * Load_last reads the last record for the given operation type on the local
 * host. Used by Get_migration_info and Abort_migration.
 */
func Load_last(vm_uuid string, op openapi.OperationCode, state *openapi.OperationState, msgs *string, msge *string, ts_start *int64, ts_end *int64) error {
	var (
		err error
		offset int64
		rec record
	)
	offset, err = vmlog_last(vm_uuid, int16(op), &rec)
	if (err != nil) {
		return err
	}
	if (offset < 0) {
		return errors.New("vmlog.Load_last: no records found")
	}
	*state = openapi.OperationState(rec.State)
	*ts_start = rec.Ts
	*ts_end = rec.Te
	*msgs, *msge, err = vmlog_msg(&rec, vm_uuid)
	return err
}

/*
 * Event appends an entry for a VM state change not requested via the VirtX
 * API. Used by the libvirt event handlers. The class is derived from code,
 * which must be an error or guest event code.
 */
func Event(vm_uuid string, code openapi.EventCode, msg string) error {
	var (
		err error
		msg_off int64
		class openapi.LogClass
	)
	class.From_code(int16(code))
	if (!code.IsValid() || (class != openapi.LOG_CLASS_ERROR && class != openapi.LOG_CLASS_GUEST)) {
		return fmt.Errorf("vmlog.Event: invalid event code %d", code)
	}
	msg_off, err = vmlog_append_msg(vm_uuid, int16(code), msg)
	if (err != nil) {
		return err
	}
	rec := record{
		Code: int16(code),
		State: uint8(openapi.OPERATION_NONE),
		Msg_start_off: msg_off,
	}
	_, err = vmlog_append(&rec, vm_uuid)
	return err
}

/*
 * List returns the log entries matching the passed options.
 */
func List(vm_uuid string, o *openapi.VmLogListOptions) (openapi.LogList, error) {
	var (
		err error
		recs []record
		list openapi.LogList
	)
	recs, err = vmlog_fetch_records(vm_uuid, o.Class, o.Code, o.From, o.To, int(o.Head), int(o.Tail), o.Reverse)
	if (err != nil) {
		return list, err
	}
	list.Items = make([]openapi.LogItem, len(recs))
	for i := range recs {
		var msgs, msge string
		msgs, msge, err = vmlog_msg(&recs[i], vm_uuid)
		if (err != nil) {
			return list, err
		}
		list.Items[i] = openapi.LogItem{
			Ts: recs[i].Ts,
			Te: recs[i].Te,
			Code: recs[i].Code,
			State: openapi.OperationState(recs[i].State),
			Msgs: msgs,
			Msge: msge,
		}
	}
	return list, nil
}
