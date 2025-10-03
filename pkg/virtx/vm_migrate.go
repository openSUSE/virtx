package virtx

import (
	"net/http"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/inventory"
)


func vm_migrate(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		o openapi.VmMigrateOptions
		uuid, host_old string
		vmdata inventory.Vmdata
		vr httpx.Request
		state openapi.Vmrunstate
		host openapi.Host
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
	state = vmdata.Runinfo.Runstate
	if (o.Live && state != openapi.RUNSTATE_RUNNING && state != openapi.RUNSTATE_PAUSED) {
		http.Error(w, "VM is not running or paused", http.StatusUnprocessableEntity)
		return
	}
	if (!o.Live && state != openapi.RUNSTATE_POWEROFF) {
		http.Error(w, "VM is not powered off", http.StatusUnprocessableEntity)
		return
	}
	if (o.Host == "") {
		/* Auto migration is not implemented yet */
		http.Error(w, "Not implemented", http.StatusNotImplemented)
		return
	}
	host_old = vmdata.Runinfo.Host
	if (o.Host == host_old) {
		http.Error(w, "Cannot migrate to the same host", http.StatusBadRequest)
		return
	}
	if (http_host_is_remote(host_old)) { /* need to proxy */
		http_proxy_request(host_old, w, vr);
		return
	}
	host, err = inventory.Get_host(o.Host)
	if (err != nil) {
		logger.Log("inventory.Get_host(%s) failed: %s", o.Host, err.Error())
		http.Error(w, "failed to get host", http.StatusInternalServerError)
		return
	}
	go func() {
		err = hypervisor.Migrate_domain(host.Def.Name, o.Host, host_old, uuid, o.Live, int(vmdata.Stats.Vcpus))
		if (err != nil) {
			logger.Log("migration of domain %s failed: %s", uuid, err.Error())
		} else {
			logger.Log("migration of domain %s successful", uuid)
		}
	} ()
	httpx.Do_response(w, http.StatusAccepted, nil)
}
