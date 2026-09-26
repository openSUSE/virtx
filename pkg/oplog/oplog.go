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

package oplog

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
 * Each operation type for a VM has two append-only files:
 * <REG_DIR>/<host_uuid>/<vm_uuid>/<op_string>.oplog
 * <REG_DIR>/<host_uuid>/<vm_uuid>/<op_string>.msg
 *
 * .oplog stores fixed-size Operation records to keep the oplog file seekable.
 * .msg stores the messages (strings of variable length), newline-terminated,
 * which are referenced by absolute and relative offsets from the oplog records.
 *
 * With Start(), a record is appended to the oplog, and then with End() or
 * Complete(), the record is updated in place with the result.
 *
 * The msg file is always written and synced before the record that points
 * into it. A crash can therefore leave an unreferenced string behind, which is
 * harmless, but never a record pointing past the end of the strings file.
 *
 * Only the host owning the VM writes these files. Within that host, a
 * per-VM mutex (see oplog_get_lock) serializes the oplog writers for that
 * VM, while the msg file is written with O_APPEND, which the kernel
 * serializes on its own.
 *
 * A crash or a partial write can leave a partial trailing record, so the oplog
 * file is not guaranteed to be RECORD_SIZE-aligned. Nothing here assumes it is:
 * readers compute the record count as fi.Size() / RECORD_SIZE, discarding any
 * partial tail, and oplog_append rounds the same way, so the next record
 * written overwrites those bytes.
 */

/*
 * FIND_LIMIT caps the backward scan in oplog_find_last_state.
 */
const FIND_LIMIT = 256

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
	Ts int64 /* must stay first: reglog.Range() reads it at byte offset 0 */
	Te int64
	Op openapi.OperationCode /* the operation code, so the record self-describes */
	State openapi.OperationState
	Msg_start_off int64 /* offset of the start message in the .msg file */
	Msg_end_roff int32 /* offset of the end message, relative to Msg_start_off */
}
/* RECORD_SIZE is the sbinary encoded size of a record. */
const RECORD_SIZE = 2 + 2 + 8 + 8 + 8 + 4

/*
 * oplog_ops lists every operation code that ever writes to a VM's oplog.
 * Used by the "merge every operation type" query path, so it knows which
 * per-type files to open without listing the directory.
 */
