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
 * Package policy is the cluster wide policy file, /vms/reg/cluster_policy.
 *
 * The file lives in the shared registration tree because that is the only place
 * a host can read after it has lost the network, and it carries the settings
 * that every host has to agree on before a judgement may be turned into an
 * action: whether the action surface is enabled at all, which addresses tell
 * "my own network is broken" apart from "the peer is unreachable", and how long
 * a peer's self report may stand still before it stops counting as evidence.
 *
 * The syntax is a flat "setting = value" per line: a # starts a comment, blank
 * lines are ignored, values may be quoted. Keys carry the area they belong to
 * (ha_ for the high availability settings) so that one file can hold more than
 * one area without a section syntax.
 *
 * generation is written by whoever changes the file and only ever moves
 * forward; a reader remembers the highest value it has applied and refuses a
 * lower one, which is what keeps a host that comes back with an old view from
 * pushing the cluster back to it.
 *
 * Parsing is tolerant: a missing file, a missing setting, an unknown setting
 * and a malformed line all leave the behaviour unchanged and only add a
 * warning. The exception is the switch: ha_enabled can only become true through
 * a well formed line, and a file that cannot be read forces it back to off
 * (fail-closed).
 */
package policy

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	. "suse.com/virtx/pkg/constants"
)

const (
	/* PATH is the cluster wide policy file. */
	PATH = REG_DIR + CLUSTER_POLICY

	/* ISOLATION_ADDRESS_MAX is how many isolation addresses may be configured. */
	ISOLATION_ADDRESS_MAX = 10

	KEY_GENERATION = "generation"
	KEY_CHANGED_BY = "changed_by"
	KEY_CHANGED_AT = "changed_at"
	KEY_HA_ENABLED = "ha_enabled"
	KEY_HA_HASTATE_REFRESH_SECONDS = "ha_hastate_refresh_seconds"
	KEY_HA_HASTATE_STALE_SECONDS = "ha_hastate_stale_seconds"
	KEY_HA_MAX_INFLIGHT = "ha_max_inflight"
	KEY_HA_ISOLATION_RESPONSE = "ha_isolation_response"
	KEY_HA_ISOLATION_SECONDS = "ha_isolation_seconds"
	KEY_HA_ISOLATION_START_INTERVAL_SECONDS = "ha_isolation_start_interval_seconds"
	KEY_HA_ISOLATION_ADDRESS = "ha_isolation_address_"

	DEFAULT_GENERATION = 0
	DEFAULT_HA_ENABLED = false
	DEFAULT_HASTATE_REFRESH_SECONDS = 5
	DEFAULT_HASTATE_STALE_SECONDS = 30
	DEFAULT_MAX_INFLIGHT = 1

	/* the words ha_isolation_response may hold, and the one it defaults to */
	RESPONSE_LEAVE_ON = "leave-on"
	RESPONSE_SHUT_DOWN = "shut-down"
	RESPONSE_POWER_OFF = "power-off"
	DEFAULT_ISOLATION_RESPONSE = RESPONSE_POWER_OFF

	/*
	 * The isolation response of a host starts this many seconds after it has
	 * found itself cut off: the delay is what lets that host stop its VMs, and
	 * their disk leases go, before the cluster starts them anywhere else.
	 */
	DEFAULT_ISOLATION_SECONDS = 14

	/*
	 * How far apart the VMs rescued from an isolated host are started: they are
	 * started one at a time, and this is the pause between two of them.
	 */
	DEFAULT_ISOLATION_START_INTERVAL_SECONDS = 5
	ISOLATION_TAKEOVER_GAP_SECONDS = 1
)

/*
 * Isolation_start_interval answers the pace at which the VMs rescued from an
 * isolated host are started: the configured value, or the documented default
 * when it is not set. One answer for the coordinator that paces the starts and
 * for the log line that describes them.
 */
func (p *Policy) Isolation_start_interval() int {
	if (p != nil && p.Isolation_start_interval_seconds > 0) {
		return p.Isolation_start_interval_seconds
	}
	return DEFAULT_ISOLATION_START_INTERVAL_SECONDS
}

