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
		vr httpx.Request
		status int
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
	vminfo, err = inventory.Get_vminfo(uuid)
	if (err == nil) {
		/*
		 * the uuid is known to inventory
		 *
		 * in this case the domain must exist in this libvirt.
		 * Check if it exists in reg, and if not register it from libvirt
		 */
		if (vminfo.Host != o.Host || vminfo.Host != machine.Uuid()) {
			http.Error(w, "invalid host for this VM", http.StatusUnprocessableEntity)
			return
		}
		err = vm_register_reg(o.Host, uuid)
		if (err == nil) {
			status = http.StatusOK
		}
	} else {
		/* the uuid is unknown to inventory
		 *
		 * Check if it exists in libvirt, and if not register it from reg.
		 */
		err = vm_register_libvirt(o.Host, uuid)
		if (err == nil) {
			status = http.StatusCreated
		}
	}
	if (err != nil) {
		logger.Log("failed to register %s/%s: %s", o.Host, uuid, err.Error())
		http.Error(w, "failed to register uuid", http.StatusFailedDependency)
		return
	}
	httpx.Do_response(w, status, nil)
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

/* register from reg into libvirt */
func vm_register_libvirt(host_uuid string, uuid string) error {
	var (
		err error
		vm openapi.Vmdef
		xml string
	)
	xml, err = reg.Load(host_uuid, uuid)
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
	err = hypervisor.Define_domain(xml, uuid)
	if (err != nil) {
		return err
	}
	return nil
}
