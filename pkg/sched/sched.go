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
package sched

import (
	"slices"

	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/reg"

	"suse.com/virtx/pkg/model"
)

/*
 * Schedule_vmdef selects the best available host for the given VM definition.
 * Returns the host UUID, or "" if no suitable host is found.
 *
 * Filtering (all criteria must pass):
 *  1. Cstate == CSTATE_ACTIVE
 *  2. Cpudef.Arch matches vmdef
 *  3. Sufficient memory (Memoryavailable or Hpavailable per vmdef.Memory.Hp)
 *  4. CPU model compatibility (host-passthrough skips this check)
 *
 * Scoring: for now host with the most available memory (normal or Hp) wins.
 * CPU-aware scoring (using cluster-wide overcommit factor) is deferred.
 */
func Schedule_vmdef(vmdef *openapi.Vmdef) string {
	var (
		filter openapi.HostListFields
		list openapi.HostList
		check_model bool = (vmdef.Cpudef.Model != "host-passthrough")
		best_host string
		best_mem int32 = -1
	)
	filter.Cstate = openapi.CSTATE_ACTIVE
	filter.Cpudef.Arch = vmdef.Cpudef.Arch
	if (vmdef.Memory.Hp) {
		filter.Hpavailable = vmdef.Memory.Total
	} else {
		filter.Memoryavailable = vmdef.Memory.Total
	}
	list = inventory.Search_hosts(filter)
	if (len(list.Items) == 0) {
		return ""
	}
	for _, item := range list.Items {
		/* cpu model compatibility check (NFS read per candidate) */
		if (check_model) {
			models, err := reg.Load_cpumodels(item.Uuid)
			if (err != nil) {
				logger.Log("%s", err.Error())
				continue
			}
			if (!slices.Contains(models, vmdef.Cpudef.Model)) {
				continue
			}
		}
		/* score by available memory of the relevant kind */
		var mem int32
		if (vmdef.Memory.Hp) {
			mem = item.Fields.Hpavailable
		} else {
			mem = item.Fields.Memoryavailable
		}
		if (mem > best_mem) {
			best_mem = mem
			best_host = item.Uuid
		}
	}
	return best_host
}