/* Source is the layer a value came from, as the log prints it. */
type Source string

const (
	FROM_DEFAULT Source = "default"
	FROM_FILE Source = "file"
)

/* item is one resolved setting, for the log. */
type item struct {
	value string
	source Source
}

/*
 * Policy is every setting of the cluster policy file, already resolved, plus
 * the bookkeeping behind the log. It is never nil: a caller that asks before
 * Load() gets the documented defaults, and ha_enabled is off in them.
 */
type Policy struct {
	Generation int64
	Changed_by string
	Changed_at string
	Ha_enabled bool
	Hastate_refresh_seconds int
	Hastate_stale_seconds int
	Max_inflight int
	Isolation_response string
	Isolation_seconds int
	Isolation_start_interval_seconds int
	Isolation_addrs []string

	items map[string]item
	warnings []string

	/*
	 * isolation keeps the addresses by their index while the file is read:
	 * the index orders them, but the gaps a sparse file leaves must not end
	 * up as empty entries in the list the consumers read.
	 */
	isolation map[int]string
}

/*
 * Load returns the documented defaults with one file applied over them, and the
 * error of reading the file. A missing file is not an error: it means the
 * cluster has no policy yet, which reads as the defaults with the switch off.
 * Every parse problem is a warning, except for the switch, which a file that
 * cannot be read forces back to off.
 */
func Load(path string) (*Policy, error) {
	var (
		p *Policy
		err error
	)
	p = Defaults()
	err = p.load_file(path)
	if (err != nil) {
		p.fail_closed(path)
	}
	return p, err
}

/* Defaults returns the documented defaults, with every value's source set. */
const REQUEST_PATH = "/vms/reg/cluster_policy.request"

const (
	REQUEST_KEY_ENABLED = "enabled"
	REQUEST_KEY_BY = "requested_by"
	REQUEST_KEY_AT = "requested_at"
)

/* Request is a change of the action surface that a host has asked for. */
type Request struct {
	Enabled bool
	By string
	At int64
}

/*
 * Save_request records a change that has been asked for, in the shared
 * registration tree, so that whoever holds the coordinator office serves it - a
 * request must not depend on the host the operator happened to talk to.
 *
 * The file is small and replaced atomically. Two hosts asking at the same time
 * leave one of the two values behind, which is the honest outcome: they asked for
 * two different things.
 */
func Save_request(enabled bool, by string) error {
	var (
		sb strings.Builder
	)
	sb.WriteString("# written by virtxd when a change of the action surface is asked for, do not edit\n")
	sb.WriteString(fmt.Sprintf("%s = %v\n", REQUEST_KEY_ENABLED, enabled))
	sb.WriteString(fmt.Sprintf("%s = %q\n", REQUEST_KEY_BY, by))
	sb.WriteString(fmt.Sprintf("%s = %d\n", REQUEST_KEY_AT, time.Now().UnixMilli()))
	return write_atomic(REQUEST_PATH, []byte(sb.String()))
}

/*
 * Load_request reads the change that has been asked for. A cluster that has asked
 * for nothing is the normal state: a missing file is not an error, and it is told
 * apart from a file that cannot be read.
 */
func Load_request() (Request, bool, error) {
	var (
		req Request
		data []byte
		err error
		line string
		key string
		value string
		cut int
		present bool
	)
	data, err = os.ReadFile(REQUEST_PATH)
	if (err != nil) {
		if (errors.Is(err, os.ErrNotExist)) {
			return req, false, nil
		}
		return req, false, err
	}
	for _, line = range strings.Split(string(data), "\n") {
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
		case REQUEST_KEY_ENABLED:
			req.Enabled = (value == "true")
			present = true
		case REQUEST_KEY_BY:
			req.By, _ = strconv.Unquote(value)
		case REQUEST_KEY_AT:
			req.At, _ = strconv.ParseInt(value, 10, 64)
		}
	}
	if (!present) {
		return req, false, nil
	}
	return req, true, nil
}

