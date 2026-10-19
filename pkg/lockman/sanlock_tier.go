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
 * The effective sanlock tier: io_timeout and watchdog_fire_timeout, plus where
 * each of the two values came from.
 *
 * Every time window of the decision layer is a function of these two numbers,
 *
 *	DEAD_CONFIRM_MS = 8 x io_timeout + watchdog_fire_timeout
 *
 * The order of the sources is fixed:
 *
 *	1. the running sanlock, asked for the settings it is really using;
 *	2. /etc/sanlock/sanlock.conf, for whatever sanlock could not answer;
 *
 * A key neither source carries is not an error and stops nothing: it takes the
 * default. What must never be silent is *where* a value came from, so every
 * value carries its source and Lines() prints it for the startup log.
 */
package lockman

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"suse.com/virtx/pkg/paths"
)

/* The two settings every window is derived from, spelled the way both sanlock's
 * sanlock.conf and `sanlock client status -D` spell them. */
const (
	KEY_IO_TIMEOUT = "io_timeout"
	KEY_WATCHDOG_FIRE_TIMEOUT = "watchdog_fire_timeout"
)

/* Where a value came from. This word is what the startup log prints as the
 * value's source, so it must be one of these three and nothing else. */
const (
	TIER_FROM_SANLOCK = "sanlock"
	TIER_FROM_FILE = "file"
	TIER_FROM_DEFAULT = "default"
)

/*
 * SANLOCK_CONF is the file sanlock reads its settings from. The daemon reads it
 * once, at its own startup, so the file may be older than what the daemon is
 * using - which is exactly why the running daemon is asked first.
 *
 * Measured on this test cluster (h11/h12/h13, 2026-10-05): /etc/sanlock is
 * drwx------ root:root and the file inside it is 0644, while virtxd runs as
 * qemu, so reading it as qemu fails with EACCES. That is a property of this
 * deployment, not of sanlock; where the file is readable, it is the fallback
 * this function uses. Here it means the running daemon's answer is in practice
 * the only real source, and a value that does not come from it comes from the
 * documented default - with the source saying which of the two it was.
 */
const SANLOCK_CONF = "/etc/sanlock/sanlock.conf"

/* The documented defaults of both keys, and what this file uses when neither
 * sanlock nor the file carries a key. 10 is also sanlock's own compiled default
 * (DEFAULT_IO_TIMEOUT in sanlock_internal.h), and 10/60 is the tier the cluster
 * has been running with since 2026-10-02. */
const (
	DEFAULT_IO_TIMEOUT_SECONDS = 10
	DEFAULT_WATCHDOG_FIRE_TIMEOUT_SECONDS = 60
)

/*
 * DEAD_CONFIRM_MS is sanlock's death threshold: a peer's host_id lease counts as DEAD
 * once it has gone unrenewed for id_renewal_fail + watchdog_fire_timeout, and
 * timeouts.h line 20 defines id_renewal_fail_seconds = 8 x io_timeout_seconds.
 *
 *	DEAD_CONFIRM_MS = 8 x io_timeout + watchdog_fire_timeout   (= 140 s at 10/60)
 *
 * The multiplier is named rather than folded into the arithmetic, because the
 * formula is quoted as such in the design and a reader must be able to match
 * the code to it term by term.
 */
const DEAD_CONFIRM_IO_MULTIPLIER = 8

/* How long the one sanlock query may take. The read happens once, at startup,
 * and virtxd must not hang on a wedged sanlock. */
const TIER_QUERY_TIMEOUT_SECONDS = 5

/*
 * Tier is the effective sanlock configuration. The two values are what the
 * windows are computed from; the two sources say where each value came from and
 * are printed; Warning is the one line that says, when it applies, that a value
 * could not be taken from a source that carries it.
 */
type Tier struct {
	Io_timeout_seconds int
	Watchdog_fire_timeout_seconds int
	Io_source string
	Watchdog_source string
	Warning string
}

/*
 * Default_tier is the documented default tier with both sources marked
 * "default". It is what a package used without virtxd's main() gets (a unit
 * test, for instance), so that such a package behaves exactly as the deployed
 * default rather than as a zero value.
 */
func Default_tier() Tier {
	return Tier{
		Io_timeout_seconds: DEFAULT_IO_TIMEOUT_SECONDS,
		Watchdog_fire_timeout_seconds: DEFAULT_WATCHDOG_FIRE_TIMEOUT_SECONDS,
		Io_source: TIER_FROM_DEFAULT,
		Watchdog_source: TIER_FROM_DEFAULT,
	}
}

/* Dead_confirm_seconds is this tier's sanlock death threshold, as derived above. */
func (t Tier) Dead_confirm_seconds() int {
	return DEAD_CONFIRM_IO_MULTIPLIER * t.Io_timeout_seconds + t.Watchdog_fire_timeout_seconds
}

