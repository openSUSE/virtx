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
package inventory

import (
	"math"
)

/* calculate difference between two overflowing counters */
func Counter_delta_uint64(c1 uint64, c0 uint64) uint64 {
	if (c1 >= c0) {
		return c1 - c0
	} else {
		return math.MaxUint64 - c0 + c1 + 1
	}
}

func Counter_delta_int64(c1 int64, c0 int64) int64 {
	if (c1 >= c0) {
		return c1 - c0
	} else {
		return (math.MaxInt64 - c0) + (c1 - math.MinInt64) + 1
	}
}