/* Request_pending reports whether a change has been asked for and not served yet. */
func Request_pending() bool {
	var (
		ok bool
		err error
	)
	_, ok, err = Load_request()
	return (ok && err == nil)
}

/* Clear_request forgets the change that has been served, or given up on. */
func Clear_request() error {
	var (
		err error
	)
	err = os.Remove(REQUEST_PATH)
	if (err != nil && errors.Is(err, os.ErrNotExist)) {
		return nil
	}
	return err
}

func Defaults() *Policy {
	var (
		p *Policy
	)
	p = &Policy{
		Generation: DEFAULT_GENERATION,
		Ha_enabled: DEFAULT_HA_ENABLED,
		Hastate_refresh_seconds: DEFAULT_HASTATE_REFRESH_SECONDS,
		Hastate_stale_seconds: DEFAULT_HASTATE_STALE_SECONDS,
		Max_inflight: DEFAULT_MAX_INFLIGHT,
		Isolation_response: DEFAULT_ISOLATION_RESPONSE,
		Isolation_seconds: DEFAULT_ISOLATION_SECONDS,
		Isolation_start_interval_seconds: DEFAULT_ISOLATION_START_INTERVAL_SECONDS,
		items: make(map[string]item),
		isolation: make(map[int]string),
	}
	p.remember(KEY_GENERATION, strconv.FormatInt(DEFAULT_GENERATION, 10), FROM_DEFAULT)
	p.remember(KEY_CHANGED_BY, "", FROM_DEFAULT)
	p.remember(KEY_CHANGED_AT, "", FROM_DEFAULT)
	p.remember(KEY_HA_ENABLED, strconv.FormatBool(DEFAULT_HA_ENABLED), FROM_DEFAULT)
	p.remember(KEY_HA_HASTATE_REFRESH_SECONDS, strconv.Itoa(DEFAULT_HASTATE_REFRESH_SECONDS), FROM_DEFAULT)
	p.remember(KEY_HA_HASTATE_STALE_SECONDS, strconv.Itoa(DEFAULT_HASTATE_STALE_SECONDS), FROM_DEFAULT)
	p.remember(KEY_HA_MAX_INFLIGHT, strconv.Itoa(DEFAULT_MAX_INFLIGHT), FROM_DEFAULT)
	p.remember(KEY_HA_ISOLATION_RESPONSE, DEFAULT_ISOLATION_RESPONSE, FROM_DEFAULT)
	p.remember(KEY_HA_ISOLATION_SECONDS, strconv.Itoa(DEFAULT_ISOLATION_SECONDS), FROM_DEFAULT)
	p.remember(KEY_HA_ISOLATION_START_INTERVAL_SECONDS, strconv.Itoa(DEFAULT_ISOLATION_START_INTERVAL_SECONDS), FROM_DEFAULT)
	return p
}

/* Warnings returns everything that could not be used as written. */
func (p *Policy) Warnings() []string {
	return p.warnings
}

/*
 * Lines returns one line per setting with its final value and where that value
 * came from, so that the startup log alone answers "what policy is this process
 * running with".
 */
func (p *Policy) Lines() []string {
	var (
		lines []string
		keys []string
	)
	keys = []string{
		KEY_GENERATION,
		KEY_CHANGED_BY,
		KEY_CHANGED_AT,
		KEY_HA_ENABLED,
		KEY_HA_HASTATE_REFRESH_SECONDS,
		KEY_HA_HASTATE_STALE_SECONDS,
		KEY_HA_MAX_INFLIGHT,
		KEY_HA_ISOLATION_RESPONSE,
		KEY_HA_ISOLATION_SECONDS,
		KEY_HA_ISOLATION_START_INTERVAL_SECONDS,
	}
	for _, k := range keys {
		lines = append(lines, line_of(p, k))
	}
	for _, k := range p.isolation_keys() {
		lines = append(lines, line_of(p, k))
	}
	return lines
}

