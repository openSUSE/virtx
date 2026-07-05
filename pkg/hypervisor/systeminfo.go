/*
 * Copyright (c) 2024-2026 SUSE LLC
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
	"path/filepath"
	"bytes"
	"bufio"
	"strings"
	"strconv"

	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/lockman"
	"suse.com/virtx/pkg/ts"
	"suse.com/virtx/pkg/machine"

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
	/* from /etc/os-release or /usr/lib/os-release */
	os_id string
	os_version string
	/* physical/bond NICs (not enslaved, not virtual), discovered once at startup */
	nic_ifaces []string
	nic_capacity int32 /* total Rx/Tx link capacity in KiB/s across all qualifying NICs */
}

type SystemInfoVms map[string]SystemInfoVm

type SystemInfoVm struct {
	inventory.VmInfo            /* embedded Vm data to be trasmitted to Serf */
	stats openapi.Vmstats       /* the Vm statistics collected on this host */

	/* overall internal counters for Vm Stats */
	hp bool                      /* hugepages used */
	cpu_time uint64              /* Total cpu time consumed in nanoseconds from libvirt.DomainCPUStats.CpuTime */
	disk_rd, disk_wr int64       /* Disk Read/Written bytes */
	disk_rd_io, disk_wr_io int64 /* Disk Read/Write IO operations */
	net_rx, net_tx int64         /* Net Rx/Tx bytes */
}

type SystemInfoHost struct {
	inventory.HostInfo          /* embedded Host data to be trasmitted to Serf */
	stats openapi.Hoststats     /* the Host resource statistics collected on this host */
}

type SystemInfo struct {
	imm SystemInfoImm
	Host SystemInfoHost /* HostInfo to transmit plus internal data not for transmission */
	Vms SystemInfoVms /* set of VmInfo to transmit plus internal data not for transmission */

	/* overall internal counters for host stats */
	cpu_kernel_ns uint64
	cpu_user_ns uint64
	nic_rx uint64  /* cumulative Rx bytes across all qualifying physical NICs */
	nic_tx uint64  /* cumulative Tx bytes across all qualifying physical NICs */
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

	si, err = system_info_get()
	if (err != nil) {
		logger.Log("system_info_loop: failed to system_info_get: %s", err.Error())
		libvirt_err, ok = err.(libvirt.Error)
		if (ok && libvirt_err.Level >= libvirt.ERR_ERROR) {
			return err
		}
	}
	check_vmreg(machine.Uuid(), &si)
	set_system_info_loop_done()

	/* this first info is missing vm cpu stats and host cpu stats */
	hv.system_info_ch <- si
	delete_ghosts(si.Vms, si.Host.Ts)

	for range ticker.C {
		si, err = system_info_get()
		if (err != nil) {
			logger.Log("system_info_loop: failed to system_info_get: %s", err.Error())
			libvirt_err, ok = err.(libvirt.Error)
			if (ok && libvirt_err.Level >= libvirt.ERR_ERROR) {
				return err
			}
			continue
		}
		hv.system_info_ch <- si
		delete_ghosts(si.Vms, si.Host.Ts)
	}
	return nil
}

