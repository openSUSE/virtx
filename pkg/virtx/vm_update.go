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


func vm_update(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		o openapi.VmCreateOptions
		old openapi.Vmdef
		xml, uuid, uuidnew string
	)
	err = json.NewDecoder(r.Body).Decode(&o)
	if (err != nil) {
		http.Error(w, "failed to decode JSON in Request Body", http.StatusBadRequest)
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
	if (vmdata.Runinfo.Runstate != openapi.RUNSTATE_POWEROFF) {
		http.Error(w, "VM is not powered off", http.StatusUnprocessableEntity)
		return
	}
	if (host_is_remote(o.Host)) { /* need to proxy */
		proxy_request(o.Host, w, r)
		return
	}

	/* Validate vmdef first */
	if (o.Vmdef.Name == vmdata.Name) {
		/* avoid name collision in the new VM definition */
		o.Vmdef.Name = vmdata.Name + "_"
	}
	err = vmdef_validate(&o.Vmdef)
	if (err != nil) {
		logger.Log("vmdef_validate failed: %s", err.Error())
		http.Error(w, "invalid parameters", http.StatusBadRequest)
		return
	}
	xml, err = hypervisor.Dumpxml(uuid)
	if (err != nil) {
		logger.Log("hypervisor.Dumpxml failed: %s", err.Error())
		http.Error(w, "could not get VM", http.StatusInternalServerError)
		return
	}
	err = vmdef_from_xml(&old, xml)
	if (err != nil) {
		logger.Log("vmdef_from_xml failed: %s", err.Error())
		http.Error(w, "invalid VM data", http.StatusInternalServerError)
		return
	}
	xml, err = vmdef_to_xml(&o.Vmdef)
	if (err != nil) {
		logger.Log("vmdef_to_xml failed: %s", err.Error())
		http.Error(w, "invalid parameters", http.StatusBadRequest)
		return
	}
	/* create missing storage where needed */
	err = vm_storage_update(&o.Vmdef, &old)
	if (err != nil) {
		logger.Log("vm_update_storage failed: %s", err.Error())
		http.Error(w, "storage update failed", http.StatusInsufficientStorage)
		return
	}
	/* define new domain and delete the previous */
	uuidnew, err = hypervisor.Define_domain(xml)
	if (err != nil) {
		logger.Log("hypervisor.Define_domain failed: %s", err.Error())
		http.Error(w, "could not define VM", http.StatusInternalServerError)
		return
	}
	/* Write the xml to disk, owner RW, group R, others - */
	err = os.WriteFile(vmdef_xml_path(&o.Vmdef), []byte(xml), 0640)
	if (err != nil) {
		logger.Log("os.WriteFile failed: %s", err.Error())
		http.Error(w, "could not write XML", http.StatusInternalServerError)
		return
	}
	err = hypervisor.Delete_domain(uuid)
	if (err != nil) {
		logger.Log("could not undefine previous domain: %s", uuid)
		http.Error(w, "could not undefine VM", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	err = json.NewEncoder(&buf).Encode(&uuidnew)
	if (err != nil) {
		http.Error(w, "failed to encode JSON", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
