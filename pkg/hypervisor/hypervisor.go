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
package hypervisor

import (
	"time"
	"encoding/xml"

	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"

	"suse.com/virtXD/pkg/model"
	"suse.com/virtXD/pkg/logger"
	. "suse.com/virtXD/pkg/constants"
)

const (
	QEMUSystemURI = "qemu:///system"

	DomainUndefined = -1
)

type GuestInfo struct {
	Ts        int64
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
	conn          *libvirt.Connect
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
func New() (*Hypervisor, error) {
	conn, err := libvirt.NewConnect(QEMUSystemURI)
	if (err != nil) {
		return nil, err
	}
	var hv *Hypervisor = new(Hypervisor)
	hv.conn = conn
	hv.eventsChannel = nil
	hv.callbackID = -1

	return hv, nil
}

func (hv *Hypervisor) Shutdown() {
	logger.Log("Deregistering event handlers")
	hv.StopListening()
	logger.Log("Closing libvirt connection")
	_, err := hv.conn.Close();
	if (err != nil) {
		logger.Log(err.Error())
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
			logger.Log(err.Error())
		}
		uuidStr, err = d.GetUUIDString()
		if (err != nil) {
			logger.Log(err.Error())
		}
		info, err = d.GetInfo()
		if err != nil {
			if e.Event == libvirt.DOMAIN_EVENT_UNDEFINED {
				state = DomainUndefined
			} else {
				logger.Log(err.Error())
			}
		} else {
			state = int(info.State)
			memory = info.Memory
			nrVirtCPU = info.NrVirtCpu
		}
		logger.Log("[HYPERVISOR] %s/%s: %v %v\n", name, uuidStr, e, info)
		hv.eventsChannel <- GuestInfo{
			Ts:        time.Now().UTC().Unix(),
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
		logger.Log("StopListening(): already stopped.")
		return
	}
	logger.Log("StopListening(): deregister libvirt CallbackId: %d", hv.callbackID)
	var err error = hv.conn.DomainEventDeregister(hv.callbackID)
	if (err != nil) {
		logger.Log(err.Error())
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
	Entries []Entry `xml:"entry"`
}

type Entry struct {
	Name  string `xml:"name,attr"`
	Value string `xml:",chardata"`
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
	host.Def.Name, err = hv.conn.GetHostname()
	if (err != nil) {
		return host, err
	}
	host.Def.Cpuarch.Arch = caps.Host.CPU.Arch
	host.Def.Cpuarch.Vendor = caps.Host.CPU.Vendor
	host.Def.Cpudef.Model = caps.Host.CPU.Model
	host.Def.Cpudef.Sockets = int16(info.Sockets)
	host.Def.Cpudef.Cores = int16(info.Cores)
	host.Def.Cpudef.Threads = int16(info.Threads)
	host.Def.TscFreq = int64(caps.Host.CPU.Counter.Frequency)
	xmldata, err = hv.conn.GetSysinfo(0)
	if (err != nil) {
		return host, err
	}
	/* XXX libvirtxml go bug/missing feature, workaround does not work XXX */
	err = xml.Unmarshal([]byte(xmldata), &smbios)
	if (err != nil) {
		return host, err
	}
	for _, e := range smbios.BIOS.Entries {
		switch e.Name {
		case "version":
			host.Def.SysinfoBios.Version = e.Value
		case "date":
			host.Def.SysinfoBios.Date = e.Value
		}
	}
	host.State = openapi.ACTIVE
	host.Resources.Memory.Total = int32(info.Memory * KiB / GiB) /* info returns memory in KiB, translate to GiB */
	var free uint64
	free, err = hv.conn.GetFreeMemory()
	if (err != nil) {
		return host, err
	}
	host.Resources.Memory.Free = int32(free / GiB) /* this returns in bytes, translate to GiB */
	host.Resources.Memory.Used = host.Resources.Memory.Total - host.Resources.Memory.Free
	host.Resources.Memory.ReservedVms = 0 /* XXX need to calculate based on domains XXX */
	host.Resources.Memory.ReservedOs = 0  /* XXX need to implement XXX */
	host.Resources.Memory.UsedVms = 0     /* XXX need to calculate based on domains XXX */
	host.Resources.Memory.AvailableVms = host.Resources.Memory.Total -
		host.Resources.Memory.ReservedOs - host.Resources.Memory.ReservedVms

	/* like VMWare, we calculate the total Mhz as (total_cores * frequency) (excluding threads) */
	host.Resources.Cpu.Total = int32(uint(info.Nodes * info.Sockets * info.Cores) * info.MHz)
	host.Resources.Cpu.Free = host.Resources.Cpu.Total
	host.Resources.Cpu.Used = 0 /* XXX need to calculate based on domains XXX */
	host.Resources.Cpu.ReservedVms = 0 /* XXX need to calculate based on domains XXX */
	host.Resources.Cpu.ReservedOs = 0  /* XXX need to implement XXX */
	host.Resources.Cpu.UsedVms = 0 /* XXX need to calculate based on domains XXX */
	host.Resources.Cpu.AvailableVms = host.Resources.Cpu.Free - host.Resources.Cpu.ReservedVms
	host.Ts = time.Now().UTC().Unix()

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
			Ts:        time.Now().UTC().Unix(),
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

/*
 * XXX this is terrible, but libvirt forces us to run this before connecting.
 * And how to guarantee that we have reached the right point in EventRunDefaultImpl()
 * before actually calling connect?
 * Using a goroutine does not work, because we would have to send to a channel inside
 * EventRunDefaultImpl().
 */
func init() {
	var err error
	err = libvirt.EventRegisterDefaultImpl();
	if (err != nil) {
		panic(err)
	}
	go func() {
		logger.Log("hypervisor: init(): Entering event loop")
		for {
			err = libvirt.EventRunDefaultImpl()
			if (err != nil) {
				panic(err)
			}
			// XXX exit properly from the event loop somehow
		}
		logger.Log("hypervisor: init(): Exiting event loop")
	}()
}
