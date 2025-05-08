package virtx

import (
	"net/http"
	"encoding/json"
	"strings"

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
		decoder *json.Decoder
		err error
		o openapi.VmListOptions
		vm hypervisor.VmStat
	)
	decoder = json.NewDecoder(r.Body)
	err = decoder.Decode(&o)
	if (err != nil) {
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
	}
}

func VmUpdate(w http.ResponseWriter, r *http.Request) {
}
func VmGet(w http.ResponseWriter, r *http.Request) {
}
func VmDelete(w http.ResponseWriter, r *http.Request) {
}
func VmGetRunstate(w http.ResponseWriter, r *http.Request) {
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