/*
 * Read_tier returns the effective tier: what the running sanlock says, then the
 * configuration file for whatever sanlock could not answer, then the documented
 * defaults. It never fails - a tier that cannot be read is the documented
 * default with a source of "default", which is what the user ruled on
 * 2026-10-05: the value may be the default, but the log must say so.
 */
func Read_tier() Tier {
	return read_tier(sanlock_settings, SANLOCK_CONF)
}

/*
 * read_tier is Read_tier with both of its inputs injected, so that every
 * fallback - no sanlock answer, a missing key, an unreadable file - is a unit
 * test rather than something a reader has to believe.
 */
func read_tier(query func() (map[string]int, error), conf_path string) Tier {
	var (
		t Tier = Default_tier()
		from_sanlock map[string]int
		from_file map[string]int
		sanlock_err error
		file_err error
		missing []string
		reasons []string
		defaults []string
	)
	from_sanlock, sanlock_err = query()
	from_file, file_err = conf_settings(conf_path)
	for _, key := range []string{ KEY_IO_TIMEOUT, KEY_WATCHDOG_FIRE_TIMEOUT } {
		var (
			value int
			ok bool
		)
		value, ok = from_sanlock[key]
		if (ok) {
			t.from(key, value, TIER_FROM_SANLOCK)
			continue
		}
		value, ok = from_file[key]
		if (ok) {
			t.from(key, value, TIER_FROM_FILE)
			continue
		}
		/* neither source carried it: the value keeps the documented default,
		 * and the warning below says which key that was */
		missing = append(missing, key)
	}
	if (len(missing) > 0) {
		/*
		 * One warning, and only when a value really did not come from a source
		 * that carries it. It names those keys and the documented default each
		 * of them fell back to, so that the log cannot be read as "everything
		 * is a default" when only one of the two was: the warning must describe
		 * what happened, not the worst thing that could have happened. A
		 * sanlock answer makes the file irrelevant, so a file we could not read
		 * is not warned about as long as the daemon carried both values.
		 */
		if (sanlock_err != nil) {
			reasons = append(reasons, "the running sanlock did not answer (" + sanlock_err.Error() + ")")
		}
		if (file_err != nil) {
			reasons = append(reasons, conf_path + " could not be read (" + file_err.Error() + ")")
		}
		if (len(reasons) == 0) {
			reasons = append(reasons, "neither source carries it")
		}
		for _, key := range missing {
			defaults = append(defaults, fmt.Sprintf("%s=%d s", key, default_setting(key)))
		}
		t.Warning = fmt.Sprintf("no source carries %s (%s): the documented defaults are used, %s",
			strings.Join(missing, ", "), strings.Join(reasons, "; "), strings.Join(defaults, ", "))
	}
	return t
}

/* from records one value with its source. */
func (t *Tier) from(key string, value int, source string) {
	switch (key) {
	case KEY_IO_TIMEOUT:
		t.Io_timeout_seconds = value
		t.Io_source = source
	case KEY_WATCHDOG_FIRE_TIMEOUT:
		t.Watchdog_fire_timeout_seconds = value
		t.Watchdog_source = source
	}
}

/* default_setting is the documented default of one key, so that the warning can
 * name the value a key fell back to instead of claiming both at once. */
func default_setting(key string) int {
	switch (key) {
	case KEY_IO_TIMEOUT:
		return DEFAULT_IO_TIMEOUT_SECONDS
	case KEY_WATCHDOG_FIRE_TIMEOUT:
		return DEFAULT_WATCHDOG_FIRE_TIMEOUT_SECONDS
	}
	return 0
}

/*
 * Lines is what the startup log prints about the tier: the two values with the
 * source of each, the one number everything else is derived from, and the
 * warning when a value did not come from a source that carries it. The values
 * and their derivation are both spelled out, so that the log alone answers
 * "which tier is this, where did each number come from, and what does it
 * imply".
 */
func (t Tier) Lines() []string {
	var (
		lines []string
	)
	lines = append(lines, fmt.Sprintf(
		"sanlock tier: io_timeout=%d s source=%s watchdog_fire_timeout=%d s source=%s: DEAD_CONFIRM_MS=%d s (= %d x io_timeout + watchdog_fire_timeout)",
		t.Io_timeout_seconds, t.Io_source,
		t.Watchdog_fire_timeout_seconds, t.Watchdog_source,
		t.Dead_confirm_seconds(), DEAD_CONFIRM_IO_MULTIPLIER))
	if (t.Warning != "") {
		lines = append(lines, "sanlock tier WARNING: " + t.Warning)
	}
	return lines
}

