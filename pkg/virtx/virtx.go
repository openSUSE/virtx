/*
 * Copyright (c) 2024-2025 SUSE LLC
 *
 * This program is free software; you can redistribute it and/or
 * modify it under the terms of the GNU General Public License
 * as published by the Free Software Foundation; either version 2
 * of the License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, see
 * <https://www.gnu.org/licenses/>
 */
package virtx

import (
	"fmt"
	"net/http"
	"sync"
	"errors"
	"math"
	"context"

	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/model"
	. "suse.com/virtx/pkg/constants"
)

const (
	VMS_DIR = "/vms/"
	VM_NAME_MAX = 32
	NET_NAME_MAX = 32
	CPU_NAME_MAX = 32
	GENID_LEN = 36
	DISKS_MAX = 20
	NETS_MAX = 8
	MAC_LEN = 17
	VLAN_MAX = 4094
)

type VmStats map[string]hypervisor.VmStat
type Hosts map[string]openapi.Host

type Service struct {
	servemux *http.ServeMux
	server http.Server
	m      sync.RWMutex

	cluster openapi.Cluster
	hosts   Hosts
	vmstats VmStats
}

var service Service

func Init() {
	var servemux *http.ServeMux = http.NewServeMux()
	servemux.HandleFunc("POST /vms", vm_create)
	servemux.HandleFunc("GET /vms", vm_list)
	servemux.HandleFunc("PUT /vms/{uuid}", vm_update)
	servemux.HandleFunc("GET /vms/{uuid}", vm_get)
	servemux.HandleFunc("DELETE /vms/{uuid}", vm_delete)
	servemux.HandleFunc("GET /vms/{uuid}/runstate", vm_runstate_get)
	servemux.HandleFunc("POST /vms/{uuid}/runstate/start", vm_start)
	servemux.HandleFunc("DELETE /vms/{uuid}/runstate/start", vm_shutdown)
	servemux.HandleFunc("POST /vms/{uuid}/runstate/pause", vm_pause)
	servemux.HandleFunc("DELETE /vms/{uuid}/runstate/pause", vm_unpause)
	servemux.HandleFunc("POST /vms/{uuid}/runstate/migrate", vm_migrate)
	servemux.HandleFunc("GET /vms/{uuid}/runstate/migrate", vm_migrate_get)
	servemux.HandleFunc("DELETE /vms/{uuid}/runstate/migrate", vm_migrate_cancel)

	servemux.HandleFunc("GET /hosts", host_list)
	servemux.HandleFunc("GET /hosts/{uuid}", host_get) // XXX not in API yet XXX
	servemux.HandleFunc("GET /cluster", cluster_get)

	service = Service{
		servemux: servemux,
		server: http.Server{
			Addr: ":8080",
			Handler: servemux,
		},
		m:         sync.RWMutex{},
		cluster:   openapi.Cluster{},
		hosts:     make(Hosts),
		vmstats:   make(VmStats),
	}
}

func Shutdown(ctx context.Context) error {
	return service.server.Shutdown(ctx)
}

func Close() error {
	return service.server.Close()
}

func Update_host(host *openapi.Host) error {
	service.m.Lock()
	defer service.m.Unlock()

	return update_host(host)
}

func update_host(host *openapi.Host) error {
	var (
		present bool
		old openapi.Host
	)
	if (service.hosts == nil) {
		service.hosts = make(map[string]openapi.Host)
	}
	old, present = service.hosts[host.Uuid]
	if (present && old.Ts > host.Ts) {
		logger.Log("Host %s: ignoring obsolete Host information: ts %d > %d",
			old.Def.Name, old.Ts, host.Ts)
		return nil
	}
	service.hosts[host.Uuid] = *host
	return nil
}

func Set_host_state(uuid string, newstate openapi.Hoststate) error {
	service.m.Lock()
	defer service.m.Unlock()

	return set_host_state(uuid, newstate)
}

