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
package virtx

import (
	"net/http"
	"encoding/json"
	"bytes"

	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/inventory"
)

func vm_stats_get(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		uuid string
		vminfo inventory.VmInfo
		vmstats openapi.Vmstats
		buf bytes.Buffer
		vr httpx.Request
	)
	vr, err = httpx.Decode_request_body(r, nil)
	if (err != nil) {
		logger.Log("%s", err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	uuid = r.PathValue("uuid")
	if (uuid == "") {
		http.Error(w, "vm_stats_get: Failed to decode parameters", http.StatusBadRequest)
		return
	}
	vminfo, err = inventory.Get_vminfo(uuid)
	if (err != nil) {
		http.Error(w, "unknown uuid", http.StatusNotFound)
		return
	}
	if (http_host_is_remote(vminfo.Host)) {
		http_proxy_request(vminfo.Host, w, vr)
		return
	}
	vmstats, err = hypervisor.Get_vmstats(uuid)
	if (err != nil) {
		http.Error(w, "vm_stats_get: stats not found", http.StatusNotFound)
		return
	}
	err = json.NewEncoder(&buf).Encode(&vmstats)
	if (err != nil) {
		logger.Log("failed to encode JSON")
		http.Error(w, "Failed to encode JSON", http.StatusInternalServerError)
		return
	}
	httpx.Do_response(w, http.StatusOK, &buf)
}
