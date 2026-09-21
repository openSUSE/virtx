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
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
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
 * Only the host owning the VM writes these files. Within that host oplog_m
 * serializes the oplog writers, while the msg file is written with O_APPEND,
 * which the kernel serializes on its own.
 *
 * A crash or a partial write can leave a partial trailing record, so the oplog
 * file is not guaranteed to be RECORD_SIZE-aligned. Nothing here assumes it is:
 * readers compute the record count as fi.Size() / RECORD_SIZE, discarding any
 * partial tail, and oplog_append rounds the same way, so the next record
 * written overwrites those bytes.
 */

/*
 * FIND_LIMIT caps the backward scan in oplog_find_last_status.
 */
const FIND_LIMIT = 256

/*
 * MSG_MAX is the maximum message length in bytes.
 * libvirt has a theoretical limit of 4MiB, but realistically most messages
 * never even reach 1K. We make it 4K, should be ok for all real use cases.
 *
 * Note that it's 4K - 1 because we want to add a newline.
 */
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
	Status openapi.OperationState
	Ts int64
	Te int64
	Msg_start_off int64 /* offset of the start message in the .msg file */
	Msg_end_roff int32 /* offset of the end message, relative to Msg_start_off */
	Reserved int16 /* unused, keeps the record at 32 bytes */
}
/* RECORD_SIZE is the sbinary encoded size of a record. */
const RECORD_SIZE = 2 + 8 + 8 + 8 + 4 + 2

/*
 * oplog_m serializes the writers, which compute the offset they write at from
 * the current file size. Only the host owning the VM writes these files, so
 * serializing the local ones is enough.
 */
var oplog_m sync.Mutex

func oplog_dir(vm_uuid string) string {
	return reg.Vmdir(machine.Uuid(), vm_uuid)
}
func oplog_syncdir(vm_uuid string) error {
	return reg.Syncdir(oplog_dir(vm_uuid))
}
func oplog_file(vm_uuid string, op openapi.Operation) string {
	return fmt.Sprintf("%s/%s.oplog", oplog_dir(vm_uuid), op.String())
}
func oplog_msg_file(vm_uuid string, op openapi.Operation) string {
	return fmt.Sprintf("%s/%s.msg", oplog_dir(vm_uuid), op.String())
}

/*
 * oplog_sanitize_msg collapses whitespace to plain spaces, so that a message
 * cannot break the one message per newline layout of the .msg file
 */
func oplog_sanitize_msg(msg string) string {
	return strings.Map(func(r rune) rune {
		if (r >= '\t' && r <= '\r') {
			return ' '
		}
		return r
	}, msg)
}

/*
 * oplog_append_msg appends a message to the strings file and returns the
 * offset it was written at.
 * The caller must append msg before writing the oplog record to disk.
 */
func oplog_append_msg(vm_uuid string, op openapi.Operation, msg string) (int64, error) {
	var (
		err error
		f *os.File
		buf []byte
		offset int64
	)
	msg = oplog_sanitize_msg(msg)
	if (len(msg) > MSG_MAX) {
		msg = msg[:MSG_MAX]
	}
	buf = []byte(msg + "\n")
	f, err = os.OpenFile(oplog_msg_file(vm_uuid, op), os.O_WRONLY | os.O_CREATE | os.O_APPEND, 0640)
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
	/* writing at offset 0 can mean the file was just created so add a syncdir */
	if (offset == 0) {
		err = oplog_syncdir(vm_uuid)
		if (err != nil) {
			return 0, err
		}
	}
	return offset, nil
}

