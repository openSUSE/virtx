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
package constants

const (
	KiB = 1024
	MiB = 1024 * KiB
	GiB = 1024 * MiB
	TiB = 1024 * GiB
)

const (
	REG_DIR = "/vms/xml/"
	DS_DIR = "/vms/ds/"
	HTTP_MAX_BODY_LEN = 1048576
	VM_NAME_MAX = 32
	NET_NAME_MAX = 32
	CPU_NAME_MAX = 32
	GENID_LEN = 36
	DISKS_MAX = 20
	NETS_MAX = 8
	MAC_LEN = 17
	VLAN_MAX = 4094
)