/* isolation_keys returns the isolation settings the file carried, in order. */
func (p *Policy) isolation_keys() []string {
	var (
		keys []string
		key string
		i int
		ok bool
	)
	for i = 0; i < ISOLATION_ADDRESS_MAX; i++ {
		key = isolation_key(i)
		_, ok = p.items[key]
		if (!ok) {
			continue
		}
		keys = append(keys, key)
	}
	return keys
}

/*
 * Fail_closed drops the switch back to off. It is called for a file that could
 * not be read at all: the rest of the settings keep their documented defaults,
 * because every one of them is only read by a path that must not act when the
 * switch is off.
 */
func (p *Policy) fail_closed(path string) {
	p.warn("%s: cannot be read, forcing %s to off", path, KEY_HA_ENABLED)
	p.Ha_enabled = DEFAULT_HA_ENABLED
	p.remember(KEY_HA_ENABLED, strconv.FormatBool(DEFAULT_HA_ENABLED), FROM_DEFAULT)
}

/*
 * Parse returns the documented defaults with one policy text applied over them.
 * It is what Load ends up doing, and what a caller uses for a text it has
 * already read itself (the local copy of the policy, for instance).
 */
func Parse(text string) *Policy {
	var (
		p *Policy
	)
	p = Defaults()
	p.load_text(text)
	return p
}

/* load_text applies one policy text over whatever the policy already holds. */
func (p *Policy) load_text(text string) {
	for _, raw := range strings.Split(text, "\n") {
		p.load_line(raw)
	}
	p.build_isolation_addrs()
}

/* load_file applies one file over the defaults. Only a read error is returned. */
func (p *Policy) load_file(path string) error {
	var (
		err error
		data []byte
	)
	data, err = os.ReadFile(path)
	if (err != nil) {
		return err
	}
	p.load_text(string(data))
	return nil
}

/* build_isolation_addrs turns the indexed addresses into the ordered list. */
func (p *Policy) build_isolation_addrs() {
	var (
		addrs []string
		i int
		s string
		ok bool
	)
	for i = 0; i < ISOLATION_ADDRESS_MAX; i++ {
		s, ok = p.isolation[i]
		if (!ok) {
			continue
		}
		addrs = append(addrs, s)
	}
	p.Isolation_addrs = addrs
}

