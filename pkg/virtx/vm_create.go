package virtx

import (
	"net/http"
	"encoding/json"
	"bytes"
	"os"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/vmdef"
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
	if (http_host_is_remote(o.Host)) { /* need to proxy */
		http_proxy_request(o.Host, w, r)
		return
	}
	/* Validate vmdef first */
	err = vmdef.Validate(&o.Vmdef)
	if (err != nil) {
		logger.Log("vmdef.Validate failed: %s", err.Error())
		http.Error(w, "invalid parameters", http.StatusBadRequest)
		return
	}
	uuid = New_uuid()
	if (uuid == "") {
		http.Error(w, "failed", http.StatusInternalServerError)
		return
	}
	xml, err = vmdef.To_xml(&o.Vmdef, uuid)
	if (err != nil) {
		logger.Log("vmdef.To_xml failed: %s", err.Error())
		http.Error(w, "invalid parameters", http.StatusBadRequest)
		return
	}
	/* create storage if needed */
	err = vm_storage_create(&o.Vmdef)
	if (err != nil) {
		logger.Log("vm_create_storage failed: %s", err.Error())
		http.Error(w, "storage creation failed", http.StatusInsufficientStorage)
		return
	}
	/*
	 * Write the xml to disk near the OS disk for convenience in its original form
	 * (not processed by libvirt), so it can be used as reference for the future,
	 * and help with debugging.
	 * This is in addition to the processed XML which is stored in /vms/xml/
	 */
	err = os.WriteFile(vmdef.Osdisk_xml(&o.Vmdef), []byte(xml), 0640)
	if (err != nil) {
		logger.Log("os.WriteFile failed: %s", err.Error())
		http.Error(w, "could not write XML", http.StatusInternalServerError)
		return
	}
	err = hypervisor.Define_domain(xml, uuid)
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
