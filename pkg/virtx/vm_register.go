package virtx

import (
	"net/http"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/vmreg"
	"suse.com/virtx/pkg/vmdef"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/inventory"
)

func vm_register(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		o openapi.VmRegisterOptions
		uuid string
		vmdata inventory.Vmdata
		vr httpx.Request
		status int
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
	if (http_host_is_remote(o.Host)) { /* need to proxy */
		http_proxy_request(o.Host, w, vr)
		return
	}
	vmdata, err = inventory.Get_vm(uuid)
	if (err == nil) {
		/*
		 * the uuid is known to inventory
		 *
		 * in this case the domain must exist in this libvirt.
		 * Check if it exists in vmreg, and if not register it from libvirt
		 */
		if (vmdata.Runinfo.Host != o.Host || vmdata.Runinfo.Host != hypervisor.Uuid()) {
			http.Error(w, "invalid host for this VM", http.StatusUnprocessableEntity)
			return
		}
		err = vm_register_vmreg(o.Host, uuid)
		if (err == nil) {
			status = http.StatusOK
		}
	} else {
		/* the uuid is unknown to inventory
		 *
		 * Check if it exists in libvirt, and if not register it from vmreg.
		 */
		err = vm_register_libvirt(o.Host, uuid)
		if (err == nil) {
			status = http.StatusCreated
		}
	}
	if (err != nil) {
		logger.Log("failed to register %s/%s: %s", o.Host, uuid, err.Error())
		http.Error(w, "failed to register uuid", http.StatusInternalServerError)
		return
	}
	httpx.Do_response(w, status, nil)
}

/* register from libvirt into vmreg */
func vm_register_vmreg(host_uuid string, uuid string) error {
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
	err = vmdef.Write_osdisk_json(&vm)
	if (err != nil) {
		return err
	}
	/* store the processed XML in /vms/xml/host-uuid/vm-uuid.xml */
	err = vmreg.Save(host_uuid, uuid, xml)
	if (err != nil) {
		return err
	}
	return nil
}

/* register from vmreg into libvirt */
func vm_register_libvirt(host_uuid string, uuid string) error {
	var (
		err error
		vm openapi.Vmdef
		xml string
	)
	xml, err = vmreg.Load(host_uuid, uuid)
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
	err = vmdef.Write_osdisk_json(&vm)
	if (err != nil) {
		return err
	}
	err = hypervisor.Define_domain(xml, uuid)
	if (err != nil) {
		return err
	}
	return nil
}