func system_info_get() (SystemInfo, error) {
	hv.m.Lock()
	defer hv.m.Unlock()
	var (
		si SystemInfo
		vms SystemInfoVms = make(SystemInfoVms)
		err error
		caps *libvirtxml.Caps
		stats *openapi.Hoststats
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
		total_hp_used uint64

		total_vcpus_mhz uint32
		total_vcpus_mhz_used int32
		total_vm_net_rx_bw int32  /* aggregate VM net Rx KiB/s */
		total_vm_net_tx_bw int32  /* aggregate VM net Tx KiB/s */
		total_vm_disk_rd_bw int32 /* aggregate VM disk read KiB/s (NFS/iSCSI Rx) */
		total_vm_disk_wr_bw int32 /* aggregate VM disk write KiB/s (NFS/iSCSI Tx) */
		cpustats *libvirt.NodeCPUStats
	)

	if (hv.si == nil) {
		err = system_info_get_immutable(&si.imm)
		if (err != nil) {
			goto out
		}
		/***** SET THE HYPERVISOR UUID AND ARCHITECTURE *****/
		machine.Set_uuid(si.imm.caps.Host.UUID)
		machine.Set_arch(si.imm.caps.Host.CPU.Arch)
		/****************************************************/
	} else {
		si.imm = hv.si.imm
	}
	/* for quick access */
	caps = &si.imm.caps
	info = si.imm.info

	/* 1. set the general host information */
	si.Host.Uuid = caps.Host.UUID
	si.Host.Name, err = hv.conn.GetHostname()
	if (err != nil) {
		goto out
	}
	si.Host.Cpuarch.Arch = caps.Host.CPU.Arch
	si.Host.Cpuarch.Vendor = caps.Host.CPU.Vendor
	si.Host.Cpudef.Model = caps.Host.CPU.Model
	si.Host.Cpudef.Nodes = int16(info.Nodes)
	si.Host.Cpudef.Sockets = int16(info.Sockets)
	si.Host.Cpudef.Cores = int16(info.Cores)
	si.Host.Cpudef.Threads = int16(info.Threads)
	si.Host.Cstate = openapi.CSTATE_ACTIVE
	/* Memoryavailable, Hpavailable are set below once calculated */
	si.Host.Osid = si.imm.os_id
	si.Host.Osv = si.imm.os_version
	si.Host.Ts = ts.Now()
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
		vm.VmInfo.VmEvent, vm.Name, err = get_domain_info(&d)
		if (err != nil) {
			logger.Log("could not get_domain_info: %s", err.Error())
			continue
		}
		vm.Ts = si.Host.Ts
		if (hv.si != nil) {
			oldvm, present = hv.si.Vms[vm.Uuid]
		}
		if (present) {
			err = get_domain_stats(&d, &vm, &oldvm, &si.imm)
		} else {
			err = get_domain_stats(&d, &vm, nil, &si.imm)
		}
		if (err != nil) {
			logger.Log("could not get_domain_stats: %s", err)
			continue
		}
		if (vm.hp) {
			total_hp_capacity += uint64(vm.stats.MemoryCapacity)
			/*
			 * to calculate total HPG used by VM we check the runstate.
			 * If the VM is still inactive, we say hugepages are not allocated
			 * and if it's active, we say they are.
			 *
			 * We consider the border cases of STARTUP and TERMINATING.
			 * STARTUP is excluded: we say hugepages are not allocated.
			 * This can cause an undercount of hp.Usedvms for a brief moment,
			 * which will be captured by a transient increase of hp.Usedother,
			 * until the VM transitions to RUNNING.
			 *
			 * TERMINATING can be long, so we do count HPGs as allocated.
			 * This can overcount hp.Usedvms for a brief moment, so this can
			 * lead to a negative hp.Usedother.
			 */
			switch (vm.Runstate) {
			case openapi.RUNSTATE_NONE, openapi.RUNSTATE_DELETED, openapi.RUNSTATE_POWEROFF, openapi.RUNSTATE_STARTUP:
			default:
				total_hp_used += uint64(vm.stats.MemoryCapacity)
			}

		} else {
			total_memory_capacity += uint64(vm.stats.MemoryCapacity)
		}
		/*
		 * we store the qemu RSS size into vm.Stats.MemoryUsed for hugepages too,
		 * since for hugetlbfs all the backing hugepages will be "used" on the host.
		 * The total memory used on the host will be HP capacity + memory used.
		 */
		total_memory_used += uint64(vm.stats.MemoryUsed)
		total_vcpus_mhz += uint32(vm.Vcpus) * uint32(info.MHz)
		total_vcpus_mhz_used += vm.stats.MhzUsed
		total_vm_net_rx_bw += vm.stats.NetRxBw
		total_vm_net_tx_bw += vm.stats.NetTxBw
		total_vm_disk_rd_bw += vm.stats.DiskRdBw
		total_vm_disk_wr_bw += vm.stats.DiskWrBw
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
	stats = &si.Host.stats
	/* Hugepages */
	stats.Hp.Total = int32(si.imm.hp_total / KiB) /* /proc/meminfo is in KiB, translate to MiB */
	stats.Hp.Free = int32(hp_free / KiB)       /* /proc/meminfo is in KiB, translate to MiB */

	/* Normal Memory (4k pages). Info is in KiB, so convert to MiB and subtract the memory stolen by Hp */
	stats.Memory.Total = int32(info.Memory / KiB) - stats.Hp.Total
	stats.Memory.Free = int32(memory_free / MiB) /* this returns in bytes, translate to MiB */

	/* HP derived calculations */
	stats.Hp.Used = stats.Hp.Total - stats.Hp.Free
	stats.Hp.Reservedvms = int32(total_hp_capacity)
	stats.Hp.Usedvms = int32(total_hp_used)
	/* Usedother could briefly go negative in rare cases when QEMU releases HPG in TERMINATING state */
	stats.Hp.Usedother = stats.Hp.Used - stats.Hp.Usedvms
	stats.Hp.Availablevms = stats.Hp.Total - stats.Hp.Reservedvms - stats.Hp.Usedother
	/* Set the HostInfo HP available field */
	si.Host.Hpavailable = stats.Hp.Availablevms

	/* Normal Memory derived calculations */
	stats.Memory.Used = stats.Memory.Total - stats.Memory.Free
	stats.Memory.Reservedvms = int32(total_memory_capacity)
	stats.Memory.Usedvms = int32(total_memory_used)
	stats.Memory.Usedother = stats.Memory.Used - stats.Memory.Usedvms
	stats.Memory.Availablevms = stats.Memory.Total - stats.Memory.Reservedvms - stats.Memory.Usedother
	/* Set the HostInfo Memory available field */
	si.Host.Memoryavailable = stats.Memory.Availablevms

	/* CPU */
	stats.Cpu.Total = int32(uint(info.Nodes * info.Sockets * info.Cores * info.Threads) * info.MHz)
	stats.Cpu.Reservedvms = int32((float64(total_vcpus_mhz) / 100.0) * hv.vcpu_load_factor)
	si.cpu_kernel_ns = cpustats.Kernel
	si.cpu_user_ns = cpustats.User

	/* Network: read cumulative byte counters for the physical NICs discovered at startup */
	si.nic_rx, si.nic_tx = get_nic_bytes(si.imm.nic_ifaces)
	stats.NetRx.Total = si.imm.nic_capacity
	stats.NetTx.Total = si.imm.nic_capacity

	/* some of the data we can only calculate as comparison from the previous measurement */
	if (hv.si != nil) {
		var interval int64 = si.Host.Ts - hv.si.Host.Ts
		if (interval <= 0) {
			logger.Log("system_info_get: host timestamps not in order?")
		} else {
			var udelta uint64
			/*
			 * we sum up only libvirt "kernel" (system + irq + softirq) and "user" (usr + nice)
			 * "iowait" we consider as Free CPU.
			 * "guest" (guest + guest_nice) is already included in "user".
			 */
			udelta = Counter_delta_uint64(si.cpu_kernel_ns, hv.si.cpu_kernel_ns)
			udelta += Counter_delta_uint64(si.cpu_user_ns, hv.si.cpu_user_ns)
			stats.Cpu.Used = int32(udelta * uint64(info.MHz) / uint64(interval * 1000000))
			logger.Debug("gsi: Cpu.Used = %d", stats.Cpu.Used)

			stats.Cpu.Free = stats.Cpu.Total - stats.Cpu.Used
			logger.Debug("gsi: Cpu.Free = %d", stats.Cpu.Free)

			stats.Cpu.Usedvms = total_vcpus_mhz_used
			logger.Debug("gsi: Cpu.Usedvms = %d", stats.Cpu.Usedvms)

			stats.Cpu.Usedother = stats.Cpu.Used - stats.Cpu.Usedvms
			logger.Debug("gsi: Cpu.Usedother = %d", stats.Cpu.Usedother)

			stats.Cpu.Availablevms = stats.Cpu.Total - stats.Cpu.Reservedvms - stats.Cpu.Usedother
			logger.Debug("gsi: Cpu.Availablevms = %d", stats.Cpu.Availablevms)

			/* Network Rx: usedvms = VM net Rx + VM disk reads (NFS/iSCSI arriving over NIC) */
			udelta = Counter_delta_uint64(si.nic_rx, hv.si.nic_rx)
			stats.NetRx.Used = int32((udelta * 1000) / uint64(interval * KiB))
			stats.NetRx.Free = stats.NetRx.Total - stats.NetRx.Used
			stats.NetRx.Usedvmsnet = total_vm_net_rx_bw
			stats.NetRx.Usedvmsdisk = total_vm_disk_rd_bw
			stats.NetRx.Usedvms = stats.NetRx.Usedvmsnet + stats.NetRx.Usedvmsdisk
			stats.NetRx.Usedother = stats.NetRx.Used - stats.NetRx.Usedvms
			/* we do not have bandwidth reservations per VM at least for now */
			stats.NetRx.Availablevms = stats.NetRx.Free
			logger.Debug("gsi: NetRx.Used = %d, Usedvms = %d, Usedother = %d", stats.NetRx.Used, stats.NetRx.Usedvms, stats.NetRx.Usedother)

			/* Network Tx: usedvms = VM net Tx + VM disk writes (NFS/iSCSI leaving over NIC) */
			udelta = Counter_delta_uint64(si.nic_tx, hv.si.nic_tx)
			stats.NetTx.Used = int32((udelta * 1000) / uint64(interval * KiB))
			stats.NetTx.Free = stats.NetTx.Total - stats.NetTx.Used
			stats.NetTx.Usedvmsnet = total_vm_net_tx_bw
			stats.NetTx.Usedvmsdisk = total_vm_disk_wr_bw
			stats.NetTx.Usedvms = stats.NetTx.Usedvmsnet + stats.NetTx.Usedvmsdisk
			stats.NetTx.Usedother = stats.NetTx.Used - stats.NetTx.Usedvms
			/* we do not have bandwidth reservations per VM at least for now */
			stats.NetTx.Availablevms = stats.NetTx.Free
			logger.Debug("gsi: NetTx.Used = %d, Usedvms = %d, Usedother = %d", stats.NetTx.Used, stats.NetTx.Usedvms, stats.NetTx.Usedother)
		}
	}
	si.Vms = vms
	if (hv.si == nil) {
		hv.si = new(SystemInfo)
	}
	*hv.si = si
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
	hv.m.Lock()
	defer hv.m.Unlock()
	var (
		idata inventory.Hostdata
		ikey string
		present bool
		err error
	)
	idata, err = inventory.Get_hostdata(machine.Uuid())
	if (err != nil) {
		return /* host not in inventory yet, ignore */
	}
	for ikey = range idata.Vms {
		_, present = vms[ikey]
		if (!present) {
			logger.Log("delete_ghosts: RUNSTATE_DELETED %s", ikey)
			hv.vm_event_ch <- inventory.VmEvent{ Uuid: ikey, Host: machine.Uuid(), Runstate: openapi.RUNSTATE_DELETED, Ts: ts }
		}
	}
}

