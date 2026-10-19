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
 * Which policy a host runs with, round after round.
 *
 * The shared file is the authority. When it can be read, it is applied and kept
 * as this host's copy; when its generation is older than the one already applied,
 * it is refused, because that is what a host coming back with an old view of the
 * cluster looks like.
 *
 * When it cannot be read, the interesting question is why:
 *
 *   - the file is not there, but the directory it belongs to can be listed: the
 *     file was removed (or the shared tree was replaced), so the cluster has no
 *     policy any more and the host must stop acting. The one thing that must not
 *     happen here is acting on a stale copy because of a hiccup of the shared
 *     file system, so a miss is only believed after ENOENT_MAX_ROUNDS rounds in
 *     a row;
 *
 *   - anything else (the mount is gone, the file system is read-only, the
 *     directory cannot be listed): the shared tree is out of reach, and the host
 *     keeps running with the policy it applied, which is why that copy exists.
 *
 * A host that has neither is left with the documented defaults, whose switch is
 * off: no policy means no action.
 */
package policy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"suse.com/virtx/pkg/ts"
)

/*
 * ENOENT_MAX_ROUNDS is how many rounds in a row the file has to be missing
 * before the host believes that it is gone. A shared file system can report a
 * name as missing while its directory is perfectly listable, and acting on that
 * would turn a hiccup into a stopped cluster; at the pace the decision layer
 * looks at the policy this is a handful of seconds either way.
 */
const ENOENT_MAX_ROUNDS = 3

/* Origin says where the policy of one round came from, as the log prints it. */
type Origin string

const (
	/* ORIGIN_SHARED is the file on the shared registration tree. */
	ORIGIN_SHARED Origin = "shared"

	/* ORIGIN_APPLIED is the copy this host stored when it last applied one. */
	ORIGIN_APPLIED Origin = "applied"

	/* ORIGIN_DEFAULTS is the documented defaults, with the switch off. */
	ORIGIN_DEFAULTS Origin = "defaults"
)

/*
 * Reader resolves the policy of every round, remembering the generation this
 * host has applied and how many rounds in a row the shared file was missing.
 * One Reader belongs to one process; it is not safe for concurrent use, and its
 * caller is the decision layer's own loop.
 */
type Reader struct {
	applied Applied
	misses int
	origin Origin
	warnings []string
	/*
	 * said is what the round before this one reported, so that a state that
	 * simply stays the way it is - the policy file still gone, say - is said
	 * once instead of once per round.
	 */
	said map[string]bool
}

/*
 * R is the reader of this process. The decision layer resolves the policy once
 * per round through it, so that the generation this host has applied and the
 * misses counted so far are one story and not one per caller.
 */
var R = &Reader{}

/*
 * Resolve returns the policy of this round and where it came from. It always
 * returns a usable policy: the caller never has to decide what to do when the
 * shared file is out of reach.
 */
func (r *Reader) Resolve() (*Policy, Origin) {
	var (
		p *Policy
		origin Origin
	)
	r.warnings = nil
	p, origin = r.resolve()
	r.keep_fresh_warnings()
	r.origin = origin
	return p, origin
}

/*
 * keep_fresh_warnings drops what the round before already said and remembers
 * what this one says. The comparison is against everything the previous round
 * said and not against the last single message: a round that reports two states
 * makes each of them follow the other, so both would come back in every round -
 * measured on the cluster, the "the policy is gone" line was still written every
 * ten seconds.
 */
func (r *Reader) keep_fresh_warnings() {
	var (
		fresh []string
		all map[string]bool = make(map[string]bool)
	)
	for _, w := range r.warnings {
		if (!r.said[w]) {
			fresh = append(fresh, w)
		}
		all[w] = true
	}
	r.said = all
	r.warnings = fresh
}

/* Origin is where the policy of the last Resolve came from. */
func (r *Reader) Origin() Origin {
	return r.origin
}

/* resolve is Resolve without the bookkeeping of one round. */
func (r *Reader) resolve() (*Policy, Origin) {
	var (
		data []byte
		err error
		p *Policy
		meta Applied
	)
	data, err = os.ReadFile(PATH)
	if (err == nil) {
		p = Parse(string(data))
		if (!May_apply(p, r.applied)) {
			r.warn("%s carries generation %d, which is older than the applied %d: keeping this host's copy",
				PATH, p.Generation, r.applied.Generation)
			return r.use_applied()
		}
		meta, err = Save_applied(string(data), PATH, p.Generation)
		if (err != nil) {
			/*
			 * The copy is what this host falls back to when the shared tree is
			 * unreachable: not being able to store it is worth a warning, but it
			 * does not change which policy this host has applied. The generation
			 * is remembered in memory either way, because that is what the other
			 * hosts read as this host's acknowledgement of the change (measured
			 * 2026-10-12: a daemon that may not write the copy acknowledged
			 * nothing at all, and the switch never reached anybody).
			 */
			r.warn("could not store the policy for the next outage: %s", err_text(err))
			r.applied = Applied{
				Generation: p.Generation,
				Sha256: Sha256_of(data),
				Source: PATH,
				Applied_at: ts.Now(),
			}
		} else {
			r.applied = meta
		}
		r.misses = 0
		return p, ORIGIN_SHARED
	}
	if (errors.Is(err, os.ErrNotExist) && dir_is_listable()) {
		r.misses++
		if (r.misses >= ENOENT_MAX_ROUNDS) {
			r.warn("%s is gone, and so is the policy of this cluster: the action surface is off", PATH)
			return Defaults(), ORIGIN_DEFAULTS
		}
		r.warn("%s is missing (%d of %d rounds), keeping the policy this host has",
			PATH, r.misses, ENOENT_MAX_ROUNDS)
		return r.use_applied()
	}
	r.warn("%s cannot be read (%s): keeping the policy this host has", PATH, err.Error())
	return r.use_applied()
}

/* Warnings returns what went wrong in the last resolution, and only that. */
func (r *Reader) Warnings() []string {
	return r.warnings
}

/* Applied returns the meta of the copy this host has applied. */
func (r *Reader) Applied() Applied {
	return r.applied
}

/*
 * use_applied is the policy this host stored when it last applied one, and where
 * that policy comes from: a copy that cannot be read leaves the documented
 * defaults, and the caller has to report that instead of claiming a copy.
 * Without a usable copy the switch is off; the
 * generation that was applied before is remembered either way, so that an old
 * view cannot walk over it later.
 */
func (r *Reader) use_applied() (*Policy, Origin) {
	var (
		p *Policy
		meta Applied
		err error
	)
	p, meta, err = Load_applied()
	if (err != nil) {
		r.warn("no usable copy of the policy on this host (%s): the documented defaults are in force",
			err.Error())
		return Defaults(), ORIGIN_DEFAULTS
	}
	r.applied = meta
	return p, ORIGIN_APPLIED
}

/* warn records one thing that went wrong while resolving this round. */
func (r *Reader) warn(format string, args ...any) {
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

/*
 * dir_is_listable reports whether the directory the policy belongs to can be
 * listed. A name that is missing while its directory is fine is what a removed
 * file looks like; a directory that cannot be listed is what an unreachable
 * shared tree looks like, and those two must not be confused.
 */
func dir_is_listable() bool {
	var (
		err error
	)
	_, err = os.ReadDir(filepath.Dir(PATH))
	return (err == nil)
}
