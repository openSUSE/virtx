package virtx

import (
	"net/http"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/inventory"
)

func vm_resume(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		uuid string
		vmdata inventory.Vmdata
		vr httpx.Request
	)
	_, err = httpx.Decode_request_body(r, nil)
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
	err = hypervisor.Resume_domain(uuid)
	if (err != nil) {
		logger.Log("hypervisor.Unpause_domain failed: %s", err.Error())
		http.Error(w, "could not unpause VM", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
