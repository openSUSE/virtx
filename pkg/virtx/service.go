package virtx

import (
	"fmt"
	"log"
	"net/http"
	"sync"

	"suse.com/virtXD/pkg/hypervisor"
)

type HostState struct {
	hypervisor.HostInfo
	Seq    uint64
	Status string
	Guests map[string]hypervisor.GuestInfo
}

type Inventory map[string]HostState

type Service struct {
	http.Server
	sync.RWMutex
	inventory Inventory
	logger    *log.Logger
}

func New(logger *log.Logger) *Service {
	mux := http.NewServeMux()
	s := &Service{
		Server: http.Server{
			Addr:    ":8080",
			Handler: mux,
		},
		RWMutex:   sync.RWMutex{},
		inventory: make(Inventory),
		logger:    logger,
	}
	mux.Handle("/", s)

	return s
}

func (s *Service) HostState(hostKey string) HostState {
	hostState, _ := s.inventory[hostKey]
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
		if err := s.updateGuestState(hostInfo.UUID.String(), gi); err != nil {
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
	uuidStr := hostInfo.UUID.String()
	hostState, ok := s.inventory[uuidStr]
	if ok && hostState.Seq >= hostInfo.Seq {
		s.logger.Printf(
			"Ignoring old host info: seq %d >= %d %s %s",
			hostState.Seq, hostInfo.Seq, hostState.UUID, hostState.Hostname,
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

func (s *Service) SetHostOffline(hostKey string) error {
	s.Lock()
	defer s.Unlock()

	return s.setHostOffline(hostKey)
}

func (s *Service) setHostOffline(hostKey string) error {
	hostState, ok := s.inventory[hostKey]
	if !ok {
		return fmt.Errorf("no such host %s", hostKey)
	}
	hostState.Status = "OFFLINE"
	s.inventory[hostKey] = hostState
	return nil
}

func (s *Service) UpdateGuestState(hostKey string, guestInfo hypervisor.GuestInfo) error {
	s.Lock()
	defer s.Unlock()

	return s.updateGuestState(hostKey, guestInfo)
}

func (s *Service) updateGuestState(hostKey string, guestInfo hypervisor.GuestInfo) error {
	hostState, _ := s.inventory[hostKey]
	if hostState.Guests == nil {
		hostState.Guests = make(map[string]hypervisor.GuestInfo)
	}
	if gi, ok := hostState.Guests[guestInfo.UUID.String()]; ok {
		if gi.Seq >= guestInfo.Seq {
			s.logger.Printf(
				"Ignoring old guest info: seq %d >= %d %s %s",
				gi.Seq, guestInfo.Seq, guestInfo.UUID, guestInfo.Name,
			)
			return nil
		}
	}
	hostState.Guests[guestInfo.UUID.String()] = guestInfo
	s.inventory[hostKey] = hostState
	return nil
}

// ServeHTTP implements net/http.Handler
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")

	s.RLock()
	defer s.RUnlock()

	http.Error(w, "Nothing implemented yet.", http.StatusNotImplemented)
}
