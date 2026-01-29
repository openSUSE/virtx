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
	"errors"
	"os"
	"bytes"
	"bufio"
	"strings"
	"strconv"

	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/inventory"

	. "suse.com/virtx/pkg/constants"
)

/* immutable host fields of SystemInfo */
type SystemInfoImm struct {
	caps libvirtxml.Caps
	/*
	 * XXX Mhz needs to be fetched manually because libvirt does a bad job of it.
	 * libvirt reads /proc/cpuinfo, which just shows current Mhz, not max Mhz.
	 * So any power state change, frequency change may change results. Oh my.
	 *
	 * We need to call nodeinfo specifically anyway, for the total memory size,
	 * and also as a fallback frequency if max_freq is not available/readable.
	 * So we keep using nodeinfo and we keep it here, overwriting the MHz value.
	 */
	info *libvirt.NodeInfo
	/* from /proc/meminfo in KiB */
	hp_size uint64
	hp_total uint64
	/* from Capabilities, smbios */
	bios_version string
	bios_date string
}

type SystemInfoVms map[string]SystemInfoVm

type SystemInfoVm struct {
	inventory.Vmdata            /* embedded Vm data to be trasmitted externally */
	stats openapi.Vmstats       /* the Vm statistics collected on this host */

	/* overall internal counters for Vm Stats */
	hp bool                     /* hugepages used */
	cpu_time uint64             /* Total cpu time consumed in nanoseconds from libvirt.DomainCPUStats.CpuTime */
	net_rx int64                /* Net Rx bytes */
	net_tx int64                /* Net Tx bytes */
}

type SystemInfo struct {
	imm SystemInfoImm
	Host openapi.Host /* host data to be transmitted externally */
	Vms SystemInfoVms /* vms data, including external and internal data not for transmission */

	/* overall internal counters for host stats */
	cpu_idle_ns uint64
	cpu_kernel_ns uint64
	cpu_iowait_ns uint64
	cpu_user_ns uint64
}

/*
 * Regularly fetch all system information (host info and vms info), and send it via system_info_ch.
 */
func system_info_loop(seconds int) error {
	var (
		si SystemInfo
		err error
		ticker *time.Ticker
		libvirt_err libvirt.Error
		ok bool
	)
	logger.Debug("system_info_loop starting...")
	defer logger.Debug("system_info_loop exit")
	ticker = time.NewTicker(time.Duration(seconds) * time.Second)
	defer ticker.Stop()

	si, err = get_system_info()
	if (err != nil) {
		logger.Log("system_info_loop: failed to get_system_info: %s", err.Error())
		libvirt_err, ok = err.(libvirt.Error)
		if (ok && libvirt_err.Level >= libvirt.ERR_ERROR) {
			return err
		}
	}
	hv.m.Lock()
	hv.uuid = si.Host.Uuid
	hv.cpuarch = si.Host.Def.Cpuarch
	check_vmreg(hv.uuid, &si)
	hv.m.Unlock()

	/* this first info is missing vm cpu stats and host cpu stats */
	hv.system_info_ch <- si

	for range ticker.C {
		si, err = get_system_info()
		if (err != nil) {
			logger.Log("system_info_loop: failed to get_system_info: %s", err.Error())
			libvirt_err, ok = err.(libvirt.Error)
			if (ok && libvirt_err.Level >= libvirt.ERR_ERROR) {
				return err
			}
			continue
		}
		hv.system_info_ch <- si
	}
	return nil
}