/*
 * this information does not change after the first fetch,
 * and is reused for all subsequent system_info_get calls
 */
func system_info_get_immutable(imm *SystemInfoImm) error {
	/* assert hv.m.RLock() or hv.m.Lock() */
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
	imm.os_id, imm.os_version = get_os_version()
	imm.nic_ifaces, imm.nic_capacity = get_nic_ifaces()

	if (imm.caps.Host.CPU.Counter != nil) {
		/* TSC frequency is in Hz */
		imm.info.MHz = uint(imm.caps.Host.CPU.Counter.Frequency / 1000000)
		return nil
	}
	/* If there is no TSC, use the node Max CPU Frequency. Failures are not fatal. */
	defer func() {
		if (err != nil) {
			/* emit warning, we will not override libvirt MHz */
			logger.Log("could not read CPU max frequency: %s", err.Error())
			logger.Log("fallback to libvirt MHz, MHz calculations will be unreliable")
		}
	}()
	raw, err = os.ReadFile(MAX_FREQ_PATH)
	if (err != nil) {
		return nil
	}
	mhz, err = strconv.Atoi(strings.TrimSpace(string(raw)))
	if (err != nil) {
		return nil
	}
	imm.info.MHz = uint(mhz / 1000) /* input from sysfs is measured in KHz */
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
		if (len(fields) < 2) {
			continue
		}
		if (fields[0] == key + ":") {
			return strconv.ParseUint(fields[1], 10, 64)
		}
	}
	return 0, errors.New("key not found")
}

