package virtx

import (
	"net/http"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/vmdef"
	"suse.com/virtx/pkg/httpx"
)

func vm_delete(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		o openapi.VmDeleteOptions
		uuid, xml string
		vm openapi.Vmdef
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
	vmdata, ok := service.vmdata[uuid]
	if (!ok) {
		http.Error(w, "unknown uuid", http.StatusNotFound)
		return
	}
	if (vmdata.Runinfo.Runstate != openapi.RUNSTATE_POWEROFF) {
		http.Error(w, "VM is not powered off", http.StatusUnprocessableEntity)
		return
	}
	if (http_host_is_remote(vmdata.Runinfo.Host)) {
		http_proxy_request(vmdata.Runinfo.Host, w, vr)
		return
	}
	xml, err = hypervisor.Dumpxml(uuid)
	if (err != nil) {
		logger.Log("hypervisor.Dumpxml failed: %s", err.Error())
		http.Error(w, "could not Get VM XML", http.StatusInternalServerError)
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
		http.Error(w, "Failed to delete VM", http.StatusInternalServerError)
		return
	}
	if (o.Deletestorage) {
		err = vm_storage_delete(&vm)
		if (err != nil) {
			http.Error(w, "Failed to delete virtual disk storage", http.StatusInternalServerError)
			return
		}
	}
	/* we keep the xml around. It could be useful for the future and should not waste a lot of space */
	w.WriteHeader(http.StatusNoContent)
}
