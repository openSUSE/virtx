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
 * along with this program; if not, see <https://www.gnu.org/licenses/>
 */

/*
 * A switch request asks the coordinator office to move to one named host.
 *
 * It lives in the shared registration tree for the same reason every other
 * request does: the operator talks to one virtxd, the office is held by another,
 * and the request must be servable whichever host ends up holding it. The
 * holder gives the office back when it reads the request; the target competes
 * for it on its next patrol, and the other hosts stay out of the contest until
 * the target has taken it or the request has run out.
 *
 * The request never changes who may hold the office: it says who should try
 * first. The lease is still the only authority, and a target that does not come
 * leaves the others with the office after the deadline, not for ever.
 */
package coord

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/ts"
)

/* SWITCH_PATH is the request that moves the coordinator office to one host. */
const SWITCH_PATH = "/vms/reg/coord_switch"

/*
 * SWITCH_TTL_MS is how long a switch request stays valid: enough for the holder
 * to give the office back and the target to take it on its next patrol, short
 * enough that a target that never comes cannot keep the other hosts out of the
 * contest for long.
 */
const SWITCH_TTL_MS = int64(30) * 1000

const (
	SWITCH_KEY_TARGET = "target"
	SWITCH_KEY_BY = "requested_by"
	SWITCH_KEY_AT = "requested_at"
	SWITCH_KEY_DEADLINE = "deadline_ms"
)

/* Switch_request is a move of the coordinator office that a host has asked for. */
type Switch_request struct {
	Target string
	Requested_by string
	Requested_at int64
	Deadline_ms int64
}

/*
 * Save_switch records that the office has been asked to move to target, in the
 * shared registration tree, so that the holder serves it whichever host the
 * operator talked to. The file is replaced atomically, like every other file of
 * the tree.
 */
func Save_switch(target string, by string) error {
	var (
		sb strings.Builder
		now int64
	)
	now = ts.Now()
	sb.WriteString("# written by virtxd when the coordinator office is asked to move, do not edit\n")
	sb.WriteString(fmt.Sprintf("%s = %q\n", SWITCH_KEY_TARGET, target))
	sb.WriteString(fmt.Sprintf("%s = %q\n", SWITCH_KEY_BY, by))
	sb.WriteString(fmt.Sprintf("%s = %d\n", SWITCH_KEY_AT, now))
	sb.WriteString(fmt.Sprintf("%s = %d\n", SWITCH_KEY_DEADLINE, now + SWITCH_TTL_MS))
	return reg.Save_shared(SWITCH_PATH, []byte(sb.String()))
}

/* Load_switch reads the request; a cluster that has asked for nothing is normal. */
func Load_switch() (Switch_request, bool, error) {
	var (
		data []byte
		err error
	)
	data, err = os.ReadFile(SWITCH_PATH)
	if (err != nil) {
		if (errors.Is(err, os.ErrNotExist)) {
			return Switch_request{}, false, nil
		}
		return Switch_request{}, false, err
	}
	req, ok := parse_switch(string(data))
	return req, ok, nil
}

/*
 * parse_switch turns the file into a request. ok is false when the file carries
 * no target at all: the target is the one field without which a request means
 * nothing, so a half written file is not a request.
 */
func parse_switch(text string) (Switch_request, bool) {
	var (
		req Switch_request
		line, key, value string
		cut int
		ok bool
	)
	for _, line = range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if (line == "" || strings.HasPrefix(line, "#")) {
			continue
		}
		cut = strings.Index(line, "=")
		if (cut < 0) {
			continue
		}
		key = strings.TrimSpace(line[:cut])
		value = strings.TrimSpace(line[cut + 1:])
		switch (key) {
		case SWITCH_KEY_TARGET:
			req.Target, _ = strconv.Unquote(value)
			ok = (req.Target != "")
		case SWITCH_KEY_BY:
			req.Requested_by, _ = strconv.Unquote(value)
		case SWITCH_KEY_AT:
			req.Requested_at, _ = strconv.ParseInt(value, 10, 64)
		case SWITCH_KEY_DEADLINE:
			req.Deadline_ms, _ = strconv.ParseInt(value, 10, 64)
		}
	}
	if (ok && req.Deadline_ms == 0) {
		req.Deadline_ms = req.Requested_at + SWITCH_TTL_MS
	}
	return req, ok
}

/* Clear_switch forgets a request that has been served or has run out. */
func Clear_switch() error {
	var (
		err error
	)
	err = os.Remove(SWITCH_PATH)
	if (err != nil && errors.Is(err, os.ErrNotExist)) {
		return nil
	}
	return err
}

/* switch_fresh answers whether a request may still move the office, at now_ms. */
func switch_fresh(req Switch_request, now_ms int64) bool {
	return (req.Deadline_ms > 0 && now_ms < req.Deadline_ms)
}

/* Switch_target answers the host a live request names; ok is false without one. */
func Switch_target(now_ms int64) (string, bool) {
	var (
		req Switch_request
		ok bool
		err error
	)
	req, ok, err = Load_switch()
	if (err != nil || !ok) {
		return "", false
	}
	if (!switch_fresh(req, now_ms)) {
		return "", false
	}
	return req.Target, true
}

/* Switch_targets answers whether a live request names this host. */
func Switch_targets(host string) bool {
	var (
		target string
		ok bool
	)
	target, ok = Switch_target(ts.Now())
	return (ok && target == host)
}

/* switch_elsewhere is the live request when it names a host other than this one. */
func switch_elsewhere(host string) (Switch_request, bool) {
	var (
		req Switch_request
		ok bool
		err error
	)
	req, ok, err = Load_switch()
	if (err != nil || !ok) {
		return Switch_request{}, false
	}
	if (!switch_fresh(req, ts.Now())) {
		return Switch_request{}, false
	}
	if (req.Target == host) {
		return Switch_request{}, false
	}
	return req, true
}
