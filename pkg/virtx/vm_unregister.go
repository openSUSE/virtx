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
	"os"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/inventory"
)

func vm_unregister(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		o openapi.VmRegisterOptions
		uuid string
		vminfo inventory.VmInfo
		vminfo_err error
		vr httpx.Request
		in_libvirt bool
		in_reg bool
	)
	vr, err = httpx.Decode_request_body(r, &o)
	if (err != nil) {
		logger.Log("%s", err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	uuid = r.PathValue("uuid")
	if (uuid == "") {
		http.Error(w, "could not get uuid", http.StatusBadRequest)
		return
	}
	if (http_host_is_remote(o.Host)) {
		http_proxy_request(o.Host, w, vr)
		return
	}
	vminfo, vminfo_err = inventory.Get_vminfo(uuid)
	in_libvirt = (vminfo_err == nil)

	err = reg.Access(o.Host, uuid)
	in_reg = (err == nil)
	if (err != nil && !os.IsNotExist(err)) {
		logger.Log("vm_unregister: reg.Access failed: %s", err.Error())
		http.Error(w, "failed to check registration", http.StatusInternalServerError)
		return
	}

	switch {
	case in_libvirt && in_reg:
		/* both consistent: nothing to unregister */
		httpx.Do_response(w, http.StatusNoContent, nil)
	case !in_libvirt && !in_reg:
		http.Error(w, "unknown uuid", http.StatusNotFound)
	case in_libvirt && !in_reg:
		/* orphan in libvirt: remove it */
		if (vminfo.Host != o.Host || vminfo.Host != machine.Uuid()) {
			http.Error(w, "invalid host for this VM", http.StatusUnprocessableEntity)
			return
		}
		if (vminfo.Runstate != openapi.RUNSTATE_POWEROFF && vminfo.Runstate != openapi.RUNSTATE_CRASHED) {
			http.Error(w, "VM is not powered off", http.StatusUnprocessableEntity)
			return
		}
		err = hypervisor.Undefine_domain(uuid)
		if (err != nil) {
			logger.Log("vm_unregister: Undefine_domain failed: %s", err.Error())
			http.Error(w, "failed to unregister VM from libvirt", http.StatusFailedDependency)
			return
		}
		httpx.Do_response(w, http.StatusOK, nil)
	case !in_libvirt && in_reg:
		/* orphan in registry: remove it */
		err = reg.Delete(o.Host, uuid)
		if (err != nil) {
			logger.Log("vm_unregister: reg.Delete failed: %s", err.Error())
			http.Error(w, "failed to unregister VM from registry", http.StatusInternalServerError)
			return
		}
		httpx.Do_response(w, 209, nil)
	}
}
