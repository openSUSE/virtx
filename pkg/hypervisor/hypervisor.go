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

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	. "suse.com/virtx/pkg/constants"
)

const (
	QEMUSystemURI = "qemu:///system"

	DomainUndefined = -1
)

type VmEvent struct {
	Uuid string
	State openapi.Vmrunstate
	Ts int64
}

/*
 * VmStat are statistics collected every 15 seconds
 */

type VmStat struct {
	Uuid string                 /* VM Uuid */
	Name string                 /* VM Name */
	Runinfo openapi.Vmruninfo   /* host running the VM and VM runstate */
	Vlanid int16                /* XXX need requirements engineering for Vlans XXX */
	Custom []openapi.CustomField

	Cpus int16                  /* Total number of vcpus for the domain from libvirt.DomainInfo */
	CpuTime uint64              /* Total cpu time consumed in nanoseconds from libvirt.DomainCPUStats.CpuTime */
	CpuUtilization int16        /* % of total cpu resources used */

	MemoryCapacity uint64       /* Memory assigned to VM in MiB from virDomainInfo->memory / 1024 */
	MemoryUsed uint64           /* Memory in MiB of the QEMU RSS process, VIR_DOMAIN_MEMORY_STAT_RSS / 1024 */

	DiskCapacity uint64         /* Disk Total virtual capacity (MiB) from virDomainBlockInfo->capacity / MiB*/
	DiskAllocation uint64       /* Disk Allocated on host (MiB) from Info->allocation / MiB */
	DiskPhysical uint64         /* Disk Physical on host (MiB) from Info->physical, including metadata */

	NetRx int64                 /* Net Rx bytes */
	NetTx int64                 /* Net Tx bytes */
	NetRxBW int32               /* Net Rx KiB/s */
	NetTxBW int32               /* Net Tx KiB/s */

	Ts int64
}

type SystemInfo struct {
	Host openapi.Host
	VmStats []VmStat
}

type Hypervisor struct {
	conn *libvirt.Connect
	callbackID int
	vmEventCh chan VmEvent
	systemInfoCh chan SystemInfo
}

/*
 * Create a Hypervisor type instance,
 * connect to libvirt and return the instance.
 *
 * vmEventCh is the channel used to communicate VmEvent from the callback
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
	hv.vmEventCh = nil
	hv.systemInfoCh = nil
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
func getDomainInfo(d *libvirt.Domain) (string, string, openapi.Vmrunstate, error) {
	var (
		name string
		uuid string
		reason int
		state libvirt.DomainState
		enumState openapi.Vmrunstate
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
		enumState = openapi.RUNSTATE_RUNNING
	case libvirt.DOMAIN_PAUSED:
		switch (reason) {
		case int(libvirt.DOMAIN_PAUSED_MIGRATION): /* paused for offline migration */
			enumState = openapi.RUNSTATE_MIGRATING
		case int(libvirt.DOMAIN_PAUSED_SHUTTING_DOWN):
			enumState = openapi.RUNSTATE_TERMINATING
		case int(libvirt.DOMAIN_PAUSED_CRASHED):
			enumState = openapi.RUNSTATE_CRASHED
		case int(libvirt.DOMAIN_PAUSED_STARTING_UP):
			enumState = openapi.RUNSTATE_STARTUP
		default:
			enumState = openapi.RUNSTATE_PAUSED
		}
	case libvirt.DOMAIN_SHUTDOWN:
		enumState = openapi.RUNSTATE_TERMINATING
	case libvirt.DOMAIN_SHUTOFF:
		switch (reason) {
		case int(libvirt.DOMAIN_SHUTOFF_CRASHED):
			enumState = openapi.RUNSTATE_CRASHED
		case int(libvirt.DOMAIN_SHUTOFF_MIGRATED):
			/* XXX what to do when domain goes away from this host? XXX */
		default:
			enumState = openapi.RUNSTATE_POWEROFF
		}
	case libvirt.DOMAIN_CRASHED:
		enumState = openapi.RUNSTATE_CRASHED
	case libvirt.DOMAIN_PMSUSPENDED:
		enumState = openapi.RUNSTATE_PMSUSPENDED
	default:
		logger.Log("Unhandled state %d, reason %d", state, reason)
	}
out:
	return name, uuid, enumState, err
}

/*
 * Regularly fetch all system information (host info and vms info), and send it via systemInfoCh.
 */
