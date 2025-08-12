package virtx

import (
	"net/http"
	"encoding/json"
	"bytes"
	"fmt"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/vmreg"
	"suse.com/virtx/pkg/vmdef"
	"suse.com/virtx/pkg/httpx"
)


func vm_migrate(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		o openapi.VmMigrateOptions
		uuid, host_old string
		vr httpx.Request
	)
	vr, err = httpx.Decode_request_body(r, &o)
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
	if (vmdata.Runinfo.Runstate != openapi.RUNSTATE_POWEROFF) {
		http.Error(w, "VM is not powered off", http.StatusUnprocessableEntity)
		return
	}
	if (o.Host == "") {
		/* Auto migration is not implemented yet */
		http.Error(w, "Not implemented", http.StatusNotImplemented)
		return
	}
	host_old = vmdata.Runinfo.Host
	if (o.Host == host_old) {
		http.Error(w, "Cannot migrate to the same host", http.StatusBadRequest)
		return
	}
	if (o.Live == false) {
		vm_migrate_offline(o.Host, host_old, uuid, w, vr);
	} else {
		vm_migrate_live(o.Host, host_old, uuid, w, vr);
	}
}

func vm_migrate_offline(host_new string, host_old string, uuid_old string, w http.ResponseWriter, vr httpx.Request) {
	var (
		err error
		uuid_new, xml string
		def openapi.Vmdef
	)
	if (http_host_is_remote(host_new)) { /* need to proxy */
		http_proxy_request(host_new, w, vr);
	}
	/* read the configuration of the VM from the registry on disk */
	xml, err = vmreg.Load(host_old, uuid_old)
	if (err != nil) {
		logger.Log("vmreg.Load(%s, %s) failed: %s", host_old, uuid_old, err.Error())
		http.Error(w, "could not Load VM", http.StatusInternalServerError)
		return
	}
	err = vmdef.From_xml(&def, xml)
	if (err != nil) {
		logger.Log("vmdef_from_xml failed: %s", err.Error())
		http.Error(w, "invalid VM data", http.StatusInternalServerError)
		return
	}
	uuid_new = New_uuid()
	if (uuid_new == "") {
		http.Error(w, "failed", http.StatusInternalServerError)
		return
	}
	/* just change the uuid */
	xml, err = vmdef.To_xml(&def, uuid_new)
	if (err != nil) {
		logger.Log("vmdef_to_xml failed: %s", err.Error())
		http.Error(w, "invalid parameters", http.StatusBadRequest)
		return
	}
	/* define new domain */
	err = hypervisor.Define_domain(xml, uuid_new)
	if (err != nil) {
		logger.Log("hypervisor.Define_domain failed: %s", err.Error())
		http.Error(w, "could not define VM", http.StatusInternalServerError)
		return
	}
	/*
	 * new domain is created, now we have to request the old host to delete
	 * the old domain. But if we fail to do so, two domains might exist at the
	 * same time. It would be tempting to then delete the new domain,
	 * but that could fail too, or we might be getting an HTTP error
	 * unrelated to the application, when the domain has been deleted,
	 * leading to the VM being lost.
	 *
	 * This is roughly the same for live migration, a problem that libvirt
	 * itself has to deal with, and it also chooses to err on the safe
	 * side, and have two domain definitions (one inactive, one active)
	 * rather than risking losing the VM.
	 *
	 * Warn the user with appropriate headers.
	 */
	var (
		delete_opts openapi.VmDeleteOptions
		buf bytes.Buffer
		response *http.Response
	)
	delete_opts.Deletestorage = false
	err = json.NewEncoder(&buf).Encode(&delete_opts)
	if (err != nil) {
		logger.Log("failed to encode JSON for DELETE")
		w.Header().Set("Warning", `299 VirtX "old domain could not be deleted"`)
		goto done
	}
	response, err = http_do_request(host_old, "DELETE",	fmt.Sprintf("/vms/%s", uuid_old), &delete_opts)
	if (err != nil) {
		logger.Log("do_request failed: %s", err.Error())
		w.Header().Set("Warning", `299 VirtX "old domain could not be deleted"`)
		goto done
	}
	defer response.Body.Close()
	if (response.StatusCode < 200 || response.StatusCode > 299) {
		logger.Log("do_request failed, status %s", response.Status)
		w.Header().Set("Warning", `299 VirtX "old domain could not be deleted"`)
	}
done:
	buf.Reset()
	err = json.NewEncoder(&buf).Encode(&uuid_new)
	if (err != nil) {
		logger.Log("failed to encode JSON")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(buf.Bytes())
}

func vm_migrate_live(host_new string, host_old string, uuid string, w http.ResponseWriter, vr httpx.Request) {
	http.Error(w, "Not implemented", http.StatusNotImplemented)
}