var oplog_ops = []openapi.OperationCode{
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
 * Is_valid_op reports whether op (the raw wire value of a List filter) is
 * usable: 0 (match every operation type) or one of oplog_ops (an operation
 * code that actually writes oplog records). Other operation codes are
 * "valid" in the wider API sense but never produce oplog files, so they are
 * not valid here.
 */
func Is_valid_op(op int16) bool {
	return op == 0 || slices.Contains(oplog_ops, openapi.OperationCode(op))
}

/*
 * oplog_locks holds one RWMutex per VM, created lazily, so serializing
 * access to one VM's files never blocks another VM's. Only the host owning
 * the VM writes these files: writers (oplog_append, oplog_update) take the
 * lock exclusively, readers that can observe a concurrent in-place update
 * or an in-progress append take it for read.
 */
var oplog_locks reglog.LockMap

func oplog_get_lock(vm_uuid string) *sync.RWMutex {
	return oplog_locks.Get(vm_uuid)
}

func init() {
	/* drops the lock for a VM whose directory has left this host */
	reg.Register_vmdir_leave_callback(oplog_locks.Delete)
}

func oplog_file(vm_uuid string, op openapi.OperationCode) string {
	return fmt.Sprintf("%s/%s.oplog", reglog.Dir(vm_uuid), op.String())
}
func oplog_msg_file(vm_uuid string, op openapi.OperationCode) string {
	return fmt.Sprintf("%s/%s.msg", reglog.Dir(vm_uuid), op.String())
}

/*
 * oplog_append_msg appends a message to the strings file and returns the
 * offset it was written at.
 * The caller must append msg before writing the oplog record to disk.
 */
func oplog_append_msg(vm_uuid string, op openapi.OperationCode, msg string) (int64, error) {
	return reglog.Append_msg(oplog_msg_file(vm_uuid, op), msg)
}

/*
 * oplog_msg returns the messages of a record: the one logged when the operation
 * started, and the one logged when it ended, if there is one.
 */
func oplog_msg(rec *record, vm_uuid string) (string, string, error) {
	var (
		err error
		f *os.File
		msgs, msge string
	)
	f, err = os.Open(oplog_msg_file(vm_uuid, rec.Op))
	if (err != nil) {
		return "", "", err
	}
	defer f.Close()
	msgs, err = reglog.Read_msg(f, rec.Msg_start_off)
	if (err != nil) {
		return "", "", err
	}
	if (rec.Msg_end_roff == 0) {
		return msgs, "", nil
	}
	msge, err = reglog.Read_msg(f, rec.Msg_start_off + int64(rec.Msg_end_roff))
	if (err != nil) {
		return "", "", err
	}
	return msgs, msge, nil
}

func oplog_read(rec *record, f *os.File, offset int64) error {
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

func oplog_write(rec *record, f *os.File, offset int64) error {
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
 * oplog_append writes a record at an aligned offset rounding the file size
 * down, and returns its offset. A partial write becomes harmless, as the next
 * write will just overwrite the extra bytes.
 */
func oplog_append(rec *record, vm_uuid string) (int64, error) {
	m := oplog_get_lock(vm_uuid)
	m.Lock()
	defer m.Unlock()
	var (
		err error
		buf [RECORD_SIZE]byte
	)
	/* set Ts under the lock so the record order matches write order */
	if (rec.State == openapi.OPERATION_STARTED) {
		rec.Ts = ts.Now()
	}
	_, err = sbinary.Encode(buf[:], binary.LittleEndian, rec)
	if (err != nil) {
		return 0, err
	}
	return reglog.Append_record(oplog_file(vm_uuid, rec.Op), buf[:], RECORD_SIZE)
}

/*
 * oplog_open opens the oplog file (the one indicated by op) and returns it along
 * with its current record count. A missing file returns a nil File; the
 * caller treats this as an empty log, not an error.
 */
func oplog_open(vm_uuid string, op openapi.OperationCode) (*os.File, int64, error) {
	return reglog.Open_log(oplog_file(vm_uuid, op), RECORD_SIZE)
}

/*
 * oplog_range resolves [lo, hi), the index range of records in f
 * whose start timestamps fall in [from, to], where 0 means unbounded.
 */
func oplog_range(f *os.File, nrec int64, from int64, to int64) (int64, int64, error) {
	return reglog.Range(f, RECORD_SIZE, nrec, from, to)
}

/*
 * oplog_fetch_records_op reads up to limit records (0 means no cap) from a
 * single operation's oplog file, restricted to Ts in [from, to] (found via
 * binary search). backward walks most-recent-first; otherwise oldest-first.
 *
 * This is the K == 1 case: a single operation code, nothing to merge.
 *
 * Possible future optimization: here (unlike the multi-file merge) we
 * already know the exact matching range [lo, hi) up front, so we could skip
 * walking in "backward" order at all and instead just read the right slice
 * of records directly in whatever order the caller wants. That would let
 * this function honor the caller's requested display order by itself,
 * instead of relying on oplog_fetch_records's separate reversal step.
 */
func oplog_fetch_records_op(vm_uuid string, op openapi.OperationCode, from int64, to int64, limit int, backward bool) ([]record, error) {
	m := oplog_get_lock(vm_uuid)
	m.RLock()
	defer m.RUnlock()
	var (
		err error
		f *os.File
		nrec, lo, hi, i int64
		rec record
		recs []record
	)
	f, nrec, err = oplog_open(vm_uuid, op)
	if (err != nil) {
		return nil, err
	}
	if (f == nil) {
		return nil, nil
	}
	defer f.Close()
	lo, hi, err = oplog_range(f, nrec, from, to)
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
		err = oplog_read(&rec, f, i * RECORD_SIZE)
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
 * oplog_cursor walks one oplog file's records inside [lo, hi), advancing
 * backward (tail-oriented, most-recent-first) or forward (head-oriented,
 * oldest-first). peek caches the next unread record so oplog_fetch_records_all
 * can compare cursors without re-reading; has_data reports whether peek
 * holds a valid record or the cursor ran out of range.
 */
type oplog_cursor struct {
	f *os.File
	lo, hi, i int64
	peek record
	has_data bool
}

func oplog_cursor_open(vm_uuid string, op openapi.OperationCode, from int64, to int64, backward bool) (*oplog_cursor, error) {
	var (
		err error
		f *os.File
		nrec, lo, hi int64
	)
	f, nrec, err = oplog_open(vm_uuid, op)
	if (err != nil) {
		return nil, err
	}
	if (f == nil) {
		return nil, nil
	}
	lo, hi, err = oplog_range(f, nrec, from, to)
	if (err != nil) {
		f.Close()
		return nil, err
	}
	c := &oplog_cursor{ f: f, lo: lo, hi: hi }
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

func (c *oplog_cursor) fill(backward bool) error {
	if (backward && c.i < c.lo) {
		c.has_data = false
		return nil
	}
	if (!backward && c.i >= c.hi) {
		c.has_data = false
		return nil
	}
	err := oplog_read(&c.peek, c.f, c.i * RECORD_SIZE)
	if (err != nil) {
		return err
	}
	c.has_data = true
	return nil
}

func (c *oplog_cursor) advance(backward bool) error {
	if (backward) {
		c.i--
	} else {
		c.i++
	}
	return c.fill(backward)
}

/*
 * oplog_fetch_records_all performs a k-way merge across the oplog files of
 * every known operation code (oplog_ops), each individually Ts-ascending.
 * backward walks from the most recent record of each file toward the
 * oldest (tail-oriented); forward walks oldest toward most recent
 * (head-oriented). Only records with Ts in [from, to] are ever visited:
 * each file's range is found directly via binary search, never by scanning
 * past it.
 *
 * limit caps how many records are returned; 0 means no cap. The result is
 * ordered by the scan direction: backward is most-recent-first, forward is
 * oldest-first.
 */
func oplog_fetch_records_all(vm_uuid string, from int64, to int64, limit int, backward bool) ([]record, error) {
	m := oplog_get_lock(vm_uuid)
	m.RLock()
	defer m.RUnlock()
	var (
		err error
		cursors []*oplog_cursor
		recs []record
	)
	for _, op := range oplog_ops {
		var c *oplog_cursor
		c, err = oplog_cursor_open(vm_uuid, op, from, to, backward)
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
		var next *oplog_cursor
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
 * oplog_fetch_records resolves the record set for a query: when op != 0 it
 * reads that single operation's file (oplog_fetch_records_op), skipping the
 * merge machinery entirely, since there is nothing to merge; when op == 0 it
 * merges every known operation type (oplog_fetch_records_all).
 *
 * head and tail are mutually exclusive. reverse selects the display order:
 * false is chronological (oldest first), true is reverse chronological
 * (newest first).
 */
func oplog_fetch_records(vm_uuid string, op openapi.OperationCode, from int64, to int64, head int, tail int, reverse bool) ([]record, error) {
	if (head != 0 && tail != 0) {
		return nil, errors.New("oplog: head and tail are mutually exclusive")
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
	if (op != 0) {
		recs, err = oplog_fetch_records_op(vm_uuid, op, from, to, limit, backward)
	} else {
		recs, err = oplog_fetch_records_all(vm_uuid, from, to, limit, backward)
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

func oplog_find_last_state(vm_uuid string, op openapi.OperationCode, state openapi.OperationState) (int64, error) {
	m := oplog_get_lock(vm_uuid)
	m.RLock()
	defer m.RUnlock()
	var (
		err error
		f *os.File
		rec record
		nrec, i int64
	)
	f, nrec, err = oplog_open(vm_uuid, op)
	if (err != nil) {
		return -1, err
	}
	if (f == nil) {
		return -1, nil
	}
	defer f.Close()
	limit := nrec - FIND_LIMIT
	if (limit < 0) {
		limit = 0
	}
	for i = nrec - 1; (i >= limit); i-- {
		err = oplog_read(&rec, f, i * RECORD_SIZE)
		if (err != nil) {
			return -1, err
		}
		if (rec.State == state) {
			return i * RECORD_SIZE, nil
		}
	}
	return -1, nil
}

/* oplog_update fills in the result of the operation started at offset. */
func oplog_update(vm_uuid string, op openapi.OperationCode, state openapi.OperationState, msg string, offset int64, te int64) error {
	m := oplog_get_lock(vm_uuid)
	m.Lock()
	defer m.Unlock()
	var (
		err error
		f *os.File
		rec record
		msg_off int64
	)
	f, err = os.OpenFile(oplog_file(vm_uuid, op), os.O_RDWR, 0)
	if (err != nil) {
		return err
	}
	defer f.Close() /* double close is fine in Golang */
	err = oplog_read(&rec, f, offset)
	if (err != nil) {
		return err
	}
	rec.State = state
	rec.Te = te
	if (msg != "") {
		msg_off, err = oplog_append_msg(vm_uuid, op, msg)
		if (err != nil) {
			return err
		}
		rec.Msg_end_roff = int32(msg_off - rec.Msg_start_off)
	}
	err = oplog_write(&rec, f, offset)
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

func oplog_head(vm_uuid string, op openapi.OperationCode, n int) ([]record, error) {
	m := oplog_get_lock(vm_uuid)
	m.RLock()
	defer m.RUnlock()
	var (
		err error
		f *os.File
		rec record
		nrec, end, i int64
		recs []record
	)
	f, nrec, err = oplog_open(vm_uuid, op)
	if (err != nil) {
		return nil, err
	}
	if (f == nil) {
		return nil, nil
	}
	defer f.Close()
	end = int64(n)
	if (end > nrec) {
		end = nrec
	}
	for i = 0; (i < end); i++ {
		err = oplog_read(&rec, f, i * RECORD_SIZE)
		if (err != nil) {
			return recs, err
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

func oplog_tail(vm_uuid string, op openapi.OperationCode, n int) ([]record, error) {
	m := oplog_get_lock(vm_uuid)
	m.RLock()
	defer m.RUnlock()
	var (
		err error
		f *os.File
		rec record
		nrec, start, i int64
		recs []record
	)
	f, nrec, err = oplog_open(vm_uuid, op)
	if (err != nil) {
		return nil, err
	}
	if (f == nil) {
		return nil, nil
	}
	defer f.Close() /* double close ok in Golang */
	start = nrec - int64(n)
	if (start < 0) {
		start = 0
	}
	for i = nrec - 1; (i >= start); i-- {
		err = oplog_read(&rec, f, i * RECORD_SIZE)
		if (err != nil) {
			return recs, err
		}
		recs = append(recs, rec)
	}
	return recs, nil
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
	msg_off, err = oplog_append_msg(vm_uuid, op, msg)
	if (err != nil) {
		return 0, err
	}
	rec := record{
		Ts: 0,
		Te: 0,
		Op: op,
		State: openapi.OPERATION_STARTED,
		Msg_start_off: msg_off,
		Msg_end_roff: 0,
	}
	return oplog_append(&rec, vm_uuid)
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
	msgs_off, err = oplog_append_msg(vm_uuid, op, msgs)
	if (err != nil) {
		return err
	}
	rec := record{
		Ts: ts_start,
		Te: ts.Now(),
		Op: op,
		State: state,
		Msg_start_off: msgs_off,
		Msg_end_roff: 0,
	}
	if (msge != "") {
		msge_off, err = oplog_append_msg(vm_uuid, op, msge)
		if (err != nil) {
			return err
		}
		rec.Msg_end_roff = int32(msge_off - msgs_off)
	}
	_, err = oplog_append(&rec, vm_uuid)
	return err
}

/*
 * End updates the STARTED record at the given offset with the final outcome.
 * state should be OPERATION_COMPLETED or OPERATION_FAILED.
 * msg is stored after the message the STARTED record already has.
 */
func End(vm_uuid string, op openapi.OperationCode, state openapi.OperationState, msg string, offset int64) error {
	return oplog_update(vm_uuid, op, state, msg, offset, ts.Now())
}

/*
 * Complete is used by the libvirt lifecycle event handler, which knows only
 * the operation type and a completion message, not the record offset. It finds
 * the last STARTED record for this VM on the local host and updates it.
 * msg is stored after the message the STARTED record already has.
 */
func Complete(vm_uuid string, op openapi.OperationCode, msg string) error {
	var (
		err error
		offset int64
	)
	offset, err = oplog_find_last_state(vm_uuid, op, openapi.OPERATION_STARTED)
	if (err != nil) {
		return err
	}
	if (offset < 0) {
		return errors.New("oplog.Complete: no pending started record found")
	}
	return oplog_update(vm_uuid, op, openapi.OPERATION_COMPLETED, msg, offset, ts.Now())
}

/*
 * Load_last reads the last record for the given operation type on the local
 * host. Used by Get_migration_info and Abort_migration.
 */
func Load_last(vm_uuid string, op openapi.OperationCode, state *openapi.OperationState, msgs *string, msge *string, ts_start *int64, ts_end *int64) error {
	var (
		recs []record
		err error
	)
	recs, err = oplog_tail(vm_uuid, op, 1)
	if (err != nil) {
		return err
	}
	if (len(recs) == 0) {
		return errors.New("oplog.Load_last: no records found")
	}
	*state = recs[0].State
	*ts_start = recs[0].Ts
	*ts_end = recs[0].Te
	*msgs, *msge, err = oplog_msg(&recs[0], vm_uuid)
	return err
}

/*
 * List returns the oplog entries matching the passed options.
 */
func List(vm_uuid string, o *openapi.VmOplogListOptions) (openapi.OplogList, error) {
	var (
		err error
		recs []record
		list openapi.OplogList
	)
	recs, err = oplog_fetch_records(vm_uuid, openapi.OperationCode(o.Op), o.From, o.To, int(o.Head), int(o.Tail), o.Reverse)
	if (err != nil) {
		return list, err
	}
	list.Items = make([]openapi.OplogItem, len(recs))
	for i := range recs {
		var msgs, msge string
		msgs, msge, err = oplog_msg(&recs[i], vm_uuid)
		if (err != nil) {
			return list, err
		}
		list.Items[i] = openapi.OplogItem{
			Ts: recs[i].Ts,
			Te: recs[i].Te,
			Op: int16(recs[i].Op),
			State: recs[i].State,
			Msgs: msgs,
			Msge: msge,
		}
	}
	return list, nil
}