func get_os_version() (string, string) {
	var (
		err error
		data []byte
		scanner *bufio.Scanner
		id, version_id string
	)
	data, err = os.ReadFile("/etc/os-release")
	if (err != nil) {
		logger.Log("failed to read /etc/os-release: %s", err.Error())
		data, err = os.ReadFile("/usr/lib/os-release")
		if (err != nil) {
			logger.Log("failed to read /usr/lib/os-release: %s", err.Error())
			return "", ""
		}
	}
	scanner = bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if (strings.HasPrefix(line, "ID=")) {
			id = strings.Trim(line[3:], `"`)
		} else if (strings.HasPrefix(line, "VERSION_ID=")) {
			version_id = strings.Trim(line[11:], `"`)
		}
	}
	return id, version_id
}

/*
 * Discover qualifying physical NICs and bond masters (not enslaved, not virtual).
 * Returns the interface names and total link capacity in KiB/s.
 * Called once at startup in system_info_get_immutable().
 */
func get_nic_ifaces() ([]string, int32) {
	var (
		err error
		entries []os.DirEntry
		ifaces []string
		capacity int32
	)
	entries, err = os.ReadDir("/sys/class/net")
	if (err != nil) {
		logger.Log("get_nic_ifaces: %s", err.Error())
		return ifaces, 0
	}
	for _, entry := range entries {
		var (
			ifname string = entry.Name()
			sysnet string = "/sys/class/net/" + ifname
			physical_err, bond_err error
		)
		_, err = os.Stat(sysnet + "/master")
		if (err == nil) {
			/* enslaved — skip only if master is a bond; bridge ports are included */
			var master_link string
			master_link, err = os.Readlink(sysnet + "/master")
			if (err != nil) {
				logger.Log("get_nic_ifaces: readlink %s/master: %s", sysnet, err.Error())
				continue
			}
			_, err = os.Stat("/sys/class/net/" + filepath.Base(master_link) + "/bonding")
			if (err == nil) {
				continue /* bond slave */
			}
			/* bridge port or other master type — fall through and include */
		} else if (!errors.Is(err, os.ErrNotExist)) {
			logger.Log("get_nic_ifaces: stat %s/master: %s", sysnet, err.Error())
			continue /* cannot determine enslavement, skip to be safe */
		}
		_, physical_err = os.Stat(sysnet + "/device")
		if (physical_err != nil && !errors.Is(physical_err, os.ErrNotExist)) {
			logger.Log("get_nic_ifaces: stat %s/device: %s", sysnet, physical_err.Error())
		}
		_, bond_err = os.Stat(sysnet + "/bonding")
		if (bond_err != nil && !errors.Is(bond_err, os.ErrNotExist)) {
			logger.Log("get_nic_ifaces: stat %s/bonding: %s", sysnet, bond_err.Error())
		}
		if (physical_err != nil && bond_err != nil) {
			continue /* neither physical NIC nor bond master */
		}
		ifaces = append(ifaces, ifname)
		{
			var (
				speed_data []byte
				speed int64
			)
			speed_data, err = os.ReadFile(sysnet + "/speed")
			if (err == nil) {
				speed, err = strconv.ParseInt(strings.TrimSpace(string(speed_data)), 10, 64)
				if (err == nil && speed > 0) {
					capacity += int32(speed * 125000 / 1024)
				}
			}
		}
	}
	return ifaces, capacity
}

