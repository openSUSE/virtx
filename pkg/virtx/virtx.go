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

	"suse.com/virtXD/pkg/logger"
	"suse.com/virtXD/pkg/hypervisor"
	"suse.com/virtXD/pkg/model"
)

type Vms map[string]openapi.Vm
type Hosts map[string]openapi.Host

type Service struct {
	http.Server
	m      sync.RWMutex

	cluster openapi.Cluster
	hosts   Hosts
	vms     Vms
}

func New() *Service {
	mux := http.NewServeMux()
	s := &Service{
		Server: http.Server {
			Addr:    ":8080",
			Handler: mux,
		},
		m:         sync.RWMutex{},
		cluster:   openapi.Cluster{},
		hosts:     make(Hosts),
		vms:       make(Vms),
	}
	mux.Handle("/", s)
	return s
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
	vm, ok := s.vms[e.Uuid]
	if !ok {
		return fmt.Errorf("no such VM %s", e.Uuid)
	}
	vm.Runinfo.Runstate = openapi.Vmrunstate(e.State)
	s.vms[e.Uuid] = vm
	return nil
}

func (s *Service) UpdateVm(vm *openapi.Vm) error {
	s.m.Lock()
	defer s.m.Unlock()

	return s.updateVm(vm)
}

func (s *Service) updateVm(vm *openapi.Vm) error {
	if (s.vms == nil) {
		s.vms = make(map[string]openapi.Vm)
	}
	if old, ok := s.vms[vm.Uuid]; ok {
		if old.Ts > vm.Ts {
			logger.Log("Ignoring old guest info: ts %d > %d %s %s",
				old.Ts, vm.Ts, vm.Uuid, vm.Def.Name,
			)
			return nil
		}
	}
	s.vms[vm.Uuid] = *vm
	return nil
}

// ServeHTTP implements net/http.Handler
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.m.RLock()
	defer s.m.RUnlock()

	w.Header().Set("Content-Type", "text/plain")
	var (
		lines, uuid, vm_uuid string
		hi openapi.Host
		vm openapi.Vm
	)
	for uuid, hi = range s.hosts {
		var line string = fmt.Sprintf("host %s(%s)\n", uuid, hi.Def.Name)
		lines += line
		for vm_uuid, vm = range s.vms {
			var line string = fmt.Sprintf("guest %s(%s): State:%d\n", vm_uuid, vm.Def.Name, vm.Runinfo.Runstate)
			lines += line
		}
	}
	http.Error(w, lines, http.StatusNotImplemented)
}

func (s *Service) StartListening() {
	go func() {
		var err error = s.ListenAndServe()
		if (err != nil && errors.Is(err, http.ErrServerClosed)) {
			logger.Log(err.Error())
		} else {
			logger.Log(err.Error())
		}
	}()
}