/* load_line applies one line; a line that cannot be used only adds a warning. */
func (p *Policy) load_line(raw string) {
	var (
		key, value string
		ok bool
	)
	raw = strip_comment(raw)
	raw = strings.TrimSpace(raw)
	if (raw == "") {
		return
	}
	key, value, ok = split_line(raw)
	if (!ok) {
		p.warn("%q is not a setting", raw)
		return
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if (!known_key(key)) {
		p.warn("unknown setting %q", key)
		return
	}
	value = unquote(value)
	if (p.is_duplicate(key)) {
		p.warn("%s is set more than once, the last one wins", key)
	}
	p.set(key, value, FROM_FILE)
}

/* set applies one known setting, or warns and keeps what was there before. */
func (p *Policy) set(key string, value string, source Source) {
	var (
		err error
		n int
		n64 int64
		b bool
		i int
	)
	switch {
	case key == KEY_GENERATION:
		n64, err = non_negative_int64(value)
		if (err != nil) {
			p.warn("%s: %s", key, err.Error())
			return
		}
		p.Generation = n64
		p.remember(key, strconv.FormatInt(n64, 10), source)
	case key == KEY_CHANGED_BY:
		p.Changed_by = value
		p.remember(key, value, source)
	case key == KEY_CHANGED_AT:
		p.Changed_at = value
		p.remember(key, value, source)
	case key == KEY_HA_ENABLED:
		b, err = bool_value(value)
		if (err != nil) {
			p.warn("%s: %s", key, err.Error())
			return
		}
		p.Ha_enabled = b
		p.remember(key, strconv.FormatBool(b), source)
	case key == KEY_HA_HASTATE_REFRESH_SECONDS:
		n, err = positive_int(value)
		if (err != nil) {
			p.warn("%s: %s", key, err.Error())
			return
		}
		p.Hastate_refresh_seconds = n
		p.remember(key, strconv.Itoa(n), source)
	case key == KEY_HA_HASTATE_STALE_SECONDS:
		n, err = positive_int(value)
		if (err != nil) {
			p.warn("%s: %s", key, err.Error())
			return
		}
		p.Hastate_stale_seconds = n
		p.remember(key, strconv.Itoa(n), source)
	case key == KEY_HA_MAX_INFLIGHT:
		n, err = positive_int(value)
		if (err != nil) {
			p.warn("%s: %s", key, err.Error())
			return
		}
		p.Max_inflight = n
		p.remember(key, strconv.Itoa(n), source)
	case key == KEY_HA_ISOLATION_RESPONSE:
		p.Isolation_response, err = isolation_response_value(value)
		if (err != nil) {
			/*
			 * What the file says is not a response this code knows. The
			 * documented default powers VMs off, so falling back to it here
			 * would turn a typo into the most aggressive behaviour: read the
			 * unknown word as leave-on and say so.
			 */
			p.warn("%s: %s: reading %s", key, err.Error(), RESPONSE_LEAVE_ON)
			p.Isolation_response = RESPONSE_LEAVE_ON
		}
		p.remember(key, p.Isolation_response, source)
	case key == KEY_HA_ISOLATION_SECONDS:
		n, err = positive_int(value)
		if (err != nil) {
			p.warn("%s: %s", key, err.Error())
			return
		}
		p.Isolation_seconds = n
		p.remember(key, strconv.Itoa(n), source)
	case key == KEY_HA_ISOLATION_START_INTERVAL_SECONDS:
		n, err = positive_int(value)
		if (err != nil) {
			p.warn("%s: %s", key, err.Error())
			return
		}
		p.Isolation_start_interval_seconds = n
		p.remember(key, strconv.Itoa(n), source)
	case is_isolation_key(key):
		i, err = isolation_index(key)
		if (err != nil) {
			p.warn("%s: %s", key, err.Error())
			return
		}
		if (net.ParseIP(value) == nil) {
			p.warn("%s: %q is not an IP address", key, value)
			return
		}
		p.set_isolation_addr(i, value)
		p.remember(key, value, source)
	default:
		p.warn("unknown setting %q", key)
	}
}

/* set_isolation_addr stores one address at its index, keeping the order. */
func (p *Policy) set_isolation_addr(index int, value string) {
	p.isolation[index] = value
}

/* remember records one resolved setting for the log. */
func (p *Policy) remember(key string, value string, source Source) {
	p.items[key] = item{ value, source }
}

/* is_duplicate reports whether a key was already taken from the file. */
func (p *Policy) is_duplicate(key string) bool {
	var (
		it item
		ok bool
	)
	it, ok = p.items[key]
	if (!ok) {
		return false
	}
	return (it.source == FROM_FILE)
}

/* warn records one thing that could not be used as written. */
func (p *Policy) warn(format string, args ...any) {
	p.warnings = append(p.warnings, fmt.Sprintf(format, args...))
}

/* line_of renders one setting for the log. */
func line_of(p *Policy, key string) string {
	var (
		it item
		ok bool
	)
	it, ok = p.items[key]
	if (!ok) {
		return fmt.Sprintf("policy: %s is not set", key)
	}
	return fmt.Sprintf("policy: %s=%s source=%s", key, it.value, it.source)
}

/* isolation_key is the setting name of one isolation address index. */
/*
 * isolation_response_value reads the isolation response: what a host does with
 * its own running VMs when it has lost sight of every judge address while the
 * action surface is on. Only the three documented words mean anything, and a
 * word that is not one of them is refused so that the caller can fall back to
 * leave-on instead of the default that powers VMs off.
 */
func isolation_response_value(value string) (string, error) {
	switch (value) {
	case RESPONSE_LEAVE_ON, RESPONSE_SHUT_DOWN, RESPONSE_POWER_OFF:
		return value, nil
	}
	return "", fmt.Errorf("%q is not one of %s, %s, %s",
		value, RESPONSE_LEAVE_ON, RESPONSE_SHUT_DOWN, RESPONSE_POWER_OFF)
}

func isolation_key(index int) string {
	return KEY_HA_ISOLATION_ADDRESS + strconv.Itoa(index)
}

/* is_isolation_key reports whether a setting name names an isolation address. */
func is_isolation_key(key string) bool {
	return strings.HasPrefix(key, KEY_HA_ISOLATION_ADDRESS)
}

/*
 * isolation_index reads the index of an isolation address setting. The index is
 * a single decimal digit: the file is flat, so the index in the name is the
 * only thing that orders the addresses, and it is where the cap of
 * ISOLATION_ADDRESS_MAX applies.
 */
func isolation_index(key string) (int, error) {
	var (
		suffix string
		n int
		err error
	)
	suffix = strings.TrimPrefix(key, KEY_HA_ISOLATION_ADDRESS)
	n, err = strconv.Atoi(suffix)
	if (err != nil) {
		return 0, fmt.Errorf("%q is not an index", suffix)
	}
	if (n < 0 || n >= ISOLATION_ADDRESS_MAX) {
		return 0, fmt.Errorf("only %d isolation addresses are allowed", ISOLATION_ADDRESS_MAX)
	}
	if (len(suffix) != 1) {
		return 0, fmt.Errorf("%q is not a one digit index", suffix)
	}
	return n, nil
}

/* known_key reports whether the file documents a setting. */
func known_key(key string) bool {
	switch {
	case key == KEY_GENERATION:
		return true
	case key == KEY_CHANGED_BY:
		return true
	case key == KEY_CHANGED_AT:
		return true
	case key == KEY_HA_ENABLED:
		return true
	case key == KEY_HA_HASTATE_REFRESH_SECONDS:
		return true
	case key == KEY_HA_HASTATE_STALE_SECONDS:
		return true
	case key == KEY_HA_MAX_INFLIGHT:
		return true
	case is_isolation_key(key):
		return true
	}
	return false
}

/* strip_comment removes a trailing comment, honouring a quoted value. */
func strip_comment(line string) string {
	var (
		quoted bool
		i int
		c rune
	)
	for i, c = range line {
		if (c == '"') {
			quoted = !quoted
			continue
		}
		if (c == '#' && !quoted) {
			return line[:i]
		}
	}
	return line
}

/* split_line splits one "setting = value" line. */
func split_line(line string) (string, string, bool) {
	var (
		i int
	)
	i = strings.IndexByte(line, '=')
	if (i < 0) {
		return "", "", false
	}
	return line[:i], line[i + 1:], true
}

/* unquote removes one pair of double quotes around a value. */
func unquote(value string) string {
	if (len(value) >= 2 && strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"")) {
		return value[1:len(value) - 1]
	}
	return value
}

/* bool_value reads the several ways a switch may be written. */
func bool_value(value string) (bool, error) {
	switch (strings.ToLower(value)) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off", "":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a yes/no value", value)
}

/* positive_int reads a setting that has to be at least one. */
func positive_int(value string) (int, error) {
	var (
		n int
		err error
	)
	n, err = strconv.Atoi(value)
	if (err != nil) {
		return 0, fmt.Errorf("%q is not a number", value)
	}
	if (n < 1) {
		return 0, fmt.Errorf("%d is too small, the smallest value is 1", n)
	}
	return n, nil
}

/* non_negative_int64 reads a setting that may be zero. */
func non_negative_int64(value string) (int64, error) {
	var (
		n int64
		err error
	)
	n, err = strconv.ParseInt(value, 10, 64)
	if (err != nil) {
		return 0, fmt.Errorf("%q is not a number", value)
	}
	if (n < 0) {
		return 0, fmt.Errorf("%d is negative", n)
	}
	return n, nil
}