/*
 * sanlock_settings asks the running daemon what it is really using. `sanlock
 * client status -D` prints one `daemon` block whose fields are com.io_timeout
 * and com.watchdog_fire_timeout (sanlock's cmd.c, print_state_daemon), and
 * those are the settings the daemon renews with.
 *
 * Only the daemon block is read. The `s` (lockspace) blocks carry an io_timeout
 * of their own, but it is the same number unless a lockspace was added with an
 * explicit `-o` override - which virtxd never does: lockman.go calls
 * `client add_lockspace` without one. Reading both keys from the one block also
 * keeps them a pair, which is how the formula uses them.
 *
 * No answer, an unreadable answer and a malformed one all come back as "no
 * setting"; the caller then falls back rather than guessing here.
 */
func sanlock_settings() (map[string]int, error) {
	var (
		ctx context.Context
		cancel context.CancelFunc
		cmd *exec.Cmd
		output []byte
		err error
	)
	ctx, cancel = context.WithTimeout(context.Background(),
		time.Duration(TIER_QUERY_TIMEOUT_SECONDS) * time.Second)
	defer cancel()
	cmd = exec.CommandContext(ctx, paths.Get("SANLOCK"), "client", "status", "-D")
	output, err = cmd.Output()
	if (err != nil) {
		return nil, fmt.Errorf("sanlock client status -D: %s", err.Error())
	}
	return parse_sanlock_status(string(output)), nil
}

/*
 * parse_sanlock_status reads the daemon block of `sanlock client status -D`.
 * The block starts at the `daemon <name>` header, ends at the next section
 * header (a line that starts in column one: `s ...`, `r ...`, `p ...`), and its
 * fields are `key=value` tokens printed one per line. Anything not in that
 * block is ignored - the lockspace block below it repeats `io_timeout`, and
 * taking that repetition for the daemon's own setting would be reading the
 * wrong one of the two.
 */
func parse_sanlock_status(output string) map[string]int {
	var (
		settings map[string]int = make(map[string]int)
		in_daemon bool
	)
	for _, raw := range strings.Split(output, "\n") {
		if (strings.HasPrefix(raw, "daemon ")) {
			in_daemon = true
			continue
		}
		if (!in_daemon) {
			continue
		}
		if (raw == "" || (raw[0] != ' ' && raw[0] != '\t')) {
			/* the next section starts: the daemon block is over */
			in_daemon = false
			continue
		}
		var (
			key string
			value int
			ok bool
		)
		key, value, ok = tier_field(strings.TrimSpace(raw))
		if (ok) {
			settings[key] = value
		}
	}
	return settings
}

/*
 * conf_settings parses the two settings out of sanlock's configuration file.
 * The format is `key = value`, `#` starts a comment, and a key that the file
 * does not set is simply not returned - the shipped file has both names
 * commented out, so "no value" is the normal case for a host that never chose a
 * tier. A file that cannot be read is an error rather than an empty set, so
 * that the caller can say in the log which of the two reasons applied.
 */
func conf_settings(path string) (map[string]int, error) {
	var (
		data []byte
		err error
		settings map[string]int = make(map[string]int)
	)
	data, err = os.ReadFile(path)
	if (err != nil) {
		return nil, err
	}
	for _, raw := range strings.Split(string(data), "\n") {
		var (
			line string = raw
			comment int
			key string
			value int
			ok bool
		)
		comment = strings.IndexByte(line, '#')
		if (comment >= 0) {
			line = line[:comment]
		}
		key, value, ok = tier_conf_field(strings.TrimSpace(line))
		if (ok) {
			settings[key] = value
		}
	}
	return settings, nil
}

/*
 * tier_field reads one `key=value` token and returns it when it is one of the
 * two settings and a value sanlock itself would accept. sanlock's own
 * read_config_file() takes io_timeout and watchdog_fire_timeout only when the
 * value is greater than zero, so zero or a negative number is not a setting
 * here either; this also filters `write_init_io_timeout`, whose key is a
 * different key and not a prefix match.
 */
func tier_field(token string) (key string, value int, ok bool) {
	var (
		parts []string
		n int
		err error
	)
	parts = strings.SplitN(token, "=", 2)
	if (len(parts) != 2) {
		return "", 0, false
	}
	key = parts[0]
	if (key != KEY_IO_TIMEOUT && key != KEY_WATCHDOG_FIRE_TIMEOUT) {
		return "", 0, false
	}
	n, err = strconv.Atoi(parts[1])
	if (err != nil || n < 1) {
		return "", 0, false
	}
	return key, n, true
}

/* tier_conf_field is tier_field for a configuration line, where the two sides
 * may be written `key = value` with spaces around the `=`. */
func tier_conf_field(line string) (key string, value int, ok bool) {
	var (
		parts []string
	)
	parts = strings.SplitN(line, "=", 2)
	if (len(parts) != 2) {
		return "", 0, false
	}
	return tier_field(strings.TrimSpace(parts[0]) + "=" + strings.TrimSpace(parts[1]))
}
