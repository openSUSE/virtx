package virtx

import (
	"net/http"
	"encoding/json"
	"bytes"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
)

func vm_get(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		uuid, xml string
		vmdef openapi.Vmdef
		buf bytes.Buffer
	)
	uuid = r.PathValue("uuid")
	if (uuid == "") {
		http.Error(w, "could not get uuid", http.StatusBadRequest)
		return
	}
	vmstat, ok := service.vmstats[uuid]
	if (!ok) {
		http.Error(w, "unknown uuid", http.StatusBadRequest)
		return
	}
	if (host_is_remote(vmstat.Runinfo.Host)) {
		proxy_request(vmstat.Runinfo.Host, w, r)
		return
	}
	xml, err = hypervisor.Dumpxml(uuid)
	if (err != nil) {
		logger.Log("hypervisor.Dumpxml failed: %s", err.Error())
		http.Error(w, "could not get VM", http.StatusInternalServerError)
		return
	}
	err = vmdef_from_xml(&vmdef, xml)
	if (err != nil) {
		logger.Log("vmdef_from_xml failed: %s", err.Error())
		http.Error(w, "invalid VM data", http.StatusInternalServerError)
		return
	}
	err = json.NewEncoder(&buf).Encode(&vmdef)
	if (err != nil) {
		http.Error(w, "Failed to encode JSON", http.StatusInternalServerError)
        return
    }

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
