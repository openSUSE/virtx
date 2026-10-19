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

/*
 * The local copy of the cluster policy, and the generation rule.
 *
 * A host has to judge while the shared registration tree may be unreachable, so
 * the policy it last applied is kept on its own disk: a judgement made during a
 * storage outage then uses the settings the host was last told to use instead of
 * the documented defaults.
 *
 * The copy carries the hash of its content and the generation it came from in a
 * small meta file next to it. Both files are replaced atomically, and the meta
 * is written after the copy, so a crash in between leaves a meta that does not
 * match the copy - which reads as "this copy cannot be trusted", the fail-closed
 * direction.
 *
 * The generation keeps a host that comes back with an old view from pushing the
 * cluster back to it: a policy read from the shared file may only replace the
 * local copy when its generation is not older than the one the copy came from.
 * A host that never applied one starts at generation zero, so the first policy
 * it reads is always accepted.
 *
 * The copy lives in /var/lib/virtx, this host's own state directory: the daemon
 * runs as qemu and has no business writing into /etc/virtx, which belongs to the
 * operator (measured 2026-10-12: the copy could not be stored there at all, so a
 * host that had to judge during a storage outage had nothing to fall back to).
 * The directory is made by the deploy; this code makes it when it may, and a
 * host that cannot store the copy still applies the policy and says so in its
 * report - the copy is the fallback, not the acknowledgement.
 */
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"suse.com/virtx/pkg/ts"
)

const (
	/* APPLIED_DIR is this host's own state directory. */
	APPLIED_DIR = "/var/lib/virtx"

	/* APPLIED_PATH is the policy copy this host has applied. */
	APPLIED_PATH = APPLIED_DIR + "/cluster_policy.applied"

	/* APPLIED_META_PATH describes that copy: its hash, generation and source. */
	APPLIED_META_PATH = APPLIED_PATH + ".meta"

	META_KEY_GENERATION = "generation"
	META_KEY_SHA256 = "sha256"
	META_KEY_SOURCE = "source"
	META_KEY_APPLIED_AT = "applied_at"
)

/* Applied is what the meta file of the local policy copy records. */
type Applied struct {
	Generation int64
	Sha256 string
	Source string
	Applied_at int64
}

/*
 * Load_applied reads the local copy of the cluster policy and its meta file. A
 * copy that is missing is reported as os.ErrNotExist, which the caller reads as
 * "this host never applied a policy"; a copy that does not match its meta is an
 * error and must not be used either.
 */
func Load_applied() (*Policy, Applied, error) {
	var (
		meta Applied
		data []byte
		err error
		p *Policy
	)
	meta, err = load_applied_meta()
	if (err != nil) {
		return nil, Applied{}, err
	}
	data, err = os.ReadFile(APPLIED_PATH)
	if (err != nil) {
		return nil, Applied{}, err
	}
	if (Sha256_of(data) != meta.Sha256) {
		return nil, Applied{}, fmt.Errorf("%s does not match the hash recorded in %s",
			APPLIED_PATH, APPLIED_META_PATH)
	}
	p = Parse(string(data))
	return p, meta, nil
}

/*
 * Save_applied stores one policy text as this host's copy, together with the
 * meta that records its hash, the generation it came from and where it was read
 * from. The caller passes the text it read from the shared file, so that the
 * copy is what was parsed and not a re-rendering of it. The meta that was
 * written is returned, so that a caller can remember what this host applied.
 */
func Save_applied(text string, source string, generation int64) (Applied, error) {
	var (
		err error
		meta Applied
		sha string
	)
	sha = Sha256_of([]byte(text))
	err = write_atomic(APPLIED_PATH, []byte(text))
	if (err != nil) {
		return Applied{}, err
	}
	meta = Applied{
		Generation: generation,
		Sha256: sha,
		Source: source,
		Applied_at: ts.Now(),
	}
	err = write_atomic(APPLIED_META_PATH, []byte(applied_meta_text(meta)))
	if (err != nil) {
		return Applied{}, err
	}
	return meta, nil
}

/*
 * May_apply reports whether a policy read from the shared file may replace the
 * one this host has applied. A host that never applied one passes the zero
 * value, whose generation is zero.
 */
func May_apply(p *Policy, applied Applied) bool {
	return (p.Generation >= applied.Generation)
}