/*
 * Read cumulative Rx/Tx byte counters from /proc/net/dev for the given interfaces.
 * Called every system_info_loop tick.
 */
func get_nic_bytes(ifaces []string) (uint64, uint64) {
	var (
		err error
		data []byte
		scanner *bufio.Scanner
		rx_bytes, tx_bytes uint64
		iface_set map[string]bool
	)
	if (len(ifaces) == 0) {
		return 0, 0
	}
	iface_set = make(map[string]bool, len(ifaces))
	for _, iface := range ifaces {
		iface_set[iface] = true
	}
	data, err = os.ReadFile("/proc/net/dev")
	if (err != nil) {
		logger.Log("get_nic_bytes: %s", err.Error())
		return 0, 0
	}
	scanner = bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var (
			line string = scanner.Text()
			colon int
			ifname string
			fields []string
			rx, tx uint64
		)
		colon = strings.Index(line, ":")
		if (colon < 0) {
			continue
		}
		ifname = strings.TrimSpace(line[:colon])
		if (!iface_set[ifname]) {
			continue
		}
		fields = strings.Fields(line[colon + 1:])
		if (len(fields) < 9) {
			continue
		}
		rx, err = strconv.ParseUint(fields[0], 10, 64)
		if (err != nil) {
			continue
		}
		tx, err = strconv.ParseUint(fields[8], 10, 64)
		if (err != nil) {
			continue
		}
		rx_bytes += rx
		tx_bytes += tx
	}
	return rx_bytes, tx_bytes
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
		var blkstat *libvirt.DomainBlockStats
		blkstat, err = d.BlockStats("")
		if (err == nil) {
			if (blkstat.RdBytesSet) {
				vm.disk_rd += blkstat.RdBytes
			}
			if (blkstat.WrBytesSet) {
				vm.disk_wr += blkstat.WrBytes
			}
			if (blkstat.RdReqSet) {
				vm.disk_rd_io += blkstat.RdReq
			}
			if (blkstat.WrReqSet) {
				vm.disk_wr_io += blkstat.WrReq
			}
		}
		for _, net := range xd.Devices.Interfaces {
			if (net.Target.Dev != "") {
				var netstat *libvirt.DomainInterfaceStats
				netstat, err = d.InterfaceStats(net.Target.Dev)
				if (err != nil) {
					/* just continue, we might just not have stats because we are shutdown */
					continue
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
			/* ignore, assume no stats are available, for example we are shutdown */
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
		var interval int64 = vm.Ts - old.Ts
		var udelta uint64 = Counter_delta_uint64(vm.cpu_time, old.cpu_time)
		logger.Debug("gds: udelta = %d, interval = %d, Vcpus = %d", udelta, interval, vm.Vcpus)

		if (udelta > 0 && interval > 0 && vm.Vcpus > 0) {
			vm.stats.CpuUtilization = int32((udelta * 100) / uint64(interval * 1000000))
		}
		logger.Debug("gds: CpuUtilization = %d", vm.stats.CpuUtilization)
		vm.stats.MhzUsed = int32(udelta * uint64(imm.info.MHz) / uint64(interval * 1000000))

		var delta int64 = Counter_delta_int64(vm.disk_rd, old.disk_rd)
		logger.Debug("gds: disk_rd delta = %d", delta)
		if (delta > 0 && interval > 0) {
			vm.stats.DiskRdBw = int32((delta * 1000) / (interval * KiB))
		}
		logger.Debug("gds: DiskRdBw = %d", vm.stats.DiskRdBw)

		delta = Counter_delta_int64(vm.disk_wr, old.disk_wr)
		logger.Debug("gds: disk_wr delta = %d", delta)
		if (delta > 0 && interval > 0) {
			vm.stats.DiskWrBw = int32((delta * 1000) / (interval * KiB))
		}
		logger.Debug("gds: DiskWrBw = %d", vm.stats.DiskWrBw)

		delta = Counter_delta_int64(vm.disk_rd_io, old.disk_rd_io)
		logger.Debug("gds: disk_rd_io delta = %d", delta)
		if (delta > 0 && interval > 0) {
			vm.stats.DiskRdIops = int32((delta * 1000) / interval)
		}
		logger.Debug("gds: DiskRdIops = %d", vm.stats.DiskRdIops)

		delta = Counter_delta_int64(vm.disk_wr_io, old.disk_wr_io)
		logger.Debug("gds: disk_wr_io delta = %d", delta)
		if (delta > 0 && interval > 0) {
			vm.stats.DiskWrIops = int32((delta * 1000) / interval)
		}
		logger.Debug("gds: DiskWrIops = %d", vm.stats.DiskWrIops)

		delta = Counter_delta_int64(vm.net_rx, old.net_rx)
		logger.Debug("gds: net_rx delta = %d", delta)
		if (delta > 0 && interval > 0) {
			vm.stats.NetRxBw = int32((delta * 1000) / (interval * KiB))
		}
		logger.Debug("gds: NetRxBw = %d", vm.stats.NetRxBw)

		delta = Counter_delta_int64(vm.net_tx, old.net_tx)
		logger.Debug("gds: net_tx delta = %d", delta)
		if (delta > 0 && interval > 0) {
			vm.stats.NetTxBw = int32((delta * 1000) / (interval * KiB))
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

func system_info_get_vmstats(si *SystemInfo, uuid string) (openapi.Vmstats, error) {
	/* assert hv.m.RLock() */
	var (
		vm SystemInfoVm
		present bool
	)
	if (si == nil) {
		return openapi.Vmstats{}, errors.New("SystemInfo not available")
	}
	vm, present = si.Vms[uuid]
	if (!present) {
		return openapi.Vmstats{}, errors.New("could not find vm")
	}
	return vm.stats, nil
}

func system_info_get_host(si *SystemInfo) openapi.Host {
	/* assert hv.m.Rlock() */
	return openapi.Host{
		Uuid: machine.Uuid(),
		Def: openapi.Hostdef{
			Name: si.Host.Name,
			Cpuarch: si.Host.Cpuarch,
			Cpudef: si.Host.Cpudef,
			Tscfreq: func() int64 {
				if (si.imm.caps.Host.CPU.Counter != nil) {
					return int64(si.imm.caps.Host.CPU.Counter.Frequency)
				} else {
					logger.Debug("TSC counter not available in capabilities")
					return 0
				}
			}(),
			Sysinfo: openapi.HostdefSysinfo{
				Version: si.imm.bios_version,
				Date: si.imm.bios_date,
			},
			Osid: si.Host.Osid,
			Osv: si.Host.Osv,
		},
		Cstate: si.Host.Cstate,
		Lockid: lockman.Lockid(),
		Ts: si.Host.Ts,
	}
}

func system_info_get_hoststats(si *SystemInfo) openapi.Hoststats {
	return si.Host.stats
}

func get_system_info_loop_done() bool {
	hv.m.RLock()
	defer hv.m.RUnlock()
	return hv.system_info_loop_done
}

func set_system_info_loop_done() {
	hv.m.Lock()
	defer hv.m.Unlock()
	hv.system_info_loop_done = true
}
