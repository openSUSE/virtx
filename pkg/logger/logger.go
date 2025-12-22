/*
 * Copyright (c) 2024-2025 SUSE LLC
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
package logger

import (
	"fmt"
	"strings"
	"os"
	"runtime"
	"path/filepath"
	"sync"
	"time"
)

var l struct {
	m sync.Mutex
	debug bool
}

/* called under lock and with the caller string already set */
func do_log(caller string, format string, args ...interface{}) {
	caller = filepath.Base(caller)
	caller = strings.TrimSuffix(caller, filepath.Ext(caller))

	var now string = time.Now().UTC().Format(time.DateTime)
	var prefix string = fmt.Sprintf("%s %s: %s: ", now, "virtx", caller)

	fmt.Fprint(os.Stderr, prefix)
	fmt.Fprintf(os.Stderr, format, args...)
	fmt.Fprintln(os.Stderr)
}

func Log(format string, args ...interface{}) {
	l.m.Lock()
	defer l.m.Unlock()

	var caller string
	_, caller, _, _ = runtime.Caller(1)
	do_log(caller, format, args...)
}

func Debug(format string, args ...interface{}) {
	l.m.Lock()
	defer l.m.Unlock()

	if (!l.debug) {
		return
	}
	var caller string
	_, caller, _, _ = runtime.Caller(1)
	do_log(caller, format, args...)
}

func Fatal(format string, args ...interface{}) {
	l.m.Lock()
	defer l.m.Unlock()

	var caller string
	_, caller, _, _ = runtime.Caller(1)
	do_log(caller, format, args...)
	os.Exit(1)
}

func Set_debug(d bool) {
	l.m.Lock()
	defer l.m.Unlock()

	l.debug = d
}
