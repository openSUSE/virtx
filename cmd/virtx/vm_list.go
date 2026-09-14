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
	"slices"
	"strings"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/ts"
)

func vm_list_req() {
	if (len(virtx.custom_values) != 0 && len(virtx.custom_values) != len(virtx.custom_names)) {
		logger.Fatal("--custom-value must be given for every --custom-name, or omitted entirely")
	}
	for i := range virtx.custom_names {
		var value string
		if (i < len(virtx.custom_values)) {
			value = virtx.custom_values[i]
		}
		virtx.vm_list_options.Filter.Custom = append(virtx.vm_list_options.Filter.Custom,
			openapi.CustomField{ Name: virtx.custom_names[i], Value: value })
	}
	virtx.path = "/vms"
	virtx.method = "GET"
	virtx.arg = &virtx.vm_list_options
	virtx.result = &openapi.VmList{}
}

func vm_list(list *openapi.VmList) {
	slices.SortFunc(list.Items, func(a, b openapi.VmListItem) int {
		if (a.Fields.Name != b.Fields.Name) {
			return strings.Compare(a.Fields.Name, b.Fields.Name)
		}
		return strings.Compare(a.Uuid, b.Uuid)
	})
	fmt.Fprintf(virtx.w, "UUID\tNAME\tHOST\t[HOSTNAME]\tCUSTOM\tSTATE\tAGE\n")
	for _, item := range (list.Items) {
		fmt.Fprintf(virtx.w, "%s\t%s\t%s\t%s\t%v\t%s\t%s\n", item.Uuid, item.Fields.Name, item.Fields.Host,
			host_name(item.Fields.Host), item.Fields.Custom, item.Fields.Runstate, ts.Since(item.Fields.Ts))
	}
}
