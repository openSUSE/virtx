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
 * <https://www.gnu.org/licenses/>.
 */

/*
 * Package config is virtxd's own configuration file, /etc/virtx/virtxd.conf.
 *
 * The copy in the source tree, etc/virtx/virtxd.conf, is the authoritative
 * documentation of the defaults: every key it documents has exactly one default
 * here. A key the file does not mention keeps its documented default.
 *
 * Only the settings that belong to this host are configured here. The settings
 * the whole cluster has to agree on - whether the action surface is enabled at
 * all, which addresses tell a broken local network apart from an unreachable
 * peer, how long the report of a peer stands as evidence - are the cluster
 * policy, which is read from the shared registration tree (see pkg/policy).
 * That split is the point of the two files: a host that cannot reach the shared
 * tree must still start with the configuration it was given, and a setting that
 * decides about another host must not be settable on one host alone.
 *
 * Three layers, lowest first: the documented default, the file, the command
 * line. Every value carries the layer it came from, and Lines() (printed at
 * startup) shows the final value and its source for every key, so "what is this
 * process actually running with" is answerable from the startup log alone.
 *
 * Tolerating a broken file is a feature, not an accident: virtxd has to come up
 * on a host whose configuration is missing or wrong - the cluster needs the
 * daemon, and the daemon must never be turned into something else by a typo. So
 * a missing file, a missing key, an unknown key and a malformed line all leave
 * the behaviour unchanged and only add a warning. Nothing here can arm
 * anything: the switch of the action surface is not in this file.
 */
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

/* The one file, the keys it documents, and their documented defaults. */
const (
	PATH = "/etc/virtx/virtxd.conf"

	KEY_ISOLATION_PING_TIMEOUT_SECONDS = "isolation_ping_timeout_seconds"
	KEY_SANLOCK_CMD_TIMEOUT_SECONDS = "sanlock_cmd_timeout_seconds"
	KEY_HASTATE_ROUND_BUDGET_SECONDS = "hastate_round_budget_seconds"

	DEFAULT_PING_TIMEOUT_SECONDS = 1
	DEFAULT_SANLOCK_TIMEOUT_SECONDS = 3
	DEFAULT_ROUND_BUDGET_SECONDS = 3
)

/* Source is the layer a value came from, as the startup log prints it. */
type Source string

const (
	FROM_DEFAULT Source = "default"
	FROM_FILE Source = "file"
	FROM_FLAG Source = "flag"
)

/* key_order is the order Lines() prints, which is the order of the shipped
 * file: the log reads like the documentation. */
var key_order = []string{
	KEY_ISOLATION_PING_TIMEOUT_SECONDS,
	KEY_SANLOCK_CMD_TIMEOUT_SECONDS,
	KEY_HASTATE_ROUND_BUDGET_SECONDS,
}

/* item is one resolved key, for the log. */
type item struct {
	value string
	source Source
}

/*
 * Config is every tunable of virtxd, already resolved. The fields are the
 * values the packages read; items and warnings are the bookkeeping behind the
 * startup log.
 */
type Config struct {
	Isolation_ping_timeout_seconds int
	Sanlock_cmd_timeout_seconds int
	Hastate_round_budget_seconds int

	items map[string]item
	warnings []string
}

/*
 * C is what the packages read. It is never nil: before Init() - and in a unit
 * test that never calls it - C is the documented defaults, so a package used
 * without virtxd's main behaves exactly as it did when these values were
 * compile-time constants.
 */
var C = Defaults()

/* Init loads PATH into C and returns it. main calls it once, before the
 * packages start. */
func Init() *Config {
	C = Load(PATH)
	return C
}

/* Load returns the documented defaults with one file applied over them. It
 * never fails: every problem is a warning and a fallback. */
func Load(path string) *Config {
	var (
		c *Config = Defaults()
	)
	c.load_file(path)
	return c
}

/* Defaults returns the documented defaults, with every value's source set to
 * FROM_DEFAULT. */
func Defaults() *Config {
	var (
		c *Config
	)
	c = &Config{
		Isolation_ping_timeout_seconds: DEFAULT_PING_TIMEOUT_SECONDS,
		Sanlock_cmd_timeout_seconds: DEFAULT_SANLOCK_TIMEOUT_SECONDS,
		Hastate_round_budget_seconds: DEFAULT_ROUND_BUDGET_SECONDS,
		items: make(map[string]item),
	}
	c.remember(KEY_ISOLATION_PING_TIMEOUT_SECONDS, strconv.Itoa(DEFAULT_PING_TIMEOUT_SECONDS), FROM_DEFAULT)
	c.remember(KEY_SANLOCK_CMD_TIMEOUT_SECONDS, strconv.Itoa(DEFAULT_SANLOCK_TIMEOUT_SECONDS), FROM_DEFAULT)
	c.remember(KEY_HASTATE_ROUND_BUDGET_SECONDS, strconv.Itoa(DEFAULT_ROUND_BUDGET_SECONDS), FROM_DEFAULT)
	return c
}

func (c *Config) remember(key string, value string, source Source) {
	c.items[key] = item{ value: value, source: source }
}

/*
 * Override applies one command line value. main calls it only for the flags the
 * operator really gave (flag.Visit), never for a flag that merely fell back to
 * its default - a default that was not typed is not an override.
 */
