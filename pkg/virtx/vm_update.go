package virtx

import (
	"net/http"
	"encoding/json"
	"bytes"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/vmreg"
	"suse.com/virtx/pkg/vmdef"
	"suse.com/virtx/pkg/httpx"
)


func vm_update(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		host string
		o openapi.VmUpdateOptions
		old openapi.Vmdef
		xml, uuid_old, uuid_new string
		vr httpx.Request
	)
	vr, err = httpx.Decode_request_body(r, &o)
	if (err != nil) {
		logger.Log(err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	uuid_old = r.PathValue("uuid")
	if (uuid_old == "") {
		http.Error(w, "could not get uuid", http.StatusBadRequest)
		return
	}
	vmdata, ok := service.vmdata[uuid_old]
	if (!ok) {
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
	xml, err = vmreg.Load(host, uuid_old)
	if (err != nil) {
		logger.Log("vmreg.Load(%s, %s) failed: %s", host, uuid_old, err.Error())
		http.Error(w, "could not Load VM", http.StatusInternalServerError)
		return
	}
	err = vmdef.From_xml(&old, xml)
	if (err != nil) {
		logger.Log("vmdef_from_xml failed: %s", err.Error())
		http.Error(w, "invalid VM data", http.StatusInternalServerError)
		return
	}
	uuid_new = New_uuid()
	if (uuid_new == "") {
		http.Error(w, "failed", http.StatusInternalServerError)
		return
	}
	xml, err = vmdef.To_xml(&o.Vmdef, uuid_new)
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
	/* define new domain */
	err = hypervisor.Define_domain(xml, uuid_new)
	if (err != nil) {
		logger.Log("hypervisor.Define_domain failed: %s", err.Error())
		http.Error(w, "could not define VM", http.StatusInternalServerError)
		return
	}
	err = hypervisor.Delete_domain(uuid_old)
	if (err != nil) {
		logger.Log("could not undefine previous domain: %s: %s", uuid_old, err.Error())
		w.Header().Set("Warning", `299 VirtX "Old definition could not be deleted"`)
	}
	var buf bytes.Buffer
	err = json.NewEncoder(&buf).Encode(&uuid_new)
	if (err != nil) {
		http.Error(w, "failed to encode JSON", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(buf.Bytes())
}
