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
package virtx

import (
	"net/http"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/inventory"
)

func vm_pause(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		uuid string
		vmdata inventory.Vmdata
		vr httpx.Request
	)
	vr, err = httpx.Decode_request_body(r, nil)
	if (err != nil) {
		logger.Log(err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	uuid = r.PathValue("uuid")
	if (uuid == "") {
		http.Error(w, "could not get uuid", http.StatusBadRequest)
		return
	}
	vmdata, err = inventory.Get_vm(uuid)
	if (err != nil) {
		http.Error(w, "unknown uuid", http.StatusNotFound)
		return
	}
	if (http_host_is_remote(vmdata.Runinfo.Host)) {
		http_proxy_request(vmdata.Runinfo.Host, w, vr)
		return
	}
	err = hypervisor.Pause_domain(uuid)
	if (err != nil) {
		logger.Log("hypervisor.Pause_domain failed: %s", err.Error())
		http.Error(w, "could not pause VM", http.StatusFailedDependency)
		return
	}
	httpx.Do_response(w, http.StatusNoContent, nil)
}
