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
	libvirt_uri = "qemu:///system"
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
	Cpu_time uint64             /* Total cpu time consumed in nanoseconds from libvirt.DomainCPUStats.CpuTime */
	Cpu_utilization int16       /* % of total cpu resources used */

	Memory_capacity uint64       /* Memory assigned to VM in MiB from virDomainInfo->memory / 1024 */
	Memory_used uint64           /* Memory in MiB of the QEMU RSS process, VIR_DOMAIN_MEMORY_STAT_RSS / 1024 */

	Disk_capacity uint64         /* Disk Total virtual capacity (MiB) from virDomainBlockInfo->capacity / MiB*/
	Disk_allocation uint64       /* Disk Allocated on host (MiB) from Info->allocation / MiB */
	Disk_physical uint64         /* Disk Physical on host (MiB) from Info->physical, including metadata */

	Net_rx int64                 /* Net Rx bytes */
	Net_tx int64                 /* Net Tx bytes */
	Net_rx_bw int32              /* Net Rx KiB/s */
	Net_tx_bw int32              /* Net Tx KiB/s */

	Ts int64
}

type SystemInfo struct {
	Host openapi.Host
	Vm_stats []VmStat
}

type Hypervisor struct {
	conn *libvirt.Connect
	callback_id int
	vm_event_ch chan VmEvent
	system_info_ch chan SystemInfo
}

/*
 * Create a Hypervisor type instance,
 * connect to libvirt and return the instance.
 *
 * vm_event_ch is the channel used to communicate VmEvent from the callback
 * returned by virConnectDomainEventRegisterAny
 *
 * https://libvirt.org/html/libvirt-libvirt-domain.html#virConnectDomainEventRegisterAny
 *
 * Note that only one callback is registered for all Domain Events.
 */
func New() (*Hypervisor, error) {
	conn, err := libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return nil, err
	}
	var hv *Hypervisor = new(Hypervisor)
	hv.conn = conn
	hv.vm_event_ch = nil
	hv.system_info_ch = nil
	hv.callback_id = -1
	return hv, nil
}

func (hv *Hypervisor) Shutdown() {
	logger.Log("Deregistering event handlers")
	hv.Stop_listening()
	logger.Log("Closing libvirt connection")
	_, err := hv.conn.Close();
	if (err != nil) {
		logger.Log(err.Error())
	}
}

