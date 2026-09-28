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

package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
)

/* vm_log_code_parse returns the code for an operation name (ie "VmBoot") or an event name (ie "watchdog"). */
func vm_log_code_parse(s string) int16 {
	var (
		op openapi.OperationCode
		event openapi.EventCode
	)
	if (op.Parse(s) == nil) {
		return int16(op)
	}
	if (event.Parse(s) == nil) {
		return int16(event)
	}
	logger.Fatal("invalid --code %q: not an operation or event name", s)
	return 0
}

/* vm_log_code_string returns the operation or event name of a code. */
func vm_log_code_string(code int16) string {
	var class openapi.LogClass
	class.From_code(code)
	if (class == openapi.LOG_CLASS_OP) {
		return openapi.OperationCode(code).String()
	}
	return openapi.EventCode(code).String()
}

func vm_log_req(uuid string, class string, code string, from string, to string) {
	if (class != "") {
		var c openapi.LogClass
		err := c.Parse(class)
		if (err != nil) {
			logger.Fatal("invalid --class %q: %s", class, err.Error())
		}
		virtx.vm_log_options.Class = int16(c)
	}
	if (code != "") {
		virtx.vm_log_options.Code = vm_log_code_parse(code)
	}
	var (
		from_ms int64
		from_off int
		from_has bool
		to_ms int64
		to_off int
		to_has bool
	)
	from_ms, from_off, from_has = parse_ts(from, false)
	to_ms, to_off, to_has = parse_ts(to, true)
	if (from_has && to_has && from_off != to_off) {
		logger.Fatal("--from and --to specify different timezone offsets (%s vs %s); use the same offset for both",
			tz_label(from_off), tz_label(to_off))
	}
	virtx.vm_log_options.From = from_ms
	virtx.vm_log_options.To = to_ms
	if (from_has) {
		virtx.log_tz = from_off
	} else if (to_has) {
		virtx.log_tz = to_off
	}
	virtx.path = fmt.Sprintf("/vms/%s/log", uuid)
	virtx.method = "GET"
	virtx.arg = &virtx.vm_log_options
	virtx.result = &openapi.LogList{}
}

func vm_log(list *openapi.LogList) {
	label := tz_label(virtx.log_tz)
	fmt.Fprintf(virtx.w, "START (%s)\tEND (%s)\tCLASS\tCODE\tSTATE\tMESSAGES\n", label, label)
	for _, item := range (list.Items) {
		var (
			class openapi.LogClass
			end, msgs string
		)
		class.From_code(item.Code)
		if (item.State != openapi.OPERATION_NONE) { /* events have no end time */
			end = format_ts(item.Te, virtx.log_tz)
		}
		msgs = item.Msgs
		if (item.Msge != "") {
			msgs += " -> " + item.Msge
		}
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			format_ts(item.Ts, virtx.log_tz), end, class, vm_log_code_string(item.Code),
			item.State, msgs)
	}
}

/* format_ts renders a UTC epoch-ms timestamp in the given fixed offset (seconds east of UTC). */
func format_ts(t int64, offset int) string {
	if (t == 0) {
		return ""
	}
	return time.UnixMilli(t).In(time.FixedZone("", offset)).Format("2006-01-02 15:04:05.000")
}

func tz_label(offset int) string {
	if (offset == 0) {
		return "UTC"
	}
	sign := "+"
	if (offset < 0) {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("%s%02d:%02d", sign, offset / 3600, (offset % 3600) / 60)
}

/*
 * parse_ts accepts a raw epoch-ms integer, an RFC3339 timestamp
 * (with a literal "Z" for the local TZ, or a numeric TZ offset),
 * or the bare "YYYY-MM-DD HH:MM:SS" layout (assumed UTC).
 *
 * It returns the epoch-ms value, the offset in seconds east of UTC,
 * and whether an explicit timezone was given.
 *
 * round_up rounds a sub-second-less input up to the last ms of that second
 */
func parse_ts(s string, round_up bool) (int64, int, bool) {
	var (
		ms int64
		t time.Time
		err error
	)
	if (s == "") {
		return 0, 0, false
	}
	ms, err = strconv.ParseInt(s, 10, 64)
	if (err == nil) {
		return ms, 0, false
	}
	round_up = round_up && !strings.Contains(s, ".")
	t, err = time.Parse(time.RFC3339, s)
	if (err == nil) {
		_, offset := t.Zone()
		if (round_up) {
			t = t.Add(999 * time.Millisecond)
		}
		return t.UnixMilli(), offset, true
	}
	t, err = time.ParseInLocation(time.DateTime, s, time.UTC)
	if (err != nil) {
		logger.Fatal("invalid timestamp %q: use epoch-ms, RFC3339, or \"YYYY-MM-DD HH:MM:SS\" (UTC)", s)
	}
	if (round_up) {
		t = t.Add(999 * time.Millisecond)
	}
	return t.UnixMilli(), 0, false
}