func set_host_state(uuid string, newstate openapi.Hoststate) error {
	host, ok := service.hosts[uuid]
	if !ok {
		return fmt.Errorf("no such host %s", uuid)
	}
	host.State = newstate
	service.hosts[uuid] = host
	return nil
}

func Update_vm_state(e *hypervisor.VmEvent) error {
	service.m.Lock()
	defer service.m.Unlock()
	vmstat, ok := service.vmstats[e.Uuid]
	if !ok {
		return fmt.Errorf("no such VM %s", e.Uuid)
	}
	vmstat.Runinfo.Runstate = openapi.Vmrunstate(e.State)
	service.vmstats[e.Uuid] = vmstat
	return nil
}

func Update_vm(vmstat *hypervisor.VmStat) error {
	service.m.Lock()
	defer service.m.Unlock()

	return update_vm(vmstat)
}

func update_vm(vmstat *hypervisor.VmStat) error {
	if (service.vmstats == nil) {
		service.vmstats = make(map[string]hypervisor.VmStat)
	}
	if old, ok := service.vmstats[vmstat.Uuid]; ok {
		if (old.Ts > vmstat.Ts) {
			logger.Log("Ignoring old guest info: ts %d > %d %s %s",
				old.Ts, vmstat.Ts, vmstat.Uuid, vmstat.Name,
			)
			return nil
		}
		/* calculate deltas from previous Vm info */
		if (int(vmstat.Runinfo.Runstate) > 1) {
			var delta uint64
			if (vmstat.Cpu_time >= old.Cpu_time) {
				delta = vmstat.Cpu_time - old.Cpu_time
			} else {
				delta = (math.MaxUint64 - old.Cpu_time) + vmstat.Cpu_time + 1
			}
			if (delta > 0 && (vmstat.Ts - old.Ts) > 0 && vmstat.Cpus > 0) {
				vmstat.Cpu_utilization = int16((delta * 100) / (uint64(vmstat.Ts - old.Ts) * uint64(vmstat.Cpus) * 1000000))
			}
		}
		{
			var delta int64
			if (vmstat.Net_rx >= old.Net_rx) {
				delta = vmstat.Net_rx - old.Net_rx
			} else {
				delta = (math.MaxInt64 - old.Net_rx) + (vmstat.Net_rx - math.MinInt64) + 1
			}
			if (delta > 0 && (vmstat.Ts - old.Ts) > 0) {
				vmstat.Net_rx_bw = int32((delta * 1000) / ((vmstat.Ts - old.Ts) * KiB))
			}
			if (vmstat.Net_tx >= old.Net_tx) {
				delta = vmstat.Net_tx - old.Net_tx
			} else {
				delta = (math.MaxInt64 - old.Net_tx) + (vmstat.Net_tx - math.MinInt64) + 1
			}
			if (delta > 0 && (vmstat.Ts - old.Ts) > 0) {
				vmstat.Net_tx_bw = int32((delta * 1000) / ((vmstat.Ts - old.Ts) * KiB))
			}
		}
	}
	service.vmstats[vmstat.Uuid] = *vmstat
	return nil
}

func libvirt_uri_from_host(uuid string) (string, error) {
	var (
		host openapi.Host
		ok bool
	)
	if (uuid == "") {
		host, ok = service.hosts[hypervisor.Uuid]
	} else {
		host, ok = service.hosts[uuid]
	}
	if (!ok) {
		return "", errors.New("could not find host")
	}
	if (host.State != openapi.HOST_ACTIVE) {
		return "", errors.New("host is not active")
	}
	if (uuid == "") {
		return "qemu:///system", nil
	} else {
		return "qemu+ssh://" + host.Def.Name + "/system", nil
	}
}

func Start_listening() {
	go func() {
		var err error = service.server.ListenAndServe()
		if (err != nil && errors.Is(err, http.ErrServerClosed)) {
			logger.Log(err.Error())
		} else {
			logger.Log(err.Error())
		}
	}()
}
