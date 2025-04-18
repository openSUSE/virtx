package virtx

import (
	"fmt"
	"log"
	"net/http"
	"sync"

	"suse.com/virtXD/pkg/hypervisor"
	"suse.com/virtXD/pkg/model"
)

type HostState struct {
	HostInfo hypervisor.HostInfo
	Seq    uint64
	Status string
	Guests map[string]hypervisor.GuestInfo
}

type Inventory map[string]HostState

type Service struct {
	http.Server
	sync.RWMutex
	inventory Inventory
	logger *log.Logger
	cluster openapi.Cluster
}

func New(logger *log.Logger) *Service {
	mux := http.NewServeMux()
	s := &Service{
		Server: http.Server{
			Addr:    ":8080",
			Handler: mux,
		},
		RWMutex:   sync.RWMutex{},
		cluster:   openapi.Cluster{},
		inventory: make(Inventory),
		logger:    logger,
	}
	mux.Handle("/", s)

	return s
}

func (s *Service) HostState(uuid string) HostState {
	hostState, _ := s.inventory[uuid]
	return hostState
}

func (s *Service) Update(
	hostInfo hypervisor.HostInfo,
	guestInfo []hypervisor.GuestInfo,
) error {
	s.Lock()
	defer s.Unlock()

	if err := s.updateHostState(hostInfo); err != nil {
		return err
	}

	for _, gi := range guestInfo {
		if err := s.updateGuestState(hostInfo.UUID, gi); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) UpdateHostState(hostInfo hypervisor.HostInfo) error {
	s.Lock()
	defer s.Unlock()

	return s.updateHostState(hostInfo)
}

func (s *Service) updateHostState(hostInfo hypervisor.HostInfo) error {
	uuidStr := hostInfo.UUID
	hostState, ok := s.inventory[uuidStr]
	if ok && hostState.Seq >= hostInfo.Seq {
		s.logger.Printf(
			"Ignoring old host info: seq %d >= %d %s %s",
			hostState.Seq, hostInfo.Seq, hostState.HostInfo.UUID, hostState.HostInfo.Hostname,
		)
		return nil
	}
	hostState.HostInfo = hostInfo
	hostState.Status = "ONLINE"
	if hostState.Guests == nil {
		hostState.Guests = make(map[string]hypervisor.GuestInfo)
	}
	s.inventory[uuidStr] = hostState
	return nil
}

func (s *Service) SetHostOffline(uuid string) error {
	s.Lock()
	defer s.Unlock()

	return s.setHostOffline(uuid)
}

func (s *Service) setHostOffline(uuid string) error {
	hostState, ok := s.inventory[uuid]
	if !ok {
		return fmt.Errorf("no such host %s", uuid)
	}
	hostState.Status = "OFFLINE"
	s.inventory[uuid] = hostState
	return nil
}

func (s *Service) UpdateGuestState(uuid string, guestInfo hypervisor.GuestInfo) error {
	s.Lock()
	defer s.Unlock()

	return s.updateGuestState(uuid, guestInfo)
}

func (s *Service) updateGuestState(uuid string, guestInfo hypervisor.GuestInfo) error {
	hostState, _ := s.inventory[uuid]
	if hostState.Guests == nil {
		hostState.Guests = make(map[string]hypervisor.GuestInfo)
	}
	if gi, ok := hostState.Guests[guestInfo.UUID]; ok {
		if gi.Seq >= guestInfo.Seq {
			s.logger.Printf(
				"Ignoring old guest info: seq %d >= %d %s %s",
				gi.Seq, guestInfo.Seq, guestInfo.UUID, guestInfo.Name,
			)
			return nil
		}
	}
	hostState.Guests[guestInfo.UUID] = guestInfo
	s.inventory[uuid] = hostState
	return nil
}

// ServeHTTP implements net/http.Handler
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	var (
		lines string
		uuid  string
		h     HostState
		hi    hypervisor.HostInfo
		gi    hypervisor.GuestInfo
	)
	for uuid, h = range s.inventory {
		hi = h.HostInfo
		var line string = fmt.Sprintf("Host %s (%s): Hostname:%s, Arch:%s, Vendor:%s, Model:%s \n",
			uuid, h.Status, hi.Hostname, hi.Arch, hi.Vendor, hi.Model)
		lines += line
		for uuid, gi = range h.Guests {
			var line string = fmt.Sprintf("Guest %s: Name:%s, State:%d, Memory:%d, VCpus: %d \n",
				uuid, gi.Name, gi.State, gi.Memory, gi.NrVirtCpu)
			lines += line
		}
	}

	s.RLock()
	defer s.RUnlock()

	http.Error(w, lines, http.StatusNotImplemented)
}
