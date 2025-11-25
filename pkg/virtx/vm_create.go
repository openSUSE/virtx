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


func vm_create(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		o openapi.VmCreateOptions
		xml, uuid string
		vr httpx.Request
	)
	vr, err = httpx.Decode_request_body(r, &o)
	if (err != nil) {
		logger.Log(err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	if (http_host_is_remote(o.Host)) { /* need to proxy */
		http_proxy_request(o.Host, w, vr)
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
	err = vmdef.Write_osdisk_json(&o.Vmdef)
	if (err != nil) {
		logger.Log("warning: Write_osdisk_json failed: %s", err.Error())
	}
	err = hypervisor.Define_domain(xml, uuid)
	if (err != nil) {
		logger.Log("hypervisor.Define_domain failed: %s", err.Error())
		http.Error(w, "could not define VM", http.StatusFailedDependency)
		return
	}
	var buf bytes.Buffer
	err = json.NewEncoder(&buf).Encode(&uuid)
	if (err != nil) {
		logger.Log("failed to encode JSON")
	}
	httpx.Do_response(w, http.StatusCreated, &buf)
}
