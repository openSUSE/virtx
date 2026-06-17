/*
 * Copyright (c) 2025-2026 SUSE LLC
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
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/ts"
)

func vm_list_req() {
	virtx.path = "/vms"
	virtx.method = "GET"
	virtx.arg = &virtx.vm_list_options
	virtx.result = &openapi.VmList{}
}

func vm_list(list *openapi.VmList) {

	fmt.Fprintf(virtx.w, "UUID\tNAME\tHOST\tVLANID\t \tSTATE\tAGE\n")

	for _, item := range (list.Items) {
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%6d\t%v\t%s\t%s\n", item.Uuid, item.Fields.Name, item.Fields.Host,
			item.Fields.Vlanid, item.Fields.Custom, item.Fields.Runstate, ts.Since(item.Fields.Ts))
	}
}
