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
package ts

import (
	"time"
)

func Now() int64 {
	return time.Now().UTC().UnixMilli()
}

func Since(t int64) time.Duration {
	return time.Duration(Now() - t) * time.Millisecond
}

func String(t int64) string {
	if (t == 0) {
		return ""
	}
	return time.UnixMilli(t).UTC().Format(time.DateTime)
}