func get_system_info() (SystemInfo, error) {
	hv.m.Lock()
	defer hv.m.Unlock()
	var (
		host openapi.Host
		si SystemInfo
		vms SystemInfoVms = make(SystemInfoVms)
		err error
		caps *libvirtxml.Caps
		info *libvirt.NodeInfo
	)
	var (
		doms []libvirt.Domain
		d libvirt.Domain
	)
	var (
		/* host memory and hp total free */
		memory_free uint64
		hp_free uint64

		/* for normal memory backed domains */
		total_memory_capacity uint64
		total_memory_used uint64

		/* for hugetlbfs backed domains */
		total_hp_capacity uint64
		/* total_hp_used is not needed, because all backing pages are "in use" */

		total_vcpus_mhz uint32
		total_cpus_used_percent int32
		cpustats *libvirt.NodeCPUStats
	)

	if (hv.si == nil) {
		err = get_system_info_immutable(&si.imm)
		if (err != nil) {
			goto out
		}
	} else {
		si.imm = hv.si.imm
	}
	/* for quick access */
	caps = &si.imm.caps
	info = si.imm.info

	/* 1. set the general host information */
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
	host.Def.Sysinfo.Version = si.imm.bios_version
	host.Def.Sysinfo.Date = si.imm.bios_date
	host.State = openapi.HOST_ACTIVE
	host.Ts = time.Now().UTC().UnixMilli()

	/*
	 * 2. get information about all the domains, so that we can calculate
	 *    host resources later.
	 */
	doms, err = hv.conn.ListAllDomains(libvirt.CONNECT_LIST_DOMAINS_PERSISTENT)
	if (err != nil) {
		goto out
	}
	defer freeDomains(doms)

	for _, d = range doms {
		var (
			vm SystemInfoVm
			oldvm SystemInfoVm
			present bool
		)
		vm.Name, vm.Uuid, vm.Runinfo.Runstate, err = get_domain_info(&d)
		if (err != nil) {
			logger.Log("could not get_domain_info: %s", err.Error())
			continue
		}
		if (hv.si != nil) {
			oldvm, present = hv.si.Vms[vm.Uuid]
		}
		vm.Runinfo.Host = host.Uuid
		vm.Ts = host.Ts
		if (present) {
			err = get_domain_stats(&d, &vm, &oldvm, &si.imm)
		} else {
			err = get_domain_stats(&d, &vm, nil, &si.imm)
		}
		if (vm.hp) {
			total_hp_capacity += uint64(vm.stats.MemoryCapacity)
		} else {
			total_memory_capacity += uint64(vm.stats.MemoryCapacity)
		}
		/*
		 * we store the qemu RSS size into vm.Stats.MemoryUsed for hugepages too,
		 * since for hugetlbfs all the backing hugepages will be "used" on the host.
		 * The total memory used on the host will be HP capacity + memory used.
		 */
		total_memory_used += uint64(vm.stats.MemoryUsed)
		total_vcpus_mhz += uint32(vm.Vcpus) * uint32(info.MHz) /* equal to Topology Sockets * Cores, since we do not use threads */
		total_cpus_used_percent += vm.stats.CpuUtilization
		vms[vm.Uuid] = vm
	}
	/* now calculate host resources */
	memory_free, err = hv.conn.GetFreeMemory()
	if (err != nil) {
		goto out
	}
	hp_free, err = get_meminfo("HugePages_Free")
	if (err != nil) {
		goto out
	}
	hp_free *= si.imm.hp_size

	cpustats, err = hv.conn.GetCPUStats(-1, 0)
	if (err != nil) {
		goto out
	}
	/* Hugepages */
	host.Resources.Hp.Total = int32(si.imm.hp_total / KiB) /* /proc/meminfo is in KiB, translate to MiB */
	host.Resources.Hp.Free = int32(hp_free / KiB)       /* /proc/meminfo is in KiB, translate to MiB */

	/* Normal Memory (4k pages). Info is in KiB, so convert to MiB and subtract the memory stolen by Hp */
	host.Resources.Memory.Total = int32(info.Memory / KiB) - host.Resources.Hp.Total
	host.Resources.Memory.Free = int32(memory_free / MiB) /* this returns in bytes, translate to MiB */

	/* HP derived calculations */
	host.Resources.Hp.Used = host.Resources.Hp.Total - host.Resources.Hp.Free
	host.Resources.Hp.Reservedvms = int32(total_hp_capacity)
	host.Resources.Hp.Usedvms = int32(total_hp_capacity)
	host.Resources.Hp.Usedos = host.Resources.Hp.Used - host.Resources.Hp.Usedvms
	host.Resources.Hp.Availablevms = host.Resources.Hp.Total - host.Resources.Hp.Reservedvms - host.Resources.Hp.Usedos

	/* Normal Memory derived calculations */
	host.Resources.Memory.Used = host.Resources.Memory.Total - host.Resources.Memory.Free
	host.Resources.Memory.Reservedvms = int32(total_memory_capacity)
	host.Resources.Memory.Usedvms = int32(total_memory_used)
	host.Resources.Memory.Usedos = host.Resources.Memory.Used - host.Resources.Memory.Usedvms
	host.Resources.Memory.Availablevms = host.Resources.Memory.Total - host.Resources.Memory.Reservedvms - host.Resources.Memory.Usedos

	/* CPU */
	host.Resources.Cpu.Total = int32(uint(info.Nodes * info.Sockets * info.Cores * info.Threads) * info.MHz)
	host.Resources.Cpu.Reservedvms = int32((float64(total_vcpus_mhz) / 100.0) * hv.vcpu_load_factor)
	si.cpu_idle_ns = cpustats.Idle
	si.cpu_kernel_ns = cpustats.Kernel
	si.cpu_iowait_ns = cpustats.Iowait
	si.cpu_user_ns = cpustats.User /* unfortunately this includes guest time */

	/* some of the data we can only calculate as comparison from the previous measurement */
	if (hv.si != nil) {
		interval := float64(host.Ts - hv.si.Host.Ts)
		if (interval <= 0.0) {
			logger.Log("get_system_info: host timestamps not in order?")
		} else {
			var delta float64
			/* idle counters are completely unreliable, behavior depends on hw cpu vendor, model etc */
			//delta = float64(Counter_delta_uint64(si.cpu_idle_ns, old.cpu_idle_ns))
			//host.Resources.Cpu.Free = int32(delta / (interval * 1000000) * float64(info.MHz) / float64(info.Threads))
			delta = float64(Counter_delta_uint64(si.cpu_kernel_ns, hv.si.cpu_kernel_ns))
			logger.Debug("gsi: cpu_kernel_ns delta = %f", delta)
			host.Resources.Cpu.Used = int32(delta / (interval * 1000000) * float64(info.MHz))

			delta = float64(Counter_delta_uint64(si.cpu_iowait_ns, hv.si.cpu_iowait_ns))
			logger.Debug("gsi: cpu_iowait_ns delta = %f", delta)
			host.Resources.Cpu.Used += int32(delta / (interval * 1000000) * float64(info.MHz))

			delta = float64(Counter_delta_uint64(si.cpu_user_ns, hv.si.cpu_user_ns))
			logger.Debug("gsi: cpu_user_ns delta = %f", delta)
			host.Resources.Cpu.Used += int32(delta / (interval * 1000000) * float64(info.MHz))

			logger.Debug("gsi: Cpu.Used = %d", host.Resources.Cpu.Used)

			host.Resources.Cpu.Free = host.Resources.Cpu.Total - host.Resources.Cpu.Used
			logger.Debug("gsi: Cpu.Free = %d", host.Resources.Cpu.Free)

			host.Resources.Cpu.Usedvms = total_cpus_used_percent * int32(info.MHz) / 100
			logger.Debug("gsi: Cpu.Usedvms = %d", host.Resources.Cpu.Usedvms)

			host.Resources.Cpu.Usedos = host.Resources.Cpu.Used - host.Resources.Cpu.Usedvms
			logger.Debug("gsi: Cpu.Usedos = %d", host.Resources.Cpu.Usedos)

			host.Resources.Cpu.Availablevms = host.Resources.Cpu.Total - host.Resources.Cpu.Reservedvms - host.Resources.Cpu.Usedos
			logger.Debug("gsi: Cpu.Availablevms = %d", host.Resources.Cpu.Availablevms)
		}
	}
	si.Host = host
	si.Vms = vms
	if (hv.si == nil) {
		hv.si = new(SystemInfo)
	}
	*hv.si = si
	delete_ghosts(si.Vms, host.Ts)
out:
	return si, err
}

