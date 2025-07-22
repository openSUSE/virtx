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
		uuid, libvirt_uri, xml string
		vmdef openapi.Vmdef
		buf bytes.Buffer
	)
	uuid = r.PathValue("uuid")
	if (uuid == "") {
		http.Error(w, "vm_get: could not get uuid", http.StatusBadRequest)
		return
	}
	vmstat, ok := service.vmstats[uuid]
	if (!ok) {
		http.Error(w, "vm_get: unknown uuid", http.StatusBadRequest)
		return
	}
	libvirt_uri, err = libvirt_uri_from_host(vmstat.Runinfo.Host)
	if (err != nil) {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	xml, err = hypervisor.Dumpxml(libvirt_uri, uuid)
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
		http.Error(w, "vm_get: Failed to encode JSON", http.StatusInternalServerError)
        return
    }

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
