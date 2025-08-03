package virtx

import (
	"net/http"
	"encoding/json"
	"bytes"
	"os"
	"fmt"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/vmreg"
	"suse.com/virtx/pkg/vmdef"
)


func vm_update(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		o openapi.VmUpdateOptions
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
	if (o.Host == "") {
		o.Host = vmdata.Runinfo.Host
	}
	if (host_is_remote(o.Host)) { /* need to proxy */
		proxy_request(o.Host, w, r)
		return
	}
	/* Validate vmdef first */
	if (o.Vmdef.Name == vmdata.Name && o.Host == vmdata.Runinfo.Host) {
		/* avoid name collision on the same host in the new VM definition */
		o.Vmdef.Name = vmdata.Name + "_"
	}
	err = vmdef.Validate(&o.Vmdef)
	if (err != nil) {
		logger.Log("vmdef_validate failed: %s", err.Error())
		http.Error(w, "invalid parameters", http.StatusBadRequest)
		return
	}
	/* read the configuration of the VM from the registry on disk */
	xml, err = vmreg.Load(vmdata.Runinfo.Host, uuid)
	if (err != nil) {
		logger.Log("vmreg.Load(%s, %s) failed: %s", vmdata.Runinfo.Host, uuid, err.Error())
		http.Error(w, "could not Load VM", http.StatusInternalServerError)
		return
	}
	err = vmdef.From_xml(&old, xml)
	if (err != nil) {
		logger.Log("vmdef_from_xml failed: %s", err.Error())
		http.Error(w, "invalid VM data", http.StatusInternalServerError)
		return
	}
	xml, err = vmdef.To_xml(&o.Vmdef)
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
	/* define new domain */
	uuidnew, err = hypervisor.Define_domain(xml)
	if (err != nil) {
		logger.Log("hypervisor.Define_domain failed: %s", err.Error())
		http.Error(w, "could not define VM", http.StatusInternalServerError)
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
	if (!host_is_remote(vmdata.Runinfo.Host)) {
		/* host is local */
		err = hypervisor.Delete_domain(uuid)
		if (err != nil) {
			logger.Log("could not undefine previous domain: %s", uuid)
			http.Error(w, "could not undefine VM", http.StatusInternalServerError)
			return
		}
	} else {
		/* host is remote, we have to request the old host to delete the old VM */
		var (
			delete_opts openapi.VmDeleteOptions
			buf bytes.Buffer
			response *http.Response
		)
		delete_opts.Deletestorage = false
		err = json.NewEncoder(&buf).Encode(&delete_opts)
		if (err != nil) {
			http.Error(w, "failed to encode JSON", http.StatusInternalServerError)
			return
		}
		response, err = do_request(vmdata.Runinfo.Host, "DELETE", fmt.Sprintf("/vms/%s", uuid), &delete_opts)
		if (err != nil) {
			logger.Log("do_request failed: %s", err.Error())
			http.Error(w, "failed to request deletion", http.StatusInternalServerError)
			return
		}
		defer response.Body.Close()
		if (response.StatusCode < 200 || response.StatusCode > 299) {
			logger.Log("do_request failed, status %s", response.Status)
			http.Error(w, "failed to request deletion", http.StatusInternalServerError)
			return
		}
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
