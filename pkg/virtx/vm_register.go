/*
 * Copyright (c) 2024-2026 SUSE LLC
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
	"suse.com/virtx/pkg/vmdef"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/inventory"
)

func vm_register(w http.ResponseWriter, r *http.Request) {
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
	if (http_host_is_remote(o.Host)) { /* need to proxy */
		http_proxy_request(o.Host, w, vr)
		return
	}
	vminfo, vminfo_err = inventory.Get_vminfo(uuid)
	in_libvirt = (vminfo_err == nil)

	err = reg.Access(o.Host, uuid)
	in_reg = (err == nil)
	if (err != nil && !os.IsNotExist(err)) {
		logger.Log("vm_register: reg.Access failed: %s", err.Error())
		http.Error(w, "failed to check registration", http.StatusInternalServerError)
		return
	}

	switch {
	case in_libvirt && in_reg:
		/* both consistent: nothing to repair */
		httpx.Do_response(w, http.StatusNoContent, nil)
	case !in_libvirt && !in_reg:
		http.Error(w, "unknown uuid", http.StatusNotFound)
	case in_libvirt && !in_reg:
		/* orphan in libvirt: register from libvirt into reg */
		if (vminfo.Host != o.Host || vminfo.Host != machine.Uuid()) {
			http.Error(w, "invalid host for this VM", http.StatusUnprocessableEntity)
			return
		}
		err = vm_register_reg(o.Host, uuid)
		if (err != nil) {
			logger.Log("vm_register_reg failed: %s", err.Error())
			http.Error(w, "failed to register uuid", http.StatusInternalServerError)
			return
		}
		httpx.Do_response(w, http.StatusOK, nil)
	case !in_libvirt && in_reg:
		/* orphan in reg: register from reg into libvirt */
		var canonical_xml string
		canonical_xml, err = vm_register_libvirt(o.Host, uuid)
		if (err != nil) {
			logger.Log("vm_register_libvirt failed: %s", err.Error())
			http.Error(w, "failed to register uuid", http.StatusFailedDependency)
			return
		}
		/*
		 * libvirt may canonicalize the XML differently from what is stored
		 * in the registry. Save it back so the registry stays consistent with
		 * what libvirt actually has. A failure here is non-fatal: the VM is
		 * registered in libvirt and the registry has the original XML, which
		 * is close enough for recovery purposes.
		 */
		reg_err := reg.Save(o.Host, uuid, canonical_xml)
		if (reg_err != nil) {
			logger.Log("vm_register: reg.Save failed: %s", reg_err.Error())
			w.Header().Set("Warning", `299 VirtX "VM registered but registration update failed"`)
		}
		httpx.Do_response(w, http.StatusCreated, nil)
	}
}

/* register from libvirt into reg */
func vm_register_reg(host_uuid string, uuid string) error {
	var (
		err error
		vm openapi.Vmdef
		xml string
	)
	xml, err = hypervisor.Dumpxml(uuid)
	if (err != nil) {
		return err
	}
	err = vmdef.From_xml(&vm, xml)
	if (err != nil) {
		return err
	}
	err = vmdef.Validate(&vm)
	if (err != nil) {
		return err
	}
	/* store the processed XML in /vms/reg/host-uuid/vm-uuid.xml */
	err = reg.Save(host_uuid, uuid, xml)
	if (err != nil) {
		return err
	}
	return nil
}

/* register from reg into libvirt; returns the canonical XML as post-processed by libvirt */
func vm_register_libvirt(host_uuid string, uuid string) (string, error) {
	var (
		err error
		vm openapi.Vmdef
		xml string
	)
	xml, err = reg.Load(host_uuid, uuid)
	if (err != nil) {
		return "", err
	}
	err = vmdef.From_xml(&vm, xml)
	if (err != nil) {
		return "", err
	}
	err = vmdef.Validate(&vm)
	if (err != nil) {
		return "", err
	}
	xml, err = hypervisor.Define_domain(xml)
	if (err != nil) {
		return "", err
	}
	return xml, nil
}
