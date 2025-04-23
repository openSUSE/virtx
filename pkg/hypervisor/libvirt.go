package hypervisor

import (
	"log"
	"sync/atomic"
	"encoding/xml"

	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"

	"suse.com/virtXD/pkg/model"
	. "suse.com/virtXD/pkg/constants"
)

const (
	QEMUSystemURI = "qemu:///system"

	DomainUndefined = -1
)

type GuestInfo struct {
	Seq       uint64
	Name      string
	UUID      string
	State     int
	Memory    uint64
	NrVirtCpu uint
}

func GuestStateToString(state int) string {
	switch libvirt.DomainState(state) {
	case libvirt.DOMAIN_NOSTATE:
		return "no state"
	case libvirt.DOMAIN_RUNNING:
		return "running"
	case libvirt.DOMAIN_BLOCKED:
		return "blocked on resource"
	case libvirt.DOMAIN_PAUSED:
		return "paused by user"
	case libvirt.DOMAIN_SHUTDOWN:
		return "being shut down"
	case libvirt.DOMAIN_SHUTOFF:
		return "shut off"
	case libvirt.DOMAIN_CRASHED:
		return "crashed"
	case libvirt.DOMAIN_PMSUSPENDED:
		return "suspended by guest pm"
	default:
		return "unknown state"
	}
}

type Hypervisor struct {
	logger        *log.Logger
	conn          *libvirt.Connect
	seq           atomic.Uint64
	callbackID    int
	eventsChannel chan GuestInfo
}

/*
 * Create a Hypervisor type instance,
 * connect to libvirt and return the instance.
 *
 * eventsChannel is the channel used to communicate GuestInfo from the callback
 * returned by virConnectDomainEventRegisterAny
 *
 * https://libvirt.org/html/libvirt-libvirt-domain.html#virConnectDomainEventRegisterAny
 *
 * Note that only one callback is registered for all Domain Events.
 */
func New(logger *log.Logger) (*Hypervisor, error) {
	conn, err := libvirt.NewConnect(QEMUSystemURI)
	if (err != nil) {
		return nil, err
	}
	var hv *Hypervisor = new(Hypervisor)
	hv.logger = logger
	hv.conn = conn
	hv.eventsChannel = nil
	hv.callbackID = -1

	return hv, nil
}

func (hv *Hypervisor) Shutdown() {
	hv.logger.Println("Deregistering event handlers")
	hv.StopListening()
	hv.logger.Println("Closing libvirt connection")
	_, err := hv.conn.Close();
	if (err != nil) {
		hv.logger.Println(err)
	}
}

/*
 * Start listening for domain events.
 * Sets the callbackID and eventsChannel fields of the Hypervisor struct.
 */

func (hv *Hypervisor) StartListening() error {
	lifecycleCb := func(_ *libvirt.Connect, d *libvirt.Domain, e *libvirt.DomainEventLifecycle) {
		var (
			name      string
			uuidStr   string
			info      *libvirt.DomainInfo
			state     int
			memory    uint64
			nrVirtCPU uint
			err       error
		)
		name, err = d.GetName()
		if (err != nil) {
			hv.logger.Println(err)
		}
		uuidStr, err = d.GetUUIDString()
		if (err != nil) {
			hv.logger.Println(err)
		}
		info, err = d.GetInfo()
		if err != nil {
			if e.Event == libvirt.DOMAIN_EVENT_UNDEFINED {
				state = DomainUndefined
			} else {
				hv.logger.Println(err)
			}
		} else {
			state = int(info.State)
			memory = info.Memory
			nrVirtCPU = info.NrVirtCpu
		}
		hv.logger.Printf("[HYPERVISOR] %s/%s: %v %v\n", name, uuidStr, e, info)
		hv.eventsChannel <- GuestInfo{
			Seq:       hv.seq.Add(1),
			Name:      name,
			UUID:      uuidStr,
			State:     state,
			Memory:    memory,
			NrVirtCpu: nrVirtCPU,
		}
	}
	var err error
	hv.callbackID, err = hv.conn.DomainEventLifecycleRegister(nil, lifecycleCb)
	if (err != nil) {
		return err
	}
	hv.eventsChannel = make(chan GuestInfo, 64)
	return nil
}

func (hv *Hypervisor) StopListening() {
	if (hv.callbackID < 0) {
		/* already stopped */
		hv.logger.Println("StopListening(): already stopped.")
		return
	}
	hv.logger.Println("StopListening(): deregister libvirt CallbackId:", hv.callbackID)
	var err error = hv.conn.DomainEventDeregister(hv.callbackID)
	if (err != nil) {
		hv.logger.Fatal(err)
	}
	close(hv.eventsChannel)
	hv.eventsChannel = nil /* assume runtime will garbage collect */
	hv.callbackID = -1
}

/* Calculate and return HostInfo and VMInfo for this host we are running on */

type SysInfo struct {
	BIOS BIOS `xml:"bios"`
}

type BIOS struct {
	Version string `xml:"entry[name='version']"`
	Date    string `xml:"entry[name='date']"`
}