/* oplog_read_msg returns the message stored at offset in the strings file. */
func oplog_read_msg(f *os.File, offset int64) (string, error) {
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
 * oplog_msg returns the messages of a record: the one logged when the operation
 * started, and the one logged when it ended, if there is one.
 */
func oplog_msg(vm_uuid string, op openapi.Operation, rec *record) (string, string, error) {
	var (
		err error
		f *os.File
		msgs, msge string
	)
	f, err = os.Open(oplog_msg_file(vm_uuid, op))
	if (err != nil) {
		return "", "", err
	}
	defer f.Close()
	msgs, err = oplog_read_msg(f, rec.Msg_start_off)
	if (err != nil) {
		return "", "", err
	}
	if (rec.Msg_end_roff == 0) {
		return msgs, "", nil
	}
	msge, err = oplog_read_msg(f, rec.Msg_start_off + int64(rec.Msg_end_roff))
	if (err != nil) {
		return "", "", err
	}
	return msgs, msge, nil
}

func oplog_read(rec *record, f *os.File, offset int64) error {
	var (
		buf []byte = make([]byte, RECORD_SIZE)
		err error
	)
	_, err = f.ReadAt(buf, offset)
	if (err != nil) {
		return err
	}
	_, err = sbinary.Decode(buf, binary.LittleEndian, rec)
	if (err != nil) {
		return err
	}
	return nil
}

func oplog_write(rec *record, f *os.File, offset int64) error {
	var (
		buf []byte = make([]byte, RECORD_SIZE)
		err error
	)
	_, err = sbinary.Encode(buf, binary.LittleEndian, rec)
	if (err != nil) {
		return err
	}
	_, err = f.WriteAt(buf, offset)
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
func oplog_append(rec *record, vm_uuid string, op openapi.Operation) (int64, error) {
	oplog_m.Lock()
	defer oplog_m.Unlock()
	var (
		err error
		f *os.File
		fi os.FileInfo
		buf []byte = make([]byte, RECORD_SIZE)
		offset int64
	)
	_, err = sbinary.Encode(buf, binary.LittleEndian, rec)
	if (err != nil) {
		return 0, err
	}
	f, err = os.OpenFile(oplog_file(vm_uuid, op), os.O_WRONLY | os.O_CREATE, 0640)
	if (err != nil) {
		return 0, err
	}
	defer f.Close()
	fi, err = f.Stat()
	if (err != nil) {
		return 0, err
	}
	offset = (fi.Size() / RECORD_SIZE) * RECORD_SIZE
	_, err = f.WriteAt(buf, offset)
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
	/* writing at offset 0 means the file was just created, or is still empty */
	if (offset == 0) {
		err = oplog_syncdir(vm_uuid)
		if (err != nil) {
			return 0, err
		}
	}
	return offset, nil
}

func oplog_find_last_status(vm_uuid string, op openapi.Operation, status openapi.OperationState) (int64, error) {
	var (
		err error
		f *os.File
		fi os.FileInfo
		rec record
		nrec, i int64
	)
	f, err = os.Open(oplog_file(vm_uuid, op))
	if (err != nil) {
		if (os.IsNotExist(err)) {
			return -1, nil
		}
		return -1, err
	}
	defer f.Close()
	fi, err = f.Stat()
	if (err != nil) {
		return -1, err
	}
	nrec = fi.Size() / RECORD_SIZE
	limit := nrec - FIND_LIMIT
	if (limit < 0) {
		limit = 0
	}
	for i = nrec - 1; (i >= limit); i-- {
		err = oplog_read(&rec, f, i * RECORD_SIZE)
		if (err != nil) {
			return -1, err
		}
		if (rec.Status == status) {
			return i * RECORD_SIZE, nil
		}
	}
	return -1, nil
}

/* oplog_update fills in the result of the operation started at offset. */
func oplog_update(vm_uuid string, op openapi.Operation, status openapi.OperationState, msg string, offset int64, te int64) error {
	oplog_m.Lock()
	defer oplog_m.Unlock()
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
	rec.Status = status
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

func oplog_tail(vm_uuid string, op openapi.Operation, n int) ([]record, error) {
	var (
		err error
		f *os.File
		fi os.FileInfo
		rec record
		nrec, start, i int64
		recs []record
	)
	f, err = os.Open(oplog_file(vm_uuid, op))
	if (err != nil) {
		if (os.IsNotExist(err)) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close() /* double close ok in Golang */
	fi, err = f.Stat()
	if (err != nil) {
		return nil, err
	}
	nrec = fi.Size() / RECORD_SIZE
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
func Start(vm_uuid string, op openapi.Operation, msg string) (int64, error) {
	var (
		err error
		msg_off int64
	)
	msg_off, err = oplog_append_msg(vm_uuid, op, msg)
	if (err != nil) {
		return 0, err
	}
	rec := record{
		Status: openapi.OPERATION_STARTED,
		Ts: ts.Now(),
		Te: 0,
		Msg_start_off: msg_off,
		Msg_end_roff: 0,
		Reserved: 0,
	}
	return oplog_append(&rec, vm_uuid, op)
}

/*
 * StartEnd appends a single already-finished record for operations that cannot
 * write a STARTED record first (ie VmCreate): no Vmdir exists at that time.
 * ts_start should be captured by the caller at the real start time, so that the
 * information is preserved and passed here.
 */
func StartEnd(vm_uuid string, op openapi.Operation, state openapi.OperationState, msgs string, msge string, ts_start int64) error {
	var (
		err error
		msgs_off, msge_off int64
	)
	msgs_off, err = oplog_append_msg(vm_uuid, op, msgs)
	if (err != nil) {
		return err
	}
	rec := record{
		Status: state,
		Ts: ts_start,
		Te: ts.Now(),
		Msg_start_off: msgs_off,
		Msg_end_roff: 0,
		Reserved: 0,
	}
	if (msge != "") {
		msge_off, err = oplog_append_msg(vm_uuid, op, msge)
		if (err != nil) {
			return err
		}
		rec.Msg_end_roff = int32(msge_off - msgs_off)
	}
	_, err = oplog_append(&rec, vm_uuid, op)
	return err
}

/*
 * End updates the STARTED record at the given offset with the final outcome.
 * state should be OPERATION_COMPLETED or OPERATION_FAILED.
 * msg is stored after the message the STARTED record already has.
 */
func End(vm_uuid string, op openapi.Operation, state openapi.OperationState, msg string, offset int64) error {
	return oplog_update(vm_uuid, op, state, msg, offset, ts.Now())
}

/*
 * Complete is used by the libvirt lifecycle event handler, which knows only
 * the operation type and a completion message, not the record offset. It finds
 * the last STARTED record for this VM on the local host and updates it.
 * msg is stored after the message the STARTED record already has.
 */
func Complete(vm_uuid string, op openapi.Operation, msg string) error {
	var (
		err error
		offset int64
	)
	offset, err = oplog_find_last_status(vm_uuid, op, openapi.OPERATION_STARTED)
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
func Load_last(vm_uuid string, op openapi.Operation, state *openapi.OperationState, msgs *string, msge *string, ts_start *int64, ts_end *int64) error {
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
	*state = recs[0].Status
	*ts_start = recs[0].Ts
	*ts_end = recs[0].Te
	*msgs, *msge, err = oplog_msg(vm_uuid, op, &recs[0])
	return err
}
