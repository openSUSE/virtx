package virtx

import (
	"net/http"
	"encoding/json"
	"bytes"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/vmdef"
	"suse.com/virtx/pkg/httpx"
)

func vm_get(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		uuid, xml string
		vm openapi.Vm
		buf bytes.Buffer
		vr httpx.Request
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
	vmdata, ok := service.vmdata[uuid]
	if (!ok) {
		http.Error(w, "unknown uuid", http.StatusNotFound)
		return
	}
	if (http_host_is_remote(vmdata.Runinfo.Host)) {
		http_proxy_request(vmdata.Runinfo.Host, w, vr)
		return
	}
	xml, err = hypervisor.Dumpxml(uuid)
	if (err != nil) {
		logger.Log("hypervisor.Dumpxml failed: %s", err.Error())
		http.Error(w, "could not get VM", http.StatusInternalServerError)
		return
	}
	err = vmdef.From_xml(&vm.Def, xml)
	if (err != nil) {
		logger.Log("vmdef.From_xml failed: %s", err.Error())
		http.Error(w, "invalid VM data", http.StatusInternalServerError)
		return
	}
	vm.Uuid = uuid
	vm.Runinfo = vmdata.Runinfo
	vm.Stats = vmdata.Stats
	vm.Ts = vmdata.Ts

	err = json.NewEncoder(&buf).Encode(&vm)
	if (err != nil) {
		logger.Log("failed to encode JSON")
		http.Error(w, "Failed to encode JSON", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
