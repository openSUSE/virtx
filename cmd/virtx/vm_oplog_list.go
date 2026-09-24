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

func vm_oplog_list_req(uuid string, op string, from string, to string) {
	if (op != "") {
		var code openapi.OperationCode
		err := code.Parse(op)
		if (err != nil) {
			logger.Fatal("invalid --op %q: %s", op, err.Error())
		}
		virtx.vm_oplog_list_options.Op = int16(code)
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
	virtx.vm_oplog_list_options.From = from_ms
	virtx.vm_oplog_list_options.To = to_ms
	if (from_has) {
		virtx.oplog_tz = from_off
	} else if (to_has) {
		virtx.oplog_tz = to_off
	}
	virtx.path = fmt.Sprintf("/vms/%s/oplog", uuid)
	virtx.method = "GET"
	virtx.arg = &virtx.vm_oplog_list_options
	virtx.result = &openapi.OplogList{}
}

func vm_oplog_list(list *openapi.OplogList) {
	label := tz_label(virtx.oplog_tz)
	fmt.Fprintf(virtx.w, "START (%s)\tEND (%s)\tOP\tSTATE\tMSGS\tMSGE\n", label, label)
	for _, item := range (list.Items) {
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			format_ts(item.Ts, virtx.oplog_tz), format_ts(item.Te, virtx.oplog_tz),
			openapi.OperationCode(item.Op).String(), item.State, item.Msgs, item.Msge)
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