func (hv *Hypervisor) systemInfoLoop(seconds int) error {
	var (
		si SystemInfo
		err error
		ticker *time.Ticker
	)
	ticker = time.NewTicker(time.Duration(seconds) * time.Second)
	defer ticker.Stop()

	err = hv.getSystemInfo(&si)
	if (err != nil) {
		logger.Log(err.Error())
	} else {
		hv.systemInfoCh <- si
	}
	for range ticker.C {
		err = hv.getSystemInfo(&si)
		if (err != nil) {
			logger.Log(err.Error())
			continue
		}
		hv.systemInfoCh <- si
	}
	logger.Log("systemInfoLoop Exiting!")
	return nil
}

/*
 * Start listening for domain events and collecting system information.
 * Sets the callbackID, vmEventCh and systemInfoCh fields of the Hypervisor struct.
 * Collects system information every "seconds" seconds.
 */
func (hv *Hypervisor) StartListening(seconds int) error {
	lifecycleCb := func(_ *libvirt.Connect, d *libvirt.Domain, e *libvirt.DomainEventLifecycle) {
		var (
			name, uuid string
			state openapi.Vmrunstate
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
		logger.Log("[VmEvent] %s/%s: %v state: %d", name, uuid, e, state)
		hv.vmEventCh <- VmEvent{ Uuid: uuid, State: state, Ts: time.Now().UTC().UnixMilli() }
	}
	var err error
	hv.vmEventCh = make(chan VmEvent, 64)
	hv.systemInfoCh = make(chan SystemInfo, 64)
	hv.callbackID, err = hv.conn.DomainEventLifecycleRegister(nil, lifecycleCb)
	if (err != nil) {
		return err
	}
	go hv.systemInfoLoop(seconds)
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
	close(hv.vmEventCh)
	close(hv.systemInfoCh)
	hv.vmEventCh = nil /* assume runtime will garbage collect */
	hv.callbackID = -1
}

/* Calculate and return HostInfo and VMInfo for this host we are running on */

type xmlSysInfo struct {
	BIOS xmlBIOS `xml:"bios"`
}

type xmlBIOS struct {
	Entries []xmlEntry `xml:"entry"`
}

type xmlEntry struct {
	Name  string `xml:"name,attr"`
	Value string `xml:",chardata"`
}

func (hv *Hypervisor) getSystemInfo(si *SystemInfo) error {
	var (
		host openapi.Host
		vmstats []VmStat
		err error
		xmldata string
		caps libvirtxml.Caps
		smbios xmlSysInfo
		info *libvirt.NodeInfo
		ts int64
	)
	var (
		doms []libvirt.Domain
		d libvirt.Domain
	)
	var (
		freeMemory uint64
		totalMemoryCapacity uint64
		totalMemoryUsed uint64
		totalCpus uint32
	)

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
	host.Def.Cpudef.Nodes = int16(info.Nodes)
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
	host.State = openapi.HOST_ACTIVE
	ts = time.Now().UTC().UnixMilli()
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

	vmstats = make([]VmStat, 0, len(doms))
	for _, d = range doms {
		var (
			vm VmStat
		)
		vm.Name, vm.Uuid, vm.Runinfo.Runstate, err = getDomainInfo(&d)
		if (err != nil) {
			logger.Log("could not getDomainInfo: %s", err.Error())
			continue
		}
		err = getDomainStats(&d, &vm)
		vm.Runinfo.Host = host.Uuid
		totalMemoryCapacity += vm.MemoryCapacity
		totalMemoryUsed += vm.MemoryUsed
		totalCpus += uint32(vm.Cpus) /* XXX maybe we should use Topology and exclude threads? XXX */
		//cpuPercent := float64(delta) / (15.0 * float64(cpus) * 1e9) * 100.0
		vm.Ts = ts
		vmstats = append(vmstats, vm)
	}
	/* now calculate host resources */
	host.Resources.Memory.Total = int32(info.Memory / KiB) /* info returns memory in KiB, translate to MiB */
	freeMemory, err = hv.conn.GetFreeMemory()
	if (err != nil) {
		goto out
	}
	/* XXX no overcommit is currently implemented XXX */
	host.Resources.Memory.Free = int32(freeMemory / MiB) /* this returns in bytes, translate to MiB */
	host.Resources.Memory.Used = host.Resources.Memory.Total - host.Resources.Memory.Free
	host.Resources.Memory.ReservedOs = 0  /* XXX need to implement XXX */
	host.Resources.Memory.ReservedVms = int32(totalMemoryCapacity)
	host.Resources.Memory.UsedVms = int32(totalMemoryUsed)
	host.Resources.Memory.AvailableVms = host.Resources.Memory.Total -
		host.Resources.Memory.ReservedOs - host.Resources.Memory.ReservedVms

	/* like VMWare, we calculate the total Mhz as (total_cores * frequency) (excluding threads) */
	/* XXX no overcommit is currently implemented XXX */
	host.Resources.Cpu.Total = int32(uint(info.Nodes * info.Sockets * info.Cores) * info.MHz)
	host.Resources.Cpu.Used = 0 /* XXX */
	host.Resources.Cpu.Free = host.Resources.Cpu.Total - host.Resources.Cpu.Used
	host.Resources.Cpu.ReservedOs = 0  /* XXX */
	host.Resources.Cpu.ReservedVms = int32(totalCpus * uint32(info.MHz))
	host.Resources.Cpu.UsedVms = 0 /* XXX */
	host.Resources.Cpu.AvailableVms = host.Resources.Cpu.Free - host.Resources.Cpu.ReservedVms

out:
	si.Host = host
	si.VmStats = vmstats
	return err
}

type xmlDisk struct {
	Device string `xml:"device,attr"`
	Source struct {
		File string `xml:"file,attr"`
	} `xml:"source"`
}

type xmlInterface struct {
	Target struct {
		Dev string `xml:"dev,attr"`
	} `xml:"target"`
	/* Type string `xml:"type,attr"` */
	Vlan struct {
		Tags [] struct {
			Id int `xml:"id,attr"`
		} `xml:"tag"`
	} `xml:"vlan"`
}

type xmlDomain struct {
	Devices struct {
		Disks []xmlDisk `xml:"disk"`
		Interfaces []xmlInterface `xml:"interface"`
	} `xml:"devices"`
}

func getDomainStats(d *libvirt.Domain, vm *VmStat) error {
	var err error
	/*
	{
		var cpustat []libvirt.DomainCPUStats
		cpustat, err = d.GetCPUStats(-1, 1, 0)
		if (err != nil) {
			return err
		}
		Vm.CpuTime = cpustat[0].CpuTime
	}
	*/
	{
		var info *libvirt.DomainInfo
		var memstat []libvirt.DomainMemoryStat
		info, err = d.GetInfo()
		if (err != nil) {
			return err
		}
		vm.Cpus = int16(info.NrVirtCpu)
		vm.CpuTime = info.CpuTime
		vm.MemoryCapacity = info.Memory / KiB /* convert from KiB to MiB */
		memstat, err = d.MemoryStats(20, 0)
		if (err != nil) {
			return err
		}
		for _, stat := range memstat {
			if (libvirt.DomainMemoryStatTags(stat.Tag) == libvirt.DOMAIN_MEMORY_STAT_RSS) {
				vm.MemoryUsed = stat.Val / KiB /* convert from KiB to MiB */
				break
			}
		}
	}
	{
		// Retrieve the domain's XML description
		var (
			xmldata string
			xd xmlDomain
		)
		xmldata, err = d.GetXMLDesc(0)
		if (err != nil) {
			return err
		}
		err = xml.Unmarshal([]byte(xmldata), &xd)
		if (err != nil) {
			return err
		}
		for _, disk := range xd.Devices.Disks {
			if (disk.Device == "disk" && disk.Source.File != "") {
				var blockinfo *libvirt.DomainBlockInfo
				blockinfo, err = d.GetBlockInfo(disk.Source.File, 0)
				if (err != nil) {
					return err
				}
				vm.DiskCapacity += blockinfo.Capacity / MiB
				vm.DiskAllocation += blockinfo.Allocation / MiB
				vm.DiskPhysical += blockinfo.Physical / MiB
			}
		}
		for _, net := range xd.Devices.Interfaces {
			if (net.Target.Dev != "") {
				var netstat *libvirt.DomainInterfaceStats
				netstat, err = d.InterfaceStats(net.Target.Dev)
				if (err != nil) {
					return err
				}
				if (netstat.RxBytesSet) {
					vm.NetRx += netstat.RxBytes
				}
				if (netstat.TxBytesSet) {
					vm.NetTx += netstat.TxBytes
				}
			}
			if (len(net.Vlan.Tags) > 0) {
				vm.Vlanid = int16(net.Vlan.Tags[0].Id) /* XXX only one VlandID for each VM is recognized XXX */
			}
		}
	}
	return nil
}

/* Return the libvirt domain Events Channel */
func (hv *Hypervisor)GetVmEventCh() (chan VmEvent) {
	return hv.vmEventCh
}

/* Return the systemInfo Events Channel */
func (hv *Hypervisor)GetSystemInfoCh() (chan SystemInfo) {
	return hv.systemInfoCh
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