func (hv *Hypervisor) GetHostInfo() (openapi.Host, error) {
	var (
		host openapi.Host
		err error
		xmldata string
		caps libvirtxml.Caps
		smbios SysInfo
		info *libvirt.NodeInfo
	)
	xmldata, err = hv.conn.GetCapabilities()
	if (err != nil) {
		return host, err
	}
	err = caps.Unmarshal(xmldata)
	if (err != nil) {
		return host, err
	}
	info, err = hv.conn.GetNodeInfo()
	if (err != nil) {
		return host, err
	}
	host.Uuid = caps.Host.UUID
	host.Hostdef.Name, err = hv.conn.GetHostname()
	if (err != nil) {
		return host, err
	}
	host.Hostdef.Cpuarch.Arch = caps.Host.CPU.Arch
	host.Hostdef.Cpuarch.Vendor = caps.Host.CPU.Vendor
	host.Hostdef.Cpudef.Model = caps.Host.CPU.Model
	host.Hostdef.Cpudef.Sockets = int32(info.Sockets)
	host.Hostdef.Cpudef.Cores = int32(info.Cores)
	host.Hostdef.Cpudef.Threads = int32(info.Threads)
	host.Hostdef.TscFreq = int64(caps.Host.CPU.Counter.Frequency)
	xmldata, err = hv.conn.GetSysinfo(0)
	if (err != nil) {
		return host, err
	}
	/* XXX libvirtxml go bug/missing feature, workaround does not work XXX */
	err = xml.Unmarshal([]byte(xmldata), &smbios)
	if (err != nil) {
		return host, err
	}
	host.Hostdef.SysinfoBios.Version = smbios.BIOS.Version
	host.Hostdef.SysinfoBios.Date = smbios.BIOS.Date
	host.Hoststate = openapi.ACTIVE
	host.Hostresources.Memory.Total = int64(info.Memory / KiB) /* info returns memory in KiB, translate to MiB */
	var free uint64
	free, err = hv.conn.GetFreeMemory()
	if (err != nil) {
		return host, err
	}
	host.Hostresources.Memory.Free = int64(free / MiB) /* this returns in bytes, translate to MiB */
	host.Hostresources.Memory.Used = host.Hostresources.Memory.Total - host.Hostresources.Memory.Free
	host.Hostresources.Memory.ReservedVms = 0 /* XXX need to calculate based on domains XXX */
	host.Hostresources.Memory.UsedVms = 0     /* XXX need to calculate based on domains XXX */
	host.Hostresources.Memory.AvailableVms = host.Hostresources.Memory.Free - host.Hostresources.Memory.ReservedVms

	/* like VMWare, we calculate the total Mhz as (total_cores * frequency) (excluding threads) */
	host.Hostresources.Mhz.Total = int64(uint(info.Nodes * info.Sockets * info.Cores) * info.MHz)
	host.Hostresources.Mhz.Free = host.Hostresources.Mhz.Total
	host.Hostresources.Mhz.Used = 0 /* XXX need to calculate based on domains XXX */
	host.Hostresources.Mhz.ReservedVms = 0 /* XXX need to calculate based on domains XXX */
	host.Hostresources.Mhz.UsedVms = 0 /* XXX need to calculate based on domains XXX */
	host.Hostresources.Mhz.AvailableVms = host.Hostresources.Mhz.Free - host.Hostresources.Mhz.ReservedVms
	host.Seq = int64(hv.seq.Add(1))

	return host, nil
}

/* Calculate and return GuestInfo */

func (hv *Hypervisor) GuestInfo() ([]GuestInfo, error) {
	var (
		flags libvirt.ConnectListAllDomainsFlags
		doms []libvirt.Domain
		guestInfo []GuestInfo
		err error
	)
	flags = libvirt.CONNECT_LIST_DOMAINS_ACTIVE | libvirt.CONNECT_LIST_DOMAINS_INACTIVE
	doms, err = hv.conn.ListAllDomains(flags)
	if (err != nil) {
		return nil, err
	}
	defer freeDomains(doms)

	guestInfo = make([]GuestInfo, 0, len(doms))
	for _, d := range doms {
		var (
			name string
			uuidStr string
			info *libvirt.DomainInfo
		)
		name, err = d.GetName()
		if (err != nil) {
			return nil, err
		}
		uuidStr, err = d.GetUUIDString()
		if (err != nil) {
			return nil, err
		}
		info, err = d.GetInfo()
		if (err != nil) {
			return nil, err
		}
		guestInfo = append(guestInfo, GuestInfo{
			Seq:       hv.seq.Add(1),
			Name:      name,
			UUID:      uuidStr,
			State:     int(info.State),
			Memory:    info.Memory,
			NrVirtCpu: info.NrVirtCpu,
		})
	}

	return guestInfo, nil
}

/* Return the libvirt domain Events Channel */
func (hv *Hypervisor)EventsChannel() (chan GuestInfo) {
	return hv.eventsChannel
}

func freeDomains(doms []libvirt.Domain) {
	for _, d := range doms {
		d.Free()
	}
}

func init() {
	var err error
	err = libvirt.EventRegisterDefaultImpl();
	if (err != nil) {
		panic(err)
	}

	go func() {
		//logger.Println("Entering event loop")
		for {
			err = libvirt.EventRunDefaultImpl()
			if (err != nil) {
				panic(err)
			}
			// TODO exit properly from the event loop
		}
		//logger.Println("Exiting event loop")
	}()
}
