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

	"suse.com/virtXD/pkg/logger"
	"suse.com/virtXD/pkg/hypervisor"
	"suse.com/virtXD/pkg/model"
)

type Vms map[string]openapi.Vm
type Hosts map[string]openapi.Host
type Guests map[string]hypervisor.GuestInfo

type Service struct {
	http.Server
	m      sync.RWMutex

	cluster openapi.Cluster
	vms     Vms
	hosts   Hosts
	guests  Guests
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
		vms:       make(Vms),
		hosts:     make(Hosts),
		guests:    make(Guests),
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
	if (present && old.Seq >= host.Seq) {
		logger.Log("Host %s: ignoring obsolete Host information: seq %d >= %d",
			old.Hostdef.Name, old.Seq, host.Seq)
		return nil
	}
	s.hosts[host.Uuid] = *host
	return nil
}

func (s *Service) SetHostState(uuid string, newstate string) error {
	s.m.Lock()
	defer s.m.Unlock()

	return s.setHostState(uuid, newstate)
}

func (s *Service) setHostState(uuid string, newstate string) error {
	host, ok := s.hosts[uuid]
	if !ok {
		return fmt.Errorf("no such host %s", uuid)
	}
	host.Hoststate = openapi.Hoststate(newstate)
	return nil
}

func (s *Service) UpdateGuest(guestInfo hypervisor.GuestInfo) error {
	s.m.Lock()
	defer s.m.Unlock()

	return s.updateGuest(guestInfo)
}

func (s *Service) updateGuest(guestInfo hypervisor.GuestInfo) error {
	if (s.guests == nil) {
		s.guests = make(map[string]hypervisor.GuestInfo)
	}
	if gi, ok := s.guests[guestInfo.UUID]; ok {
		if gi.Seq >= guestInfo.Seq {
			logger.Log("Ignoring old guest info: seq %d >= %d %s %s",
				gi.Seq, guestInfo.Seq, guestInfo.UUID, guestInfo.Name,
			)
			return nil
		}
	}
	s.guests[guestInfo.UUID] = guestInfo
	return nil
}

// ServeHTTP implements net/http.Handler
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.m.RLock()
	defer s.m.RUnlock()

	w.Header().Set("Content-Type", "text/plain")
	var (
		lines string
		uuid  string
		hi    openapi.Host
		gi    hypervisor.GuestInfo
	)
	for uuid, hi = range s.hosts {
		var line string = fmt.Sprintf("host uuid %s name %s\n", uuid, hi.Hostdef.Name)
		lines += line
		for uuid, gi = range s.guests {
			var line string = fmt.Sprintf("Guest %s: Name:%s, State:%d, Memory:%d, VCpus: %d \n",
				uuid, gi.Name, gi.State, gi.Memory, gi.NrVirtCpu)
			lines += line
		}
	}
	http.Error(w, lines, http.StatusNotImplemented)
}
