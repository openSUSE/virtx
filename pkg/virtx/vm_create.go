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


/* we use virt-install for now for simplicity while possible */

func vm_create(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		ok bool
		o openapi.VmCreateOptions
		libvirt_uri string
		host openapi.Host
		xml, uuid string
	)
	err = json.NewDecoder(r.Body).Decode(&o)
	if (err != nil) {
		http.Error(w, "failed to decode JSON in Request Body", http.StatusBadRequest)
		return
	}
	if (o.Host != "") {
		host, ok = service.hosts[o.Host]
		if (!ok) {
			http.Error(w, "could not find host", http.StatusUnprocessableEntity)
			return
		}
		libvirt_uri = "qemu+ssh://" + host.Def.Name + "/system"
	} else {
		host, ok = service.hosts[hypervisor.Uuid]
		if (!ok) {
			logger.Log("could not find my own host in service")
			http.Error(w, "invalid service state", http.StatusInternalServerError)
			return
		}
		libvirt_uri = "qemu:///system"
	}
	if (host.State != openapi.HOST_ACTIVE) {
		http.Error(w, "host is not active", http.StatusUnprocessableEntity)
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
		logger.Log("vmdef_create_storage failed: %s", err.Error())
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
	uuid, err = hypervisor.Define_domain(libvirt_uri, xml)
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
