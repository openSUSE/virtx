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
	"encoding/json"
	"bytes"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/vmdef"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/inventory"
)

func vm_get(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		uuid, xml string
		vmdata inventory.Vmdata
		vm openapi.Vm
		buf bytes.Buffer
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
	xml, err = hypervisor.Dumpxml(uuid)
	if (err != nil) {
		logger.Log("hypervisor.Dumpxml failed: %s", err.Error())
		http.Error(w, "could not get VM", http.StatusFailedDependency)
		return
	}
	err = vmdef.From_xml(&vm.Def, xml)
	if (err != nil) {
		logger.Log("vmdef.From_xml failed: %s", err.Error())
		http.Error(w, "invalid VM data", http.StatusInternalServerError)
		return
	}
	vm.Uuid = uuid
	vm.Runinfo = vmdata.Runinfo
	vm.Ts = vmdata.Ts
	vm.Stats, err = hypervisor.Get_Vmstats(uuid)
	if (err != nil) {
		logger.Log("hypervisor.Get_Vmstats failed: %s", err.Error())
		http.Error(w, "could not get VM stats", http.StatusNotFound)
		return
	}
	err = json.NewEncoder(&buf).Encode(&vm)
	if (err != nil) {
		logger.Log("failed to encode JSON")
		http.Error(w, "Failed to encode JSON", http.StatusInternalServerError)
		return
	}
	httpx.Do_response(w, http.StatusOK, &buf)
}