/* get basic information about a Domain */
func get_domain_info(d *libvirt.Domain) (string, string, openapi.Vmrunstate, error) {
	var (
		name string
		uuid string
		reason int
		state libvirt.DomainState
		enum_state openapi.Vmrunstate
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
	logger.Log("get_domain_info: state %d, reason %d", state, reason)
	switch (state) {
	//case libvirt.DOMAIN_NOSTATE: /* ?XXX? */
	case libvirt.DOMAIN_RUNNING:
		fallthrough
	case libvirt.DOMAIN_BLOCKED: /* ?XXX? */
		enum_state = openapi.RUNSTATE_RUNNING
	case libvirt.DOMAIN_PAUSED:
		switch (reason) {
		case int(libvirt.DOMAIN_PAUSED_MIGRATION): /* paused for offline migration */
			enum_state = openapi.RUNSTATE_MIGRATING
		case int(libvirt.DOMAIN_PAUSED_SHUTTING_DOWN):
			enum_state = openapi.RUNSTATE_TERMINATING
		case int(libvirt.DOMAIN_PAUSED_CRASHED):
			enum_state = openapi.RUNSTATE_CRASHED
		case int(libvirt.DOMAIN_PAUSED_STARTING_UP):
			enum_state = openapi.RUNSTATE_STARTUP
		default:
			enum_state = openapi.RUNSTATE_PAUSED
		}
	case libvirt.DOMAIN_SHUTDOWN:
		enum_state = openapi.RUNSTATE_TERMINATING
	case libvirt.DOMAIN_SHUTOFF:
		switch (reason) {
		case int(libvirt.DOMAIN_SHUTOFF_CRASHED):
			enum_state = openapi.RUNSTATE_CRASHED
		case int(libvirt.DOMAIN_SHUTOFF_MIGRATED):
			/* XXX what to do when domain goes away from this host? XXX */
		default:
			enum_state = openapi.RUNSTATE_POWEROFF
		}
	case libvirt.DOMAIN_CRASHED:
		enum_state = openapi.RUNSTATE_CRASHED
	case libvirt.DOMAIN_PMSUSPENDED:
		enum_state = openapi.RUNSTATE_PMSUSPENDED
	default:
		logger.Log("Unhandled state %d, reason %d", state, reason)
	}
out:
	return name, uuid, enum_state, err
}

/*
 * Regularly fetch all system information (host info and vms info), and send it via system_info_ch.
 */
func (hv *Hypervisor) system_info_loop(seconds int) error {
	var (
		si SystemInfo
		err error
		ticker *time.Ticker
	)
	ticker = time.NewTicker(time.Duration(seconds) * time.Second)
	defer ticker.Stop()

	err = hv.get_system_info(&si)
	if (err != nil) {
		logger.Log(err.Error())
	} else {
		hv.system_info_ch <- si
	}
	for range ticker.C {
		err = hv.get_system_info(&si)
		if (err != nil) {
			logger.Log(err.Error())
			continue
		}
		hv.system_info_ch <- si
	}
	logger.Log("system_info_loop Exiting!")
	return nil
}

/*
 * Start listening for domain events and collecting system information.
 * Sets the callback_id, vm_event_ch and system_info_ch fields of the Hypervisor struct.
 * Collects system information every "seconds" seconds.
 */
func (hv *Hypervisor) Start_listening(seconds int) error {
	lifecycleCb := func(_ *libvirt.Connect, d *libvirt.Domain, e *libvirt.DomainEventLifecycle) {
		var (
			name, uuid string
			state openapi.Vmrunstate
			err error
		)
		name, uuid, state, err = get_domain_info(d)
		if (err != nil) {
			if e.Event == libvirt.DOMAIN_EVENT_UNDEFINED {
				/* XXX handle this XXX */
			} else {
				logger.Log(err.Error())
			}
		}
		logger.Log("[VmEvent] %s/%s: %v state: %d", name, uuid, e, state)
		hv.vm_event_ch <- VmEvent{ Uuid: uuid, State: state, Ts: time.Now().UTC().UnixMilli() }
	}
	var err error
	hv.vm_event_ch = make(chan VmEvent, 64)
	hv.system_info_ch = make(chan SystemInfo, 64)
	hv.callback_id, err = hv.conn.DomainEventLifecycleRegister(nil, lifecycleCb)
	if (err != nil) {
		return err
	}
	go hv.system_info_loop(seconds)
	return nil
}

func (hv *Hypervisor) Stop_listening() {
	if (hv.callback_id < 0) {
		/* already stopped */
		logger.Log("StopListening(): already stopped.")
		return
	}
	logger.Log("StopListening(): deregister libvirt CallbackId: %d", hv.callback_id)
	var err error = hv.conn.DomainEventDeregister(hv.callback_id)
	if (err != nil) {
		logger.Log(err.Error())
	}
	close(hv.vm_event_ch)
	close(hv.system_info_ch)
	hv.vm_event_ch = nil /* assume runtime will garbage collect */
	hv.callback_id = -1
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

func (hv *Hypervisor) get_system_info(si *SystemInfo) error {
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
		free_memory uint64
		total_memory_capacity uint64
		total_memory_used uint64
		total_cpus uint32
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
	host.Def.Tscfreq = int64(caps.Host.CPU.Counter.Frequency)
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
			host.Def.Sysinfo.Version = e.Value
		case "date":
			host.Def.Sysinfo.Date = e.Value
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
		vm.Name, vm.Uuid, vm.Runinfo.Runstate, err = get_domain_info(&d)
		if (err != nil) {
			logger.Log("could not get_domain_info: %s", err.Error())
			continue
		}
		err = getDomainStats(&d, &vm)
		vm.Runinfo.Host = host.Uuid
		total_memory_capacity += vm.Memory_capacity
		total_memory_used += vm.Memory_used
		total_cpus += uint32(vm.Cpus) /* XXX maybe we should use Topology and exclude threads? XXX */
		//cpuPercent := float64(delta) / (15.0 * float64(cpus) * 1e9) * 100.0
		vm.Ts = ts
		vmstats = append(vmstats, vm)
	}
	/* now calculate host resources */
	host.Resources.Memory.Total = int32(info.Memory / KiB) /* info returns memory in KiB, translate to MiB */
	free_memory, err = hv.conn.GetFreeMemory()
	if (err != nil) {
		goto out
	}
	/* XXX no overcommit is currently implemented XXX */
	host.Resources.Memory.Free = int32(free_memory / MiB) /* this returns in bytes, translate to MiB */
	host.Resources.Memory.Used = host.Resources.Memory.Total - host.Resources.Memory.Free
	host.Resources.Memory.Reservedos = 0  /* XXX need to implement XXX */
	host.Resources.Memory.Reservedvms = int32(total_memory_capacity)
	host.Resources.Memory.Usedvms = int32(total_memory_used)
	host.Resources.Memory.Availablevms = host.Resources.Memory.Total -
		host.Resources.Memory.Reservedos - host.Resources.Memory.Reservedvms

	/* like VMWare, we calculate the total Mhz as (total_cores * frequency) (excluding threads) */
	/* XXX no overcommit is currently implemented XXX */
	host.Resources.Cpu.Total = int32(uint(info.Nodes * info.Sockets * info.Cores) * info.MHz)
	host.Resources.Cpu.Used = 0 /* XXX */
	host.Resources.Cpu.Free = host.Resources.Cpu.Total - host.Resources.Cpu.Used
	host.Resources.Cpu.Reservedos = 0  /* XXX */
	host.Resources.Cpu.Reservedvms = int32(total_cpus * uint32(info.MHz))
	host.Resources.Cpu.Usedvms = 0 /* XXX */
	host.Resources.Cpu.Availablevms = host.Resources.Cpu.Free - host.Resources.Cpu.Reservedvms

out:
	si.Host = host
	si.Vm_stats = vmstats
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
		Vm.Cpu_time = cpustat[0].CpuTime
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
		vm.Cpu_time = info.CpuTime
		vm.Memory_capacity = info.Memory / KiB /* convert from KiB to MiB */
		memstat, err = d.MemoryStats(20, 0)
		if (err != nil) {
			return err
		}
		for _, stat := range memstat {
			if (libvirt.DomainMemoryStatTags(stat.Tag) == libvirt.DOMAIN_MEMORY_STAT_RSS) {
				vm.Memory_used = stat.Val / KiB /* convert from KiB to MiB */
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
				vm.Disk_capacity += blockinfo.Capacity / MiB
				vm.Disk_allocation += blockinfo.Allocation / MiB
				vm.Disk_physical += blockinfo.Physical / MiB
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
					vm.Net_rx += netstat.RxBytes
				}
				if (netstat.TxBytesSet) {
					vm.Net_tx += netstat.TxBytes
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
	return hv.vm_event_ch
}

/* Return the systemInfo Events Channel */
func (hv *Hypervisor)GetSystemInfoCh() (chan SystemInfo) {
	return hv.system_info_ch
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
