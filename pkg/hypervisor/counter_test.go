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
package hypervisor

import (
	"math"
	"testing"
)

func Test_counter_delta_uint64(t *testing.T) {
	cases := []struct {
		name string
		c1   uint64
		c0   uint64
		want uint64
	}{
		{"normal",       100, 50,                  50},
		{"equal",        50,  50,                  0},
		{"zero_to_zero", 0,   0,                   0},
		{"max_delta",    math.MaxUint64, 0,         math.MaxUint64},
		{"wrap_by_one",  0,   math.MaxUint64,       1},
		{"wraparound",   5,   math.MaxUint64 - 4,   10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Counter_delta_uint64(tc.c1, tc.c0)
			if (got != tc.want) {
				t.Errorf("Counter_delta_uint64(%d, %d) = %d, want %d", tc.c1, tc.c0, got, tc.want)
			}
		})
	}
}

func Test_counter_delta_int64(t *testing.T) {
	cases := []struct {
		name string
		c1   int64
		c0   int64
		want int64
	}{
		{"normal",       100, 50,                   50},
		{"equal",        50,  50,                   0},
		{"negative",     -10, -20,                  10},
		{"cross_zero",   5,   -5,                   10},
		{"wrap_by_one",  math.MinInt64, math.MaxInt64, 1},
		{"wraparound",   math.MinInt64 + 5, math.MaxInt64 - 4, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Counter_delta_int64(tc.c1, tc.c0)
			if (got != tc.want) {
				t.Errorf("Counter_delta_int64(%d, %d) = %d, want %d", tc.c1, tc.c0, got, tc.want)
			}
		})
	}
}
