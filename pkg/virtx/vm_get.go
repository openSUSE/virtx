package virtx

import (
	"net/http"
	"encoding/json"
	"bytes"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/vmdef"
)

func vm_get(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		uuid, xml string
		vm openapi.Vm
		buf bytes.Buffer
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
		http.Error(w, "Failed to encode JSON", http.StatusInternalServerError)
        return
    }
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