func (c *Config) Override(key string, value string) {
	var (
		known bool
		err error
	)
	known, err = c.set(key, value, FROM_FLAG)
	if (!known) {
		c.warn("command line: unknown key %q ignored", key)
		return
	}
	if (err != nil) {
		c.warn("command line: %s; the key keeps its previous value (%s)",
			err.Error(), c.items[key].value)
	}
}

/*
 * Flag_key maps a command line flag name to the key it carries, so that main
 * can apply exactly the flags that were given. It is false for the flags that
 * are not configuration (for example -D).
 */
func Flag_key(name string) (string, bool) {
	var (
		key string = strings.ReplaceAll(name, "-", "_")
	)
	if (!known_key(key)) {
		return "", false
	}
	return key, true
}

/* Warnings is everything the file (or a flag) could not be taken as; main logs
 * them, and they are part of Lines() too. */
func (c *Config) Warnings() []string {
	return append([]string(nil), c.warnings...)
}

/*
 * Lines is exactly what the startup log prints, one line per key plus the
 * warnings. It is a function of its own so that the format can be pinned:
 * "which value, from where" must not drift.
 */
func (c *Config) Lines() []string {
	var (
		lines []string
	)
	for _, key := range key_order {
		var (
			it item = c.items[key]
		)
		lines = append(lines, fmt.Sprintf("config: %s=%s source=%s", key, it.value, it.source))
	}
	for _, w := range c.warnings {
		lines = append(lines, "config: warning: "+w)
	}
	return lines
}

/*
 * load_file reads one file over the current values. Everything it cannot use is
 * a warning; nothing here returns an error, because no configuration problem
 * may keep virtxd from starting.
 */
func (c *Config) load_file(path string) {
	var (
		data []byte
		err error
	)
	data, err = os.ReadFile(path)
	if (err != nil) {
		c.warn("%s: cannot read (%s): every key keeps its documented default",
			path, err.Error())
		return
	}
	for i, raw := range strings.Split(string(data), "\n") {
		var (
			line, key, value string
			comment int
			known bool
			ok bool
			set_err error
		)
		line = raw
		/* a comment runs to the end of the line */
		comment = strings.IndexByte(line, '#')
		if (comment >= 0) {
			line = line[:comment]
		}
		line = strings.TrimSpace(line)
		if (line == "") {
			continue
		}
		key, value, ok = split_line(line)
		if (!ok) {
			c.warn("%s:%d: not \"key = value\", ignored: %q", path, i + 1, line)
			continue
		}
		known, set_err = c.set(key, value, FROM_FILE)
		if (!known) {
			/* an unknown key is not an error: a file written for a later
			 * version must not change the behaviour of this one */
			c.warn("%s:%d: unknown key %q ignored", path, i + 1, key)
			continue
		}
		if (set_err != nil) {
			c.warn("%s:%d: %s; %s keeps its previous value (%s)",
				path, i + 1, set_err.Error(), key, c.items[key].value)
		}
	}
}

/* split_line splits one "key = value" line. */
func split_line(line string) (key string, value string, ok bool) {
	var (
		parts []string
	)
	parts = strings.SplitN(line, "=", 2)
	if (len(parts) != 2) {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(parts[0]))
	value = unquote(strings.TrimSpace(parts[1]))
	if (key == "") {
		return "", "", false
	}
	return key, value, true
}

/* unquote drops one pair of surrounding double quotes, so that a value may be
 * written either way. */
func unquote(value string) string {
	if (len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`)) {
		return value[1:len(value) - 1]
	}
	return value
}

/*
 * set applies one key/value pair. known is false when the key is not one of
 * this file's keys; err is non-nil when it is, but the value cannot be taken
 * (then the previous value is kept).
 */
func (c *Config) set(key string, value string, source Source) (known bool, err error) {
	var (
		n int
	)
	if (!known_key(key)) {
		return false, nil
	}
	switch (key) {
	case KEY_ISOLATION_PING_TIMEOUT_SECONDS:
		n, err = positive_int(value)
		if (err != nil) {
			return true, err
		}
		c.Isolation_ping_timeout_seconds = n
		c.remember(key, strconv.Itoa(n), source)
	case KEY_SANLOCK_CMD_TIMEOUT_SECONDS:
		n, err = positive_int(value)
		if (err != nil) {
			return true, err
		}
		c.Sanlock_cmd_timeout_seconds = n
		c.remember(key, strconv.Itoa(n), source)
	case KEY_HASTATE_ROUND_BUDGET_SECONDS:
		n, err = positive_int(value)
		if (err != nil) {
			return true, err
		}
		c.Hastate_round_budget_seconds = n
		c.remember(key, strconv.Itoa(n), source)
	}
	return true, nil
}

/* known_key reports whether the key belongs to this file. */
func known_key(key string) bool {
	for _, k := range key_order {
		if (k == key) {
			return true
		}
	}
	return false
}

/* positive_int takes a whole number of seconds (or of attempts) that is at
 * least one: zero would mean "no timeout" and "no attempt at all". */
func positive_int(value string) (int, error) {
	var (
		n int
		err error
	)
	n, err = strconv.Atoi(value)
	if (err != nil) {
		return 0, fmt.Errorf("%q is not a whole number", value)
	}
	if (n < 1) {
		return 0, fmt.Errorf("%d is not at least 1", n)
	}
	return n, nil
}

/* warn records one thing that could not be used as written. */
func (c *Config) warn(format string, args ...any) {
	c.warnings = append(c.warnings, fmt.Sprintf(format, args...))
}
