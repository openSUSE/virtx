package virtx

import (
	"net/http"
	"encoding/json"
	"io"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
)

func vm_delete(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		o openapi.VmDeleteOptions
		uuid, xml string
		vmdef openapi.Vmdef
	)
	err = json.NewDecoder(r.Body).Decode(&o)
	if (err != nil && err != io.EOF) {
		http.Error(w, "Failed to decode JSON in Request Body", http.StatusBadRequest)
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
	if (host_is_remote(vmdata.Runinfo.Host)) {
		proxy_request(vmdata.Runinfo.Host, w, r)
		return
	}
	xml, err = hypervisor.Dumpxml(uuid)
	if (err != nil) {
		logger.Log("hypervisor.Dumpxml failed: %s", err.Error())
		http.Error(w, "could not Get VM XML", http.StatusInternalServerError)
		return
	}
	err = vmdef_from_xml(&vmdef, xml)
	if (err != nil) {
		logger.Log("vmdef_from_xml failed: %s", err.Error())
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
		err = vm_delete_storage(&vmdef)
		if (err != nil) {
			http.Error(w, "Failed to delete virtual disk storage", http.StatusInternalServerError)
			return
		}
	}
	/* we keep the xml around. It could be useful for the future and should not waste a lot of space */
	w.WriteHeader(http.StatusNoContent)
}
