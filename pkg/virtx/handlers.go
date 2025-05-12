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

func VmCreate(w http.ResponseWriter, r *http.Request) {
}

func VmList(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "VmList: Failed to decode JSON in Request Body", http.StatusBadRequest)
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
		http.Error(w, "VmList: Failed to encode JSON", http.StatusInternalServerError)
        return
    }

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}

func VmUpdate(w http.ResponseWriter, r *http.Request) {
}
func VmGet(w http.ResponseWriter, r *http.Request) {
}
func VmDelete(w http.ResponseWriter, r *http.Request) {
}
func VmGetRunstate(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		ok bool
		uuid string
		vmstat hypervisor.VmStat
		runinfo openapi.Vmruninfo
		buf bytes.Buffer
	)
	uuid = r.PathValue("uuid")
	if (uuid == "") {
		http.Error(w, "VmGetRunstate: Failed to decode parameters", http.StatusBadRequest)
		return
	}
	vmstat, ok = service.vmstats[uuid]
	if (!ok) {
		http.Error(w, "VmGetRunstate: No such VM", http.StatusNotFound)
		return
	}
	runinfo = vmstat.Runinfo
	err = json.NewEncoder(&buf).Encode(&runinfo)
	if (err != nil) {
		http.Error(w, "VmGetRunstate: Failed to encode JSON", http.StatusInternalServerError)
        return
    }
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
func VmStart(w http.ResponseWriter, r *http.Request) {
}
func VmShutdown(w http.ResponseWriter, r *http.Request) {
}
func VmPause(w http.ResponseWriter, r *http.Request) {
}
func VmUnpause(w http.ResponseWriter, r *http.Request) {
}
func VmMigrate(w http.ResponseWriter, r *http.Request) {
}
func VmGetMigrateInfo(w http.ResponseWriter, r *http.Request) {
}
func VmMigrateCancel(w http.ResponseWriter, r *http.Request) {
}
func HostList(w http.ResponseWriter, r *http.Request) {
}
func HostGet(w http.ResponseWriter, r *http.Request) {
}
func ClusterGet(w http.ResponseWriter, r *http.Request) {
}
