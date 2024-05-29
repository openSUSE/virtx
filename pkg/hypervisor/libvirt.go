package hypervisor

import (
	"log"

	"github.com/google/uuid"

	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"
)

const (
	QEMUSystemURI = "qemu:///system"
)

type HostInfo struct {
	Hostname string
	UUID     uuid.UUID
}

type GuestInfo struct {
	Name      string
	UUID      uuid.UUID
	State     int
	Memory    uint64
	NrVirtCpu uint
}

type hypervisor struct {
	logger   *log.Logger
	conn     *libvirt.Connect
	handlers map[int]chan<- GuestInfo
}

func New(logger *log.Logger) (*hypervisor, error) {
	conn, err := libvirt.NewConnect(QEMUSystemURI)
	if err != nil {
		return nil, err
	}
	return &hypervisor{
		logger:   logger,
		conn:     conn,
		handlers: make(map[int]chan<- GuestInfo),
	}, nil
}

func (h *hypervisor) HostInfo() (*HostInfo, error) {
	hostname, err := h.conn.GetHostname()
	if err != nil {
		return nil, err
	}

	capsXml, err := h.conn.GetCapabilities()
	if err != nil {
		return nil, err
	}
	caps := libvirtxml.Caps{}
	if err := caps.Unmarshal(capsXml); err != nil {
		return nil, err
	}

	return &HostInfo{
		Hostname: hostname,
		UUID:     uuid.MustParse(caps.Host.UUID),
	}, nil
}

func (h *hypervisor) GuestInfo() ([]GuestInfo, error) {
	flags := libvirt.CONNECT_LIST_DOMAINS_ACTIVE | libvirt.CONNECT_LIST_DOMAINS_INACTIVE
	doms, err := h.conn.ListAllDomains(flags)
	if err != nil {
		return nil, err
	}
	defer freeDomains(doms)

	guestInfo := make([]GuestInfo, 0, len(doms))
	for _, d := range doms {
		name, err := d.GetName()
		if err != nil {
			return nil, err
		}
		uuidStr, err := d.GetUUIDString()
		if err != nil {
			return nil, err
		}
		info, err := d.GetInfo()
		if err != nil {
			return nil, err
		}
		guestInfo = append(guestInfo, GuestInfo{
			Name:      name,
			UUID:      uuid.MustParse(uuidStr),
			State:     int(info.State),
			Memory:    info.Memory,
			NrVirtCpu: info.NrVirtCpu,
		})
	}

	return guestInfo, nil
}

func freeDomains(doms []libvirt.Domain) {
	for _, d := range doms {
		d.Free()
	}
}

func (h *hypervisor) Watch(eventCh chan<- GuestInfo) (int, error) {
	lifecycleCb := func(_ *libvirt.Connect, d *libvirt.Domain, e *libvirt.DomainEventLifecycle) {
		var (
			state     int
			memory    uint64
			nrVirtCPU uint
		)
		name, err := d.GetName()
		if err != nil {
			h.logger.Fatal(err)
		}
		uuidStr, err := d.GetUUIDString()
		if err != nil {
			h.logger.Fatal(err)
		}
		info, err := d.GetInfo()
		if err != nil {
			if e.Event == libvirt.DOMAIN_EVENT_UNDEFINED {
				state = -1
			} else {
				h.logger.Fatal(err)
			}
		} else {
			state = int(info.State)
			memory = info.Memory
			nrVirtCPU = info.NrVirtCpu
		}
		h.logger.Printf("[HYPERVISOR] %s/%s: %v %v\n", name, uuidStr, e, info)
		eventCh <- GuestInfo{
			Name:      name,
			UUID:      uuid.MustParse(uuidStr),
			State:     state,
			Memory:    memory,
			NrVirtCpu: nrVirtCPU,
		}
	}
	id, err := h.conn.DomainEventLifecycleRegister(nil, lifecycleCb)
	if err != nil {
		return id, err
	}
	h.handlers[id] = eventCh

	return id, nil
}

func (h *hypervisor) Stop(watchId int) {
	eventCh, ok := h.handlers[watchId]
	if !ok {
		return
	}

	h.logger.Println("Deregister:", watchId)
	if err := h.conn.DomainEventDeregister(watchId); err != nil {
		h.logger.Fatal(err)
	}

	delete(h.handlers, watchId)
	if eventCh != nil {
		close(eventCh)
	}
}

func (h *hypervisor) Shutdown() {
	h.logger.Println("Deregistering event handlers")
	for id := range h.handlers {
		h.Stop(id)
	}
	h.logger.Println("Closing libvirt connection")
	if _, err := h.conn.Close(); err != nil {
		h.logger.Fatal(err)
	}
}

func init() {
	if err := libvirt.EventRegisterDefaultImpl(); err != nil {
		panic(err)
	}

	go func() {
		//logger.Println("Entering event loop")
		for {
			if err := libvirt.EventRunDefaultImpl(); err != nil {
				panic(err)
			}
			// TODO exit properly from the event loop
		}
		//logger.Println("Exiting event loop")
	}()
}
