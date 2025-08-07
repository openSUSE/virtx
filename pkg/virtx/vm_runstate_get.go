package virtx

import (
	"net/http"
	"encoding/json"
	"bytes"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/model"
)

func vm_runstate_get(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		ok bool
		uuid string
		vmdata hypervisor.Vmdata
		runinfo openapi.Vmruninfo
		buf bytes.Buffer
	)
	_, err = httpx.Decode_request_body(r, nil)
	if (err != nil) {
		logger.Log(err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	uuid = r.PathValue("uuid")
	if (uuid == "") {
		http.Error(w, "vm_runstate_get: Failed to decode parameters", http.StatusBadRequest)
		return
	}
	vmdata, ok = service.vmdata[uuid]
	if (!ok) {
		http.Error(w, "vm_runstate_get: No such VM", http.StatusNotFound)
		return
	}
	runinfo = vmdata.Runinfo
	err = json.NewEncoder(&buf).Encode(&runinfo)
	if (err != nil) {
		http.Error(w, "vm_runstate_get: Failed to encode JSON", http.StatusInternalServerError)
        return
    }
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
