package virtx

import (
	"net/http"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/httpx"
)

func vm_shutdown(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		uuid string
		o openapi.VmShutdownOptions
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
	if (o.Force < 0 || o.Force > 2) {
		http.Error(w, "invalid force field", http.StatusBadRequest)
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
	err = hypervisor.Shutdown_domain(uuid, o.Force)
	if (err != nil) {
		logger.Log("hypervisor.Shutdown_domain failed: %s", err.Error())
		http.Error(w, "could not shutdown VM", http.StatusInternalServerError)
		return
	}
	if (o.Force == 0) {
		/* domain could be shutting down or not, depends on guest ACPI */
		w.WriteHeader(http.StatusAccepted)
	} else {
		/* if we reach here, it means that the Destroy was successful */
		w.WriteHeader(http.StatusNoContent)
	}
}
