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

type VmEvent struct {
	Uuid      string
	Name      string
	State     string
	Ts        int64
}

type Hypervisor struct {
	conn          *libvirt.Connect
	callbackID    int
	eventsChannel chan VmEvent
}

/*
 * Create a Hypervisor type instance,
 * connect to libvirt and return the instance.
 *
 * eventsChannel is the channel used to communicate VmEvent from the callback
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

/* get basic information about a Domain */
func getDomainInfo(d *libvirt.Domain) (string, string, string, error) {
	var (
		name string
		uuid string
		reason int
		state libvirt.DomainState
		statestr string
		err error
	)
	name, err = d.GetName()
	if (err != nil) {
		goto out
	}
	uuid, err = d.GetUUIDString()
	if (err != nil) {
		goto out
	}
	state, reason, err = d.GetState()
	if (err != nil) {
		goto out
	}
	logger.Log("getDomainInfo: state %d, reason %d", state, reason)
	switch (state) {
	//case libvirt.DOMAIN_NOSTATE: /* ?XXX? */
	case libvirt.DOMAIN_RUNNING:
		fallthrough
	case libvirt.DOMAIN_BLOCKED: /* ?XXX? */
		statestr = string(openapi.RUNNING)
	case libvirt.DOMAIN_PAUSED:
		switch (reason) {
		case int(libvirt.DOMAIN_PAUSED_MIGRATION): /* paused for offline migration */
			statestr = string(openapi.MIGRATING)
		case int(libvirt.DOMAIN_PAUSED_SHUTTING_DOWN):
			statestr = string(openapi.TERMINATING)
		case int(libvirt.DOMAIN_PAUSED_CRASHED):
			statestr = string(openapi.CRASHED)
		case int(libvirt.DOMAIN_PAUSED_STARTING_UP):
			statestr = string(openapi.STARTUP)
		default:
			statestr = string(openapi.PAUSED)
		}
	case libvirt.DOMAIN_SHUTDOWN:
		statestr = string(openapi.TERMINATING)
	case libvirt.DOMAIN_SHUTOFF:
		switch (reason) {
		case int(libvirt.DOMAIN_SHUTOFF_CRASHED):
			statestr = string(openapi.CRASHED)
		case int(libvirt.DOMAIN_SHUTOFF_MIGRATED):
			/* XXX what to do when domain goes away from this host? XXX */
		default:
			statestr = string(openapi.POWEROFF)
		}
	case libvirt.DOMAIN_CRASHED:
		statestr = string(openapi.CRASHED)
	case libvirt.DOMAIN_PMSUSPENDED:
		statestr = string(openapi.PMSUSPENDED)
	default:
		logger.Log("Unhandled state %d, reason %d", state, reason)
	}
out:
	return name, uuid, statestr, err
}
/*
 * Start listening for domain events.
 * Sets the callbackID and eventsChannel fields of the Hypervisor struct.
 */

func (hv *Hypervisor) StartListening() error {
	lifecycleCb := func(_ *libvirt.Connect, d *libvirt.Domain, e *libvirt.DomainEventLifecycle) {
		var (
			name, uuid, state string
			err error
		)
		name, uuid, state, err = getDomainInfo(d)
		if (err != nil) {
			if e.Event == libvirt.DOMAIN_EVENT_UNDEFINED {
				/* XXX handle this XXX */
			} else {
				logger.Log(err.Error())
			}
		}
		logger.Log("[VmEvent] %s/%s: %v state: %s", name, uuid, e, state)
		hv.eventsChannel <- VmEvent{ Name: name, Uuid: uuid, State: state, Ts: time.Now().UTC().Unix() }
	}
	var err error
	hv.callbackID, err = hv.conn.DomainEventLifecycleRegister(nil, lifecycleCb)
	if (err != nil) {
		return err
	}
	hv.eventsChannel = make(chan VmEvent, 64)
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

func (hv *Hypervisor) GetSystemInfo() (openapi.Host, []openapi.Vm, error) {
	var (
		host openapi.Host
		vms []openapi.Vm
		err error
		xmldata string
		caps libvirtxml.Caps
		smbios SysInfo
		info *libvirt.NodeInfo
		ts int64
	)
	var (
		doms []libvirt.Domain
		d libvirt.Domain
	)
	var free uint64

	/* 1. set the general host information */
	xmldata, err = hv.conn.GetCapabilities()
	if (err != nil) {
		goto out
	}
	err = caps.Unmarshal(xmldata)
	if (err != nil) {
		goto out
	}
	info, err = hv.conn.GetNodeInfo()
	if (err != nil) {
		goto out
	}
	host.Uuid = caps.Host.UUID
	host.Def.Name, err = hv.conn.GetHostname()
	if (err != nil) {
		goto out
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
		goto out
	}
	/* workaround for libvirtxml go bindings bug/missing feature. Should behave like libvirtxml.Caps() instead. */
	err = xml.Unmarshal([]byte(xmldata), &smbios)
	if (err != nil) {
		goto out
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
	ts = time.Now().UTC().Unix()
	host.Ts = ts

	/*
	 * 2. get information about all the domains, so that we can calculate
	 *    host resources later.
	 */
	doms, err = hv.conn.ListAllDomains(0)
	if (err != nil) {
		goto out
	}
	defer freeDomains(doms)

	vms = make([]openapi.Vm, 0, len(doms))
	for _, d = range doms {
		var (
			vm openapi.Vm
			state string
		)
		vm.Vmdef.Name, vm.Uuid, state, err = getDomainInfo(&d)
		if (err != nil) {
			logger.Log("could not getDomainInfo: %s", err.Error())
			continue
		}
		vm.Runstate.State = openapi.Vmrunstate(state)
		vm.Runstate.Host = host.Uuid
		vm.Ts = ts
		xmldata, err = d.GetXMLDesc(0)
		if (err != nil) {
			logger.Log("could not getXMLDesc: %s", err.Error())
			continue
		}
		/* XXX get all the other fields from XML Desc I presume XXX */
		/*
		Custom []CustomField `json:"custom"`
		Genid string `json:"genid"`
		Vlanid int16 `json:"vlanid"`
		Firmware string `json:"firmware"`
		Nets []Net `json:"nets"`
		Disks []Disk `json:"disks"`
		Memory VmdefMemory `json:"memory"`
		Cpudef Cpudef `json:"cpudef"`
		*/
		vms = append(vms, vm)
	}
	/* now calculate host resources */
	host.Resources.Memory.Total = int32(info.Memory * KiB / GiB) /* info returns memory in KiB, translate to GiB */
	free, err = hv.conn.GetFreeMemory()
	if (err != nil) {
		goto out
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

out:
	return host, vms, err
}

/* Return the libvirt domain Events Channel */
func (hv *Hypervisor)EventsChannel() (chan VmEvent) {
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
