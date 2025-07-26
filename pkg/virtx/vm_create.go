package virtx

import (
	"net/http"
	"encoding/json"
	"bytes"
	"os"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
)


func vm_create(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		o openapi.VmCreateOptions
		xml, uuid string
	)
	err = json.NewDecoder(r.Body).Decode(&o)
	if (err != nil) {
		http.Error(w, "failed to decode JSON in Request Body", http.StatusBadRequest)
		return
	}
	if (host_is_remote(o.Host)) { /* need to proxy */
		proxy_request(o.Host, w, r)
		return
	}
	/* Validate vmdef first */
	err = vmdef_validate(&o.Vmdef)
	if (err != nil) {
		logger.Log("vmdef_validate failed: %s", err.Error())
		http.Error(w, "invalid parameters", http.StatusBadRequest)
		return
	}
	/* create storage if needed */
	err = vm_create_storage(&o.Vmdef)
	if (err != nil) {
		logger.Log("vm_create_storage failed: %s", err.Error())
		http.Error(w, "storage creation failed", http.StatusInsufficientStorage)
		return
	}
	xml, err = vmdef_to_xml(&o.Vmdef)
	if (err != nil) {
		logger.Log("vmdef_to_xml failed: %s", err.Error())
		http.Error(w, "invalid parameters", http.StatusBadRequest)
		return
	}
	/* Write the xml to disk, owner RW, group R, others - */
	err = os.WriteFile(vmdef_xml_path(&o.Vmdef), []byte(xml), 0640)
	if (err != nil) {
		logger.Log("os.WriteFile failed: %s", err.Error())
		http.Error(w, "could not write XML", http.StatusInternalServerError)
		return
	}
	uuid, err = hypervisor.Define_domain(xml)
	if (err != nil) {
		logger.Log("hypervisor.Define_domain failed: %s", err.Error())
		http.Error(w, "could not define VM", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	err = json.NewEncoder(&buf).Encode(&uuid)
	if (err != nil) {
		http.Error(w, "failed to encode JSON", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
