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

package coord

import (
	"suse.com/virtx/pkg/hastate"
)

/*
 * The office this host holds is published in its self report, because the host
 * that needs it most is the one that can no longer ask: a host cut off from the
 * management network still writes its report to the shared storage, and the host
 * deciding whether it is isolated reads the office from there.
 */
func init() {
	hastate.Set_office(held_office_epoch)
}

/* held_office_epoch is the epoch of the office this host holds, 0 for none. */
func held_office_epoch() int64 {
	var (
		epoch int64
		ok bool
	)
	epoch, ok = Term()
	if (!ok) {
		return 0
	}
	return epoch
}
