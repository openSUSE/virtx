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

func New() *Service {
	var servemux *http.ServeMux = http.NewServeMux()
	servemux.HandleFunc("POST /vms", VmCreate)
	servemux.HandleFunc("GET /vms", VmList)
	servemux.HandleFunc("PUT /vms/{uuid}", VmUpdate)
	servemux.HandleFunc("GET /vms/{uuid}", VmGet)
	servemux.HandleFunc("DELETE /vms/{uuid}", VmDelete)
	servemux.HandleFunc("GET /vms/{uuid}/runstate", VmGetRunstate)
	servemux.HandleFunc("POST /vms/{uuid}/runstate/start", VmStart)
	servemux.HandleFunc("DELETE /vms/{uuid}/runstate/start", VmShutdown)
	servemux.HandleFunc("POST /vms/{uuid}/runstate/pause", VmPause)
	servemux.HandleFunc("DELETE /vms/{uuid}/runstate/pause", VmUnpause)
	servemux.HandleFunc("POST /vms/{uuid}/runstate/migrate", VmMigrate)
	servemux.HandleFunc("GET /vms/{uuid}/runstate/migrate", VmGetMigrateInfo)
	servemux.HandleFunc("DELETE /vms/{uuid}/runstate/migrate", VmMigrateCancel)

	servemux.HandleFunc("GET /hosts", HostList)
	servemux.HandleFunc("GET /hosts/{uuid}", HostGet) // XXX not in API yet XXX
	servemux.HandleFunc("GET /cluster", ClusterGet)

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
	return &service
}

func (s *Service) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *Service) Close() error {
	return s.server.Close()
}

/* get a host from the list and return whether present */
func (s *Service) GetHost(uuid string) (openapi.Host, error) {
	s.m.RLock()
	defer s.m.RUnlock()
	var (
		host openapi.Host
		present bool
		err error
	)
	host, present = s.hosts[uuid]
	if (!present) {
		err = fmt.Errorf("Service: GetHost(%s): No such host!\n", uuid)
		return host, err
	}
	return host, nil
}

func (s *Service) UpdateHost(host *openapi.Host) error {
	s.m.Lock()
	defer s.m.Unlock()

	return s.updateHost(host)
}

func (s *Service) updateHost(host *openapi.Host) error {
	var (
		present bool
		old openapi.Host
	)
	if (s.hosts == nil) {
		s.hosts = make(map[string]openapi.Host)
	}
	old, present = s.hosts[host.Uuid]
	if (present && old.Ts > host.Ts) {
		logger.Log("Host %s: ignoring obsolete Host information: ts %d > %d",
			old.Def.Name, old.Ts, host.Ts)
		return nil
	}
	s.hosts[host.Uuid] = *host
	return nil
}

func (s *Service) SetHostState(uuid string, newstate openapi.Hoststate) error {
	s.m.Lock()
	defer s.m.Unlock()

	return s.setHostState(uuid, newstate)
}

func (s *Service) setHostState(uuid string, newstate openapi.Hoststate) error {
	host, ok := s.hosts[uuid]
	if !ok {
		return fmt.Errorf("no such host %s", uuid)
	}
	host.State = newstate
	s.hosts[uuid] = host
	return nil
}

func (s *Service) UpdateVmState(e *hypervisor.VmEvent) error {
	s.m.Lock()
	defer s.m.Unlock()
	vmstat, ok := s.vmstats[e.Uuid]
	if !ok {
		return fmt.Errorf("no such VM %s", e.Uuid)
	}
	vmstat.Runinfo.Runstate = openapi.Vmrunstate(e.State)
	s.vmstats[e.Uuid] = vmstat
	return nil
}

func (s *Service) UpdateVm(vmstat *hypervisor.VmStat) error {
	s.m.Lock()
	defer s.m.Unlock()

	return s.updateVm(vmstat)
}

func (s *Service) updateVm(vmstat *hypervisor.VmStat) error {
	if (s.vmstats == nil) {
		s.vmstats = make(map[string]hypervisor.VmStat)
	}
	if old, ok := s.vmstats[vmstat.Uuid]; ok {
		if (old.Ts > vmstat.Ts) {
			logger.Log("Ignoring old guest info: ts %d > %d %s %s",
				old.Ts, vmstat.Ts, vmstat.Uuid, vmstat.Name,
			)
			return nil
		}
		/* calculate deltas from previous Vm info */
		if (int(vmstat.Runinfo.Runstate) > 1) {
			var delta uint64
			if (vmstat.CpuTime >= old.CpuTime) {
				delta = vmstat.CpuTime - old.CpuTime
			} else {
				delta = (math.MaxUint64 - old.CpuTime) + vmstat.CpuTime + 1
			}
			if (delta > 0 && (vmstat.Ts - old.Ts) > 0 && vmstat.Cpus > 0) {
				vmstat.CpuUtilization = int16((delta * 100) / (uint64(vmstat.Ts - old.Ts) * uint64(vmstat.Cpus) * 1000000))
			}
		}
		{
			var delta int64
			if (vmstat.NetRx >= old.NetRx) {
				delta = vmstat.NetRx - old.NetRx
			} else {
				delta = (math.MaxInt64 - old.NetRx) + (vmstat.NetRx - math.MinInt64) + 1
			}
			if (delta > 0 && (vmstat.Ts - old.Ts) > 0) {
				vmstat.NetRxBW = int32((delta * 1000) / ((vmstat.Ts - old.Ts) * KiB))
			}
			if (vmstat.NetTx >= old.NetTx) {
				delta = vmstat.NetTx - old.NetTx
			} else {
				delta = (math.MaxInt64 - old.NetTx) + (vmstat.NetTx - math.MinInt64) + 1
			}
			if (delta > 0 && (vmstat.Ts - old.Ts) > 0) {
				vmstat.NetTxBW = int32((delta * 1000) / ((vmstat.Ts - old.Ts) * KiB))
			}
		}
	}
	s.vmstats[vmstat.Uuid] = *vmstat
	return nil
}

// ServeHTTP implements net/http.Handler
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.m.RLock()
	defer s.m.RUnlock()

	w.Header().Set("Content-Type", "text/plain")
	var (
		lines string
		hi openapi.Host
		vm hypervisor.VmStat
	)
	for _, hi = range s.hosts {
		var line string = fmt.Sprintf("HOST %+v\n", hi)
		lines += line
		for _, vm = range s.vmstats {
			var line string = fmt.Sprintf("VM %+v\n", vm)
			lines += line
		}
	}
	http.Error(w, lines, http.StatusNotImplemented)
}

func (s *Service) StartListening() {
	go func() {
		var err error = s.server.ListenAndServe()
		if (err != nil && errors.Is(err, http.ErrServerClosed)) {
			logger.Log(err.Error())
		} else {
			logger.Log(err.Error())
		}
	}()
}
