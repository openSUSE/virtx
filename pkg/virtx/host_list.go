package virtx

import (
	"net/http"
	"encoding/json"
	"strings"
	"bytes"
	"io"

	//	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
)

func host_list(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		o openapi.HostListOptions
		host openapi.Host
		host_list openapi.HostList
		buf bytes.Buffer
	)
	err = json.NewDecoder(r.Body).Decode(&o)
	if (err != nil && err != io.EOF) {
		http.Error(w, "Failed to decode JSON in Request Body", http.StatusBadRequest)
		return
	}
	/* filters: [name, cpuarch, cpudef, hoststate, memoryavailable] */
	for _, host = range service.hosts {
		if (o.Filter.Name != "" && !strings.Contains(host.Def.Name, o.Filter.Name)) {
			continue
		}
		if (o.Filter.Cpuarch.Arch != "" && (host.Def.Cpuarch.Arch != o.Filter.Cpuarch.Arch)) {
			continue
		}
		if (o.Filter.Cpuarch.Vendor != "" && (host.Def.Cpuarch.Vendor != o.Filter.Cpuarch.Vendor)) {
			continue
		}
		if (o.Filter.Cpudef.Model != "" && (host.Def.Cpudef.Model != o.Filter.Cpudef.Model)) {
			continue
		}
		if (o.Filter.Cpudef.Sockets > 0 && (host.Def.Cpudef.Sockets < o.Filter.Cpudef.Sockets)) {
			continue
		}
		if (o.Filter.Cpudef.Cores > 0 && (host.Def.Cpudef.Cores < o.Filter.Cpudef.Cores)) {
			continue
		}
		if (o.Filter.Cpudef.Threads > 0 && (host.Def.Cpudef.Threads < o.Filter.Cpudef.Threads)) {
			continue
		}
		if (o.Filter.Hoststate != openapi.HOST_INVALID && (host.State != o.Filter.Hoststate)) {
			continue
		}
		if (o.Filter.Memoryavailable > 0 && (host.Resources.Memory.Availablevms < o.Filter.Memoryavailable)) {
			continue
		}
		var item openapi.HostListItem = openapi.HostListItem{
			Uuid: host.Uuid,
			Fields: openapi.HostListFields{
				Name: host.Def.Name,
				Cpuarch: host.Def.Cpuarch,
				Cpudef: host.Def.Cpudef,
				Hoststate: host.State,
				Memoryavailable: host.Resources.Memory.Availablevms,
			},
		}
		host_list.Items = append(host_list.Items, item)
	}
	err = json.NewEncoder(&buf).Encode(&host_list)
	if (err != nil) {
		http.Error(w, "Failed to encode JSON", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
