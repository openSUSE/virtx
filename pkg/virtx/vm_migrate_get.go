package virtx

import (
	"net/http"
	"encoding/json"
	"bytes"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/inventory"
)


func vm_migrate_get(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		uuid, host_old string
		vmdata inventory.Vmdata
		vr httpx.Request
		info openapi.MigrationInfo
	)
	vr, err = httpx.Decode_request_body(r, nil)
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
	host_old = vmdata.Runinfo.Host
	if (http_host_is_remote(host_old)) { /* need to proxy */
		http_proxy_request(host_old, w, vr);
		return
	}
	info, err = hypervisor.Get_migration_info(uuid)
	if (err != nil) {
		logger.Log("Get_migration_status failed: %s", err.Error())
		http.Error(w, "could not get migration info", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	err = json.NewEncoder(&buf).Encode(&info)
	if (err != nil) {
		logger.Log("failed to encode JSON")
	}
	httpx.Do_response(w, http.StatusOK, &buf)
}
