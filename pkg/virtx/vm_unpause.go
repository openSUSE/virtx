package virtx

import (
	"net/http"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
)

func vm_unpause(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		uuid string
		vr VirtxRequest
	)
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
	if (http_host_is_remote(vmdata.Runinfo.Host)) {
		http_proxy_request(vmdata.Runinfo.Host, w, vr)
		return
	}
	err = hypervisor.Unpause_domain(uuid)
	if (err != nil) {
		logger.Log("hypervisor.Unpause_domain failed: %s", err.Error())
		http.Error(w, "could not unpause VM", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
