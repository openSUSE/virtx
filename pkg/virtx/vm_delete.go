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
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/vmdef"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/storage"
)

func vm_delete(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		o openapi.VmDeleteOptions
		uuid, xml string
		vmdata inventory.Vmdata
		vm openapi.Vmdef
		vr httpx.Request
		state openapi.Vmrunstate
	)
	vr, err = httpx.Decode_request_body(r, &o)
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
	state = vmdata.Runinfo.Runstate
	if (state != openapi.RUNSTATE_POWEROFF && state != openapi.RUNSTATE_CRASHED) {
		http.Error(w, "VM is not powered off", http.StatusUnprocessableEntity)
		return
	}
	xml, err = hypervisor.Dumpxml(uuid)
	if (err != nil) {
		logger.Log("hypervisor.Dumpxml failed: %s", err.Error())
		http.Error(w, "could not Get VM XML", http.StatusFailedDependency)
		return
	}
	err = vmdef.From_xml(&vm, xml)
	if (err != nil) {
		logger.Log("vmdef.From_xml failed: %s", err.Error())
		http.Error(w, "invalid VM data", http.StatusInternalServerError)
		return
	}
	err = hypervisor.Delete_domain(uuid)
	if (err != nil) {
		logger.Log("Delete_domain failed: %s", err.Error())
		http.Error(w, "Failed to delete VM", http.StatusFailedDependency)
		return
	}
	if (o.Deletestorage) {
		err = storage.Delete(&vm, nil)
		if (err != nil) {
			w.Header().Set("Warning", `299 VirtX "storage could not be deleted"`)
		}
	}
	/* we keep the xml around. It could be useful for the future and should not waste a lot of space */
	if (err != nil) {
		httpx.Do_response(w, http.StatusOK, nil)
	} else {
		httpx.Do_response(w, http.StatusNoContent, nil)
	}
}