/*
 * we may miss the DELETE event, and then we are left with ghosts of old vms
 * in the inventory.
 * To address this, go over the local VmsInventory and compare it with the
 * inventory returned by libvirt, removing items unknown to libvirt.
 */
func delete_ghosts(vms SystemInfoVms, ts int64) {
	var (
		idata inventory.Hostdata
		ikey string
		present bool
		err error
	)
	idata, err = inventory.Get_hostdata(hv.uuid)
	if (err != nil) {
		return /* host not in inventory yet, ignore */
	}
	for ikey = range idata.Vms {
		_, present = vms[ikey]
		if (!present) {
			logger.Log("delete_ghosts: RUNSTATE_DELETED %s", ikey)
			hv.vm_event_ch <- inventory.VmEvent{ Uuid: ikey, Host: hv.uuid, State: openapi.RUNSTATE_DELETED, Ts: ts }
		}
	}
}

/*
 * this information does not change after the first fetch,
 * and is reused for all subsequent get_system_info calls
 */
func get_system_info_immutable(imm *SystemInfoImm) error {
	var (
		data string
		smbios xmlSysInfo
		raw []byte
		mhz int
		err error
	)
	data, err = hv.conn.GetCapabilities()
	if (err != nil) {
		return err
	}
	err = imm.caps.Unmarshal(data)
	if (err != nil) {
		return err
	}
	data, err = hv.conn.GetSysinfo(0)
	if (err != nil) {
		return err
	}
	/* workaround for libvirtxml go bindings bug/missing feature. Should behave like libvirtxml.Caps() instead. */
	err = xml.Unmarshal([]byte(data), &smbios)
	if (err != nil) {
		return err
	}
	for _, e := range smbios.BIOS.Entries {
		switch e.Name {
		case "version":
			imm.bios_version = e.Value
		case "date":
			imm.bios_date = e.Value
		}
	}
	/* we still need nodeinfo for the memory size and fallback frequency */
	imm.info, err = hv.conn.GetNodeInfo()
	if (err != nil) {
		return err
	}
	/* get the hugepage size and total physical mem in KB */
	imm.hp_size, err = get_meminfo("Hugepagesize")
	if (err != nil) {
		return errors.New("failed to read Hugepagesize: " + err.Error())
	}
	var hp_total uint64
	hp_total, err = get_meminfo("HugePages_Total")
	if (err != nil) {
		return errors.New("failed to read HugePages_Total: " + err.Error())
	}
	/* get the total physical memory (KiB) used by default-sized hugepages */
	imm.hp_total = hp_total * imm.hp_size

	/* now deal with calculating the node Max CPU Frequency. Failures are not fatal. */
	defer func() {
		if (err != nil) {
			/* emit warning, we will not override libvirt MHz */
			logger.Log("could not read CPU max frequency: %s", err.Error())
			logger.Log("fallback to libvirt MHz, MHz calculations will be unreliable")
		}
	}()
	raw, err = os.ReadFile(max_freq_path)
	if (err != nil) {
		return nil
	}
	mhz, err = strconv.Atoi(strings.TrimSpace(string(raw)))
	if (err != nil) {
		return nil
	}
	imm.info.MHz = uint(mhz / 1000) /* input from sysfs is measured in Hz */
	return nil
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

/* get the specific key out of /proc/meminfo in KB */
func get_meminfo(key string) (uint64, error) {
	var (
		err error
		data []byte
		scanner *bufio.Scanner
	)
	data, err = os.ReadFile("/proc/meminfo")
	if (err != nil) {
		return 0, err
	}
	scanner = bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if (fields[0] == key + ":") {
			return strconv.ParseUint(fields[1], 10, 64)
		}
	}
	return 0, errors.New("key not found")
}


