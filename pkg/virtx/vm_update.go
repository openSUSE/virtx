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


func vm_update(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		host string
		o openapi.VmUpdateOptions
		old openapi.Vmdef
		xml, uuid string
		vmdata inventory.Vmdata
		vr httpx.Request
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
	if (vmdata.Runinfo.Runstate != openapi.RUNSTATE_POWEROFF) {
		http.Error(w, "VM is not powered off", http.StatusUnprocessableEntity)
		return
	}
	host = vmdata.Runinfo.Host
	if (http_host_is_remote(host)) { /* need to proxy */
		http_proxy_request(host, w, vr)
		return
	}
	err = vmdef.Validate(&o.Vmdef)
	if (err != nil) {
		logger.Log("vmdef_validate failed: %s", err.Error())
		http.Error(w, "invalid parameters", http.StatusBadRequest)
		return
	}
	/* read the configuration of the VM from the registry on disk */
	xml, err = vmreg.Load(host, uuid)
	if (err != nil) {
		logger.Log("vmreg.Load(%s, %s) failed: %s", host, uuid, err.Error())
		http.Error(w, "could not Load VM", http.StatusInternalServerError)
		return
	}
	err = vmdef.From_xml(&old, xml)
	if (err != nil) {
		logger.Log("vmdef_from_xml failed: %s", err.Error())
		http.Error(w, "invalid VM data", http.StatusInternalServerError)
		return
	}
	xml, err = vmdef.To_xml(&o.Vmdef, uuid)
	if (err != nil) {
		logger.Log("vmdef_to_xml failed: %s", err.Error())
		http.Error(w, "invalid parameters", http.StatusBadRequest)
		return
	}
	/* create missing storage where needed */
	err = vm_storage_update(&o.Vmdef, &old)
	if (err != nil) {
		logger.Log("vm_update_storage failed: %s", err.Error())
		http.Error(w, "storage update failed", http.StatusInsufficientStorage)
		return
	}
	err = vmdef.Write_osdisk_json(&o.Vmdef)
	if (err != nil) {
		logger.Log("warning: Write_osdisk_json failed: %s", err.Error())
	}
	/* redefine the updated domain */
	err = hypervisor.Define_domain(xml, uuid)
	if (err != nil) {
		logger.Log("hypervisor.Define_domain failed: %s", err.Error())
		http.Error(w, "could not define VM", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