/* Sha256_of is the hash the meta file records and Load_applied verifies. */
func Sha256_of(data []byte) string {
	var (
		sum [32]byte
	)
	sum = sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

/* load_applied_meta reads and checks the meta file of the local copy. */
func load_applied_meta() (Applied, error) {
	var (
		meta Applied
		data []byte
		err error
	)
	data, err = os.ReadFile(APPLIED_META_PATH)
	if (err != nil) {
		return Applied{}, err
	}
	meta = parse_applied_meta(string(data))
	if (meta.Sha256 == "") {
		return Applied{}, fmt.Errorf("%s does not record a hash", APPLIED_META_PATH)
	}
	return meta, nil
}

/* parse_applied_meta reads the meta file; it is as tolerant as the policy is. */
func parse_applied_meta(text string) Applied {
	var (
		meta Applied
		raw string
		key, value string
		ok bool
		n int64
		err error
	)
	for _, raw = range strings.Split(text, "\n") {
		raw = strings.TrimSpace(strip_comment(raw))
		if (raw == "") {
			continue
		}
		key, value, ok = split_line(raw)
		if (!ok) {
			continue
		}
		key = strings.TrimSpace(key)
		value = unquote(strings.TrimSpace(value))
		switch {
		case key == META_KEY_GENERATION:
			n, err = strconv.ParseInt(value, 10, 64)
			if (err == nil) {
				meta.Generation = n
			}
		case key == META_KEY_SHA256:
			meta.Sha256 = value
		case key == META_KEY_SOURCE:
			meta.Source = value
		case key == META_KEY_APPLIED_AT:
			n, err = strconv.ParseInt(value, 10, 64)
			if (err == nil) {
				meta.Applied_at = n
			}
		}
	}
	return meta
}

/*
 * err_text is the cause of a file error, without the name of the temporary file
 * it may mention: a message that changes every time is a message that is never
 * recognized as the same one, and so one that is logged in every round.
 */
func err_text(err error) string {
	var (
		perr *os.PathError
	)
	if (errors.As(err, &perr)) {
		return perr.Err.Error()
	}
	return err.Error()
}

/* applied_meta_text renders the meta file of a stored copy. */
func applied_meta_text(meta Applied) string {
	var (
		sb strings.Builder
	)
	sb.WriteString("# written by virtxd when it applied a policy, do not edit\n")
	sb.WriteString(fmt.Sprintf("%s = %d\n", META_KEY_GENERATION, meta.Generation))
	sb.WriteString(fmt.Sprintf("%s = %s\n", META_KEY_SHA256, meta.Sha256))
	sb.WriteString(fmt.Sprintf("%s = %q\n", META_KEY_SOURCE, meta.Source))
	sb.WriteString(fmt.Sprintf("%s = %d\n", META_KEY_APPLIED_AT, meta.Applied_at))
	return sb.String()
}

/*
 * write_atomic replaces a file in one step: a reader sees either the old file or
 * the new one, never a partial one. The temporary file is created in the target
 * directory so that the rename stays inside one file system.
 */
func write_atomic(path string, data []byte) error {
	var (
		tmp *os.File
		tmpname string
		err error
	)
	/*
	 * The deploy makes the directory of the copy; making it here as well costs
	 * nothing on a host where it may, and on one where it may not the error below
	 * names one stable path instead of a temporary name that changes every round.
	 */
	err = os.MkdirAll(filepath.Dir(path), 0755)
	if (err != nil) {
		return fmt.Errorf("cannot create %s (%s)", filepath.Dir(path), err.Error())
	}
	tmp, err = os.CreateTemp(filepath.Dir(path), filepath.Base(path) + ".tmp-*")
	if (err != nil) {
		return fmt.Errorf("cannot create a temporary file in %s (%s)", filepath.Dir(path), err_text(err))
	}
	tmpname = tmp.Name()
	defer func() {
		if (err != nil) {
			tmp.Close()
			os.Remove(tmpname)
		}
	}()
	_, err = tmp.Write(data)
	if (err != nil) {
		return err
	}
	err = tmp.Sync()
	if (err != nil) {
		return err
	}
	err = tmp.Chmod(0644)
	if (err != nil) {
		return err
	}
	err = tmp.Close()
	if (err != nil) {
		return err
	}
	return os.Rename(tmpname, path)
}
