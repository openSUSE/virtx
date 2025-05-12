package virtx

import (
	"net/http"
	"encoding/json"
	"strings"
	"bytes"
	"io"

	"suse.com/virtx/pkg/hypervisor"
	//	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
)

func vm_list(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		o openapi.VmListOptions
		vm hypervisor.VmStat
		vm_list openapi.VmList
		buf bytes.Buffer
	)
	err = json.NewDecoder(r.Body).Decode(&o)
	if (err != nil && err != io.EOF) {
		http.Error(w, "vm_list: Failed to decode JSON in Request Body", http.StatusBadRequest)
		return
	}

vmloop:
	for _, vm = range service.vmstats {
		if (o.Filter.Name != "" && !strings.Contains(vm.Name, o.Filter.Name)) {
			continue
		}
		if (o.Filter.Host != "" && (vm.Runinfo.Host != o.Filter.Host)) {
			continue
		}
		if (o.Filter.Runstate > 0 && (vm.Runinfo.Runstate != o.Filter.Runstate)) {
			continue
		}
		if (o.Filter.Vlanid > 0 && (vm.Vlanid != o.Filter.Vlanid)) {
			continue
		}
		if (o.Filter.Custom.Name != "") {
			var found bool
			for _, custom := range vm.Custom {
				if (custom.Name == o.Filter.Custom.Name) {
					if (custom.Value != o.Filter.Custom.Value) {
						continue vmloop
					} else {
						found = true
						break
					}
				}
			}
			if (!found) {
				continue
			}
		}
		var item openapi.VmListItem = openapi.VmListItem{
			Uuid: vm.Uuid,
			Fields: openapi.VmListFields{
				Name: vm.Name,
				Host: vm.Runinfo.Host,
				Runstate: vm.Runinfo.Runstate,
				Vlanid: vm.Vlanid,
			},
		}
		vm_list.Items = append(vm_list.Items, item)
	}
	err = json.NewEncoder(&buf).Encode(&vm_list)
	if (err != nil) {
		http.Error(w, "vm_list: Failed to encode JSON", http.StatusInternalServerError)
        return
    }

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
