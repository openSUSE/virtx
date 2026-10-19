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
package coord

import "testing"

func Test_parse_switch(t *testing.T) {
	var (
		req Switch_request
		ok bool
	)
	req, ok = parse_switch("# written\n\n" +
		"target = \"aaaa\"\n" +
		"requested_by = \"bbbb\"\n" +
		"requested_at = 1000\n" +
		"deadline_ms = 4000\n")
	if (!ok) {
		t.Fatalf("a well formed request was not read")
	}
	if (req.Target != "aaaa") {
		t.Fatalf("target: %q", req.Target)
	}
	if (req.Requested_by != "bbbb") {
		t.Fatalf("requested_by: %q", req.Requested_by)
	}
	if (req.Deadline_ms != 4000) {
		t.Fatalf("deadline: %d", req.Deadline_ms)
	}
}

func Test_parse_switch_without_target(t *testing.T) {
	var (
		req Switch_request
		ok bool
	)
	req, ok = parse_switch("requested_by = \"bbbb\"\n")
	if (ok) {
		t.Fatalf("a request without a target was read as one: %+v", req)
	}
}

func Test_switch_fresh(t *testing.T) {
	var (
		req Switch_request
	)
	req = Switch_request{ Target: "aaaa", Deadline_ms: 1000 }
	if (!switch_fresh(req, 999)) {
		t.Fatalf("a request before its deadline must be fresh")
	}
	if (switch_fresh(req, 1000)) {
		t.Fatalf("a request at its deadline has run out")
	}
}