type xmlDisk struct {
	Device string `xml:"device,attr"`
	Source struct {
		File string `xml:"file,attr"`
		Dev string `xml:"dev,attr"`
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
	MemoryBacking *libvirtxml.DomainMemoryBacking `xml:"memoryBacking"`
	Devices struct {
		Disks []xmlDisk `xml:"disk"`
		Interfaces []xmlInterface `xml:"interface"`
	} `xml:"devices"`
}

func get_domain_stats(d *libvirt.Domain, vm *SystemInfoVm, old *SystemInfoVm, imm *SystemInfoImm) error {
	var err error
	{
		// Retrieve the necessary info from domain's XML description
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
		if (xd.MemoryBacking != nil) {
			vm.hp = true
		}
		for _, disk := range xd.Devices.Disks {
			var path string
			if (disk.Device != "disk" && disk.Device != "lun") {
				continue
			}
			if (disk.Source.File != "") {
				path = disk.Source.File
			} else if (disk.Source.Dev != "") {
				path = disk.Source.Dev
			} else {
				continue
			}
			var blockinfo *libvirt.DomainBlockInfo
			blockinfo, err = d.GetBlockInfo(path, 0)
			if (err != nil) {
				return err
			}
			vm.stats.DiskCapacity += int64(blockinfo.Capacity / MiB)
			vm.stats.DiskAllocation += int64(blockinfo.Allocation / MiB)
			vm.stats.DiskPhysical += int64(blockinfo.Physical / MiB)
		}
		for _, net := range xd.Devices.Interfaces {
			if (net.Target.Dev != "") {
				var netstat *libvirt.DomainInterfaceStats
				netstat, err = d.InterfaceStats(net.Target.Dev)
				if (err != nil) {
					return err
				}
				if (netstat.RxBytesSet) {
					vm.net_rx += netstat.RxBytes
				}
				if (netstat.TxBytesSet) {
					vm.net_tx += netstat.TxBytes
				}
			}
			if (len(net.Vlan.Tags) > 0) {
				vm.Vlanid = int16(net.Vlan.Tags[0].Id) /* XXX only one VlandID for each VM is recognized XXX */
			}
		}
	}
	{
		/* now retrieve the necessary info from GetInfo() */
		var info *libvirt.DomainInfo
		info, err = d.GetInfo()
		if (err != nil) {
			return err
		}
		vm.Vcpus = int16(info.NrVirtCpu)
		vm.cpu_time = info.CpuTime
		vm.stats.MemoryCapacity = int64(info.Memory / KiB) /* convert from KiB to MiB */
		/*
		 * we store the RSS size of qemu into MemoryUsed for hugetlbfs too.
		 * This is to account for the amount of normal memory (70 MiB or so) used even
		 * backing the VM with hugepages from hugetlbfs.
		 */
		var memstat []libvirt.DomainMemoryStat
		memstat, err = d.MemoryStats(20, 0)
		if (err != nil) {
			return err
		}
		for _, stat := range memstat {
			if (libvirt.DomainMemoryStatTags(stat.Tag) == libvirt.DOMAIN_MEMORY_STAT_RSS) {
				vm.stats.MemoryUsed = int64(stat.Val / KiB) /* convert from KiB to MiB */
				break
			}
		}
	}
	if (old != nil) {
		/* finally, calculate deltas from previous Vm cpu and net stats */
		var udelta uint64 = Counter_delta_uint64(vm.cpu_time, old.cpu_time)
		logger.Debug("gds: udelta = %d, (vm.Ts - old.Ts) = %d, Vcpus = %d", udelta, (vm.Ts - old.Ts), vm.Vcpus)

		if (udelta > 0 && (vm.Ts - old.Ts) > 0 && vm.Vcpus > 0) {
			vm.stats.CpuUtilization = int32((udelta * 100) / (uint64(vm.Ts - old.Ts) * 1000000))
		}
		logger.Debug("gds: CpuUtilization = %d", vm.stats.CpuUtilization)
		vm.stats.MhzUsed = vm.stats.CpuUtilization * int32(imm.info.MHz) / 100

		var delta int64 = Counter_delta_int64(vm.net_rx, old.net_rx)
		logger.Debug("gds: net_rx delta = %d", delta)

		if (delta > 0 && (vm.Ts - old.Ts) > 0) {
			vm.stats.NetRxBw = int32((delta * 1000) / ((vm.Ts - old.Ts) * KiB))
		}
		logger.Debug("gds: NetRxBw = %d", vm.stats.NetRxBw)

		delta = Counter_delta_int64(vm.net_tx, old.net_tx)
		logger.Debug("gds: net_tx delta = %d", delta)

		if (delta > 0 && (vm.Ts - old.Ts) > 0) {
			vm.stats.NetTxBw = int32((delta * 1000) / ((vm.Ts - old.Ts) * KiB))
		}
		logger.Debug("gds: NetTxBw = %d", vm.stats.NetTxBw)
	}
	return nil
}

func freeDomains(doms []libvirt.Domain) {
	for _, d := range doms {
		d.Free()
	}
}

func Get_Vmstats(uuid string) (openapi.Vmstats, error) {
	hv.m.RLock()
	defer hv.m.RUnlock()
	var (
		vm SystemInfoVm
		present bool
	)
	if (hv.si == nil) {
		return openapi.Vmstats{}, errors.New("SystemInfo not available")
	}
	vm, present = hv.si.Vms[uuid]
	if (!present) {
		return openapi.Vmstats{}, errors.New("could not find vm")
	}
	return vm.stats, nil
}
