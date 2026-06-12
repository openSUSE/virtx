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
 * the information stored here is read from libvirt in the first system_info_loop
 * and should be accessed only after it.
 */
package machine

type Machine struct {
	uuid string
	arch string
}

var m Machine

func Uuid() string {
	return m.uuid
}
func Set_uuid(uuid string) {
	m.uuid = uuid
}
func Arch() string {
	return m.arch
}
func Set_arch(arch string) {
	m.arch = arch
}
