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

package reglog

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/reg"
	. "suse.com/virtx/pkg/constants"
)

/*
 * reglog holds the record-agnostic code shared by pkg/oplog and pkg/eventlog:
 * both keep for each VM a file with fixed-size records, plus a msg file
 * containing all the variable-length strings.
 */

/* MSG_MAX is the maximum message length in bytes (room for a trailing newline). */
const MSG_MAX = (4 * KiB) - 1

/* Dir returns the VM's reg dir, where both oplog and eventlog keep their files. */
func Dir(vm_uuid string) string {
	return reg.Vmdir(machine.Uuid(), vm_uuid)
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
 * Append_msg appends msg (sanitized, truncated to MSG_MAX) to msg_file,
 * creating it if needed, and returns the offset it was written at.
 */
func Append_msg(msg_file string, msg string) (int64, error) {
	var (
		err error
		f *os.File
		buf []byte
		offset int64
	)
	msg = sanitize_msg(msg)
	if (len(msg) > MSG_MAX) {
		msg = msg[:MSG_MAX]
	}
	buf = []byte(msg + "\n")
	f, err = os.OpenFile(msg_file, os.O_WRONLY | os.O_CREATE | os.O_APPEND, 0640)
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
	if (offset == 0) {
		err = reg.Syncdir(filepath.Dir(msg_file))
		if (err != nil) {
			return 0, err
		}
	}
	return offset, nil
}

/* Read_msg returns the message stored at offset in the open msg file f. */
func Read_msg(f *os.File, offset int64) (string, error) {
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
 * Open_log opens path (record file) and returns it along with its
 * record count. A missing file returns a nil File;
 * the caller treats this as an empty log, not an error.
 */
func Open_log(path string, record_size int64) (*os.File, int64, error) {
	var (
		err error
		f *os.File
		fi os.FileInfo
	)
	f, err = os.Open(path)
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
	return f, fi.Size() / record_size, nil
}

/*
 * Append_record writes buf, a sbinary-encoded record, to path at the next
 * record-aligned offset, creating the file if needed, and returns offset.
 *
 * A crash or a partial write can leave a partial trailing record, so the
 * file is not guaranteed of a record-aligned size; this will self-heal
 * as each append truncates the previous stray bytes if any.
 *
 * The directory is synced only when this is the very first record (offset 0).
 *
 * The caller is responsible for locking: concurrent callers racing on the
 * same path would otherwise compute the same offset from the same fi.Size().
 */
func Append_record(path string, buf []byte, record_size int64) (int64, error) {
	var (
		err error
		f *os.File
		fi os.FileInfo
		offset int64
	)
	f, err = os.OpenFile(path, os.O_WRONLY | os.O_CREATE, 0640)
	if (err != nil) {
		return 0, err
	}
	defer f.Close()
	fi, err = f.Stat()
	if (err != nil) {
		return 0, err
	}
	offset = (fi.Size() / record_size) * record_size
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
	if (offset == 0) {
		err = reg.Syncdir(filepath.Dir(path))
		if (err != nil) {
			return 0, err
		}
	}
	return offset, nil
}

/*
 * ts_at reads the int64 Ts field stored at the very start (byte offset 0) of
 * the record at index in a record_size-record file f. Every reglog-based
 * record type must place its Ts field first, so binary search never needs
 * to decode the rest of the record just to compare timestamps.
 */
func ts_at(f *os.File, record_size int64, index int64) (int64, error) {
	var buf [8]byte
	_, err := f.ReadAt(buf[:], index * record_size)
	if (err != nil) {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(buf[:])), nil
}

/* bsearch_lo returns the first index in [0, nrec) whose record has Ts >= from, or nrec if there is none. */
func bsearch_lo(f *os.File, record_size int64, nrec int64, from int64) (int64, error) {
	var (
		err error
		t int64
		lo, hi, mid int64
	)
	lo, hi = 0, nrec
	for (lo < hi) {
		mid = (lo + hi) / 2
		t, err = ts_at(f, record_size, mid)
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
func bsearch_hi(f *os.File, record_size int64, nrec int64, to int64) (int64, error) {
	var (
		err error
		t int64
		lo, hi, mid int64
	)
	lo, hi = 0, nrec
	for (lo < hi) {
		mid = (lo + hi) / 2
		t, err = ts_at(f, record_size, mid)
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

/* Range resolves [lo, hi), the index range of records whose Ts falls in [from, to], where 0 means unbounded. */
func Range(f *os.File, record_size int64, nrec int64, from int64, to int64) (int64, int64, error) {
	var (
		err error
		lo, hi int64
	)
	lo = 0
	if (from != 0) {
		lo, err = bsearch_lo(f, record_size, nrec, from)
		if (err != nil) {
			return 0, 0, err
		}
	}
	hi = nrec
	if (to != 0) {
		hi, err = bsearch_hi(f, record_size, nrec, to)
		if (err != nil) {
			return 0, 0, err
		}
	}
	return lo, hi, nil
}

/*
 * LockMap lazily creates one RWMutex per uuid key. Each caller keeps its own LockMap.
 */
type LockMap struct {
	m sync.Map /* uuid string -> *sync.RWMutex */
}

func (s *LockMap) Get(key string) *sync.RWMutex {
	v, _ := s.m.LoadOrStore(key, &sync.RWMutex{})
	return v.(*sync.RWMutex)
}

func (s *LockMap) Delete(key string) {
	s.m.Delete(key)
}
