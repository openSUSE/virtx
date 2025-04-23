package virtx

import (
	"fmt"
	"log"
	"net/http"
	"sync"

	"suse.com/virtXD/pkg/hypervisor"
	"suse.com/virtXD/pkg/model"
)

type Vms map[string]openapi.Vm
type Hosts map[string]openapi.Host
type Guests map[string]hypervisor.GuestInfo

type Service struct {
	http.Server
	sync.RWMutex

	logger *log.Logger
	cluster openapi.Cluster
	vms     Vms
	hosts   Hosts
	guests  Guests
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
		vms:       make(Vms),
		hosts:     make(Hosts),
		guests:    make(Guests),
		logger:    logger,
	}
	mux.Handle("/", s)
	return s
}

/* get a host from the list and return whether present */
func (s *Service) GetHost(uuid string) (openapi.Host, error) {
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

func (s *Service) UpdateHost(host openapi.Host) error {
	s.Lock()
	defer s.Unlock()

	return s.updateHost(host)
}

func (s *Service) updateHost(host openapi.Host) error {
	var (
		present bool
		old openapi.Host
	)
	if (s.hosts == nil) {
		s.hosts = make(map[string]openapi.Host)
	}
	old, present = s.hosts[host.Uuid]
	if (present && old.Seq >= host.Seq) {
		s.logger.Printf("Host %s: ignoring obsolete Host information: seq %d >= %d",
			            old.Hostdef.Name, old.Seq, host.Seq)
		return nil
	}
	s.hosts[host.Uuid] = host
	return nil
}

func (s *Service) SetHostState(uuid string, newstate string) error {
	s.Lock()
	defer s.Unlock()

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
	s.Lock()
	defer s.Unlock()

	return s.updateGuest(guestInfo)
}

func (s *Service) updateGuest(guestInfo hypervisor.GuestInfo) error {
	if (s.guests == nil) {
		s.guests = make(map[string]hypervisor.GuestInfo)
	}
	if gi, ok := s.guests[guestInfo.UUID]; ok {
		if gi.Seq >= guestInfo.Seq {
			s.logger.Printf(
				"Ignoring old guest info: seq %d >= %d %s %s",
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

	s.RLock()
	defer s.RUnlock()

	http.Error(w, lines, http.StatusNotImplemented)
}
