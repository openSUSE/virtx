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
	"encoding/json"
	"sync"
	"sync/atomic"
	"errors"
	"os"
	"bytes"
	"bufio"
	"strings"
	"strconv"
	"fmt"

	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/vmreg"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/encoding/hexstring"
	. "suse.com/virtx/pkg/constants"
)

const (
	max_freq_path = "/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq"
	libvirt_uri = "qemu:///system"
	libvirt_reconnect_seconds = 5
	libvirt_system_info_seconds = 15
)

type SystemInfoImm struct { /* immutable fields of SystemInfo */
	caps libvirtxml.Caps
	/*
	 * XXX Mhz needs to be fetched manually because libvirt does a bad job of it.
	 * libvirt reads /proc/cpuinfo, which just shows current Mhz, not max Mhz.
	 * So any power state change, frequency change may change results. Oh my.
	 *
	 * We need to call nodeinfo specifically anyway, only for the total memory size,
	 * since there is an API to get the free memory, but not one to get total memory size. Ugh.
	 * So we keep using nodeinfo and we keep it here, overwriting the MHz value.
	 */
	info *libvirt.NodeInfo
	bios_version string
	bios_date string
}

type SystemInfo struct {
	imm SystemInfoImm
	Host openapi.Host
	Vms inventory.VmsInventory

	/* overall counters for host cpu nanoseconds (for host stats) */
	cpu_idle_ns uint64
	cpu_kernel_ns uint64
	cpu_user_ns uint64
}

type Hypervisor struct {
	is_connected atomic.Bool
	m sync.RWMutex

	conn *libvirt.Connect
	lifecycle_id int
	vm_event_ch chan inventory.VmEvent
	system_info_ch chan SystemInfo

	uuid string /* the UUID of this host */
	cpuarch openapi.Cpuarch /* the Arch and Vendor */

	vcpu_load_factor float64
}
var hv = Hypervisor{
	m: sync.RWMutex{},
	lifecycle_id: -1,
}

/*
 * Connect to libvirt.
 */
func Connect() error {
	hv.m.Lock()
	defer hv.m.Unlock()

	if (hv.conn != nil) {
		/* Reconnect */
		stop_listening()
		hv.conn.Close()
		hv.is_connected.Store(false)
	}
	conn, err := libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return err
	}
	hv.conn = conn
	hv.is_connected.Store(true)
	err = start_listening()
	return err
}

func Shutdown() {
	hv.m.Lock()
	defer hv.m.Unlock()
	logger.Log("shutdown started...")
	stop_listening()
	hv.conn.Close();
	close(hv.vm_event_ch)
	close(hv.system_info_ch)
	hv.conn = nil
	hv.vm_event_ch = nil
	hv.system_info_ch = nil
	hv.lifecycle_id = -1
	logger.Log("shutdown complete.")
}

/* get basic information about a Domain */
func get_domain_info(d *libvirt.Domain) (string, string, openapi.Vmrunstate, error) {
	/* assert (hv.m.IsRLocked) */
	var (
		name string
		uuid string
		reason int
		state libvirt.DomainState
		err error
		enum_state openapi.Vmrunstate = openapi.RUNSTATE_NONE
	)
	name, err = d.GetMetadata(libvirt.DOMAIN_METADATA_TITLE, "", libvirt.DOMAIN_AFFECT_CONFIG)
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
	//case libvirt.DOMAIN_NOSTATE: /* leave enum_state RUNSTATE_NONE */
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
			/* XXX I have never seen this yet in my migration tests XXX */
			logger.Log("XXX DOMAIN_SHUTOFF_MIGRATED encountered XXX")
			enum_state = openapi.RUNSTATE_DELETED
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
func system_info_loop(seconds int) error {
	var (
		old, si SystemInfo
		err error
		ticker *time.Ticker
	)
	logger.Log("system_info_loop starting...")
	defer logger.Log("system_info_loop exit")
	ticker = time.NewTicker(time.Duration(seconds) * time.Second)
	defer ticker.Stop()

	err = get_system_info(&old, nil)
	if (err != nil) {
		return err
	}
	hv.m.Lock()
	hv.uuid = old.Host.Uuid
	hv.cpuarch = old.Host.Def.Cpuarch
	check_vmreg(hv.uuid, &old)
	hv.m.Unlock()

	/* this first info is missing vm cpu stats and host cpu stats */
	hv.system_info_ch <- old

	for range ticker.C {
		err = get_system_info(&si, &old)
		if (err != nil) {
			return err
		}
		hv.system_info_ch <- si
	}
	return nil
}

func delete_ghosts(vms inventory.VmsInventory, ts int64) {
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

func lifecycle_cb(_ *libvirt.Connect, d *libvirt.Domain, e *libvirt.DomainEventLifecycle) {
	/* e.Detail: see all DomainEvent*DetailType types */
	var (
		name, uuid string
		state openapi.Vmrunstate
		persistent bool
		err error
	)
	/*
	 * I think we need to lock here because we could be connecting, and the use of libvirt.Domain
	 * can access the connection whose data structure may be in the process of updating.
	 */
	hv.m.RLock()
	defer hv.m.RUnlock()

	if (e.Event == libvirt.DOMAIN_EVENT_UNDEFINED) {
		/* VM has been DELETED */
		uuid, err = d.GetUUIDString()
		if (err != nil) {
			logger.Log("lifecycle_cb: GetUUIDString error: %s", err.Error())
			return
		}
		state = openapi.RUNSTATE_DELETED
	} else {
		persistent, err = d.IsPersistent()
		if (err != nil) {
			logger.Log("lifecycle_cb: IsPersistent err: %s", err.Error())
			return
		}
		if (!persistent) {
			return /* ignore transient domains (ongoing migrations) */
		}
		name, uuid, state, err = get_domain_info(d)
	}
	if (err != nil) {
		logger.Log("lifecycle_cb: event %d: %s:", e.Event, err.Error())
	}
	if (state != openapi.RUNSTATE_NONE) {
		logger.Log("[VmEvent] %s/%s: %v state: %d", name, uuid, e, state)
		hv.vm_event_ch <- inventory.VmEvent{ Uuid: uuid, Host: hv.uuid, State: state, Ts: time.Now().UTC().UnixMilli() }
	}
}

/*
 * Start listening for domain events and collecting system information.
 * Sets the lifecycle_id, vm_event_ch and system_info_ch fields of the Hypervisor struct.
 * Collects system information every "seconds" seconds.
 */
func start_listening() error {
	/* assert(hv.m.IsLocked()) */
	var err error
	hv.lifecycle_id, err = hv.conn.DomainEventLifecycleRegister(nil, lifecycle_cb)
	if (err != nil) {
		return err
	}
	return nil
}

func stop_listening() {
	/* assert(hv.m.IsLocked()) */
	if (hv.lifecycle_id < 0) {
		/* already stopped */
		return
	}
	_ = hv.conn.DomainEventDeregister(hv.lifecycle_id)
	hv.lifecycle_id = -1
}

func Define_domain(xml string, uuid string) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	domain, err = conn.DomainDefineXML(xml)
	if (err != nil) {
		return err
	}
	defer domain.Free()
	xml, err = domain.GetXMLDesc(libvirt.DOMAIN_XML_SECURE | libvirt.DOMAIN_XML_INACTIVE | libvirt.DOMAIN_XML_MIGRATABLE)
	if (err != nil) {
		return err
	}
	/* store the processed XML in /vms/xml/host-uuid/vm-uuid.xml */
	err = vmreg.Save(hv.uuid, uuid, xml)
	if (err != nil) {
		logger.Log("Define_domain: failed to vmreg.Save(%s, %s)", hv.uuid, uuid)
	}
	return nil
}

func Migrate_domain(hostname string, host_uuid string, host_old string, uuid string, live bool, vcpus int) error {
	var (
		err error
		conn, conn2 *libvirt.Connect
		domain, domain2 *libvirt.Domain
		len int
		bytes [16]byte
		params libvirt.DomainMigrateParameters
		flags libvirt.DomainMigrateFlags
	)
	params.URI = "tcp://" + hostname
	params.URISet = true
	if (live) {
		params.ParallelConnectionsSet = true
		params.ParallelConnections = vcpus
		flags = libvirt.MIGRATE_LIVE         |
			libvirt.MIGRATE_PERSIST_DEST     |
			libvirt.MIGRATE_ABORT_ON_ERROR   |
			libvirt.MIGRATE_UNDEFINE_SOURCE  |
			libvirt.MIGRATE_AUTO_CONVERGE    |
			libvirt.MIGRATE_PARALLEL         |
			libvirt.MIGRATE_UNSAFE
	} else {
		flags = libvirt.MIGRATE_OFFLINE      |
			libvirt.MIGRATE_PERSIST_DEST     |
			libvirt.MIGRATE_ABORT_ON_ERROR   |
			libvirt.MIGRATE_UNDEFINE_SOURCE  |
			libvirt.MIGRATE_UNSAFE
	}
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	conn2, err = libvirt.NewConnect("qemu+tcp://" + hostname + "/system")
	if (err != nil) {
		return err
	}
	defer conn2.Close()
	len = hexstring.Encode(bytes[:], uuid)
	if (len <= 0) {
		return errors.New("failed to encode uuid from hexstring")
	}
	domain, err = conn.LookupDomainByUUID(bytes[:])
	if (err != nil) {
		return err
	}
	defer domain.Free()
	domain2, err = domain.Migrate3(conn2, &params, flags)
	if (err != nil) {
		logger.Log("Migrate_domain: failed to Migrate3")
		return err
	}
	defer domain2.Free()
	/* move the xml file to /vms/xml/host_uuid/uuid.xml */
	err = vmreg.Move(host_uuid, host_old, uuid)
	if (err != nil) {
		logger.Log("Migrate_domain: failed to vmreg.Move(%s, %s, %s)", host_uuid, host_old, uuid)
	}
	return nil
}

type QemuMigrationInfo struct {
	R struct {
		Status string `json:"status"`
		Ram struct {
			Transferred int64 `json:"transferred"`
			Remaining int64 `json:"remaining"`
			Total int64 `json:"total"`
			Mbps float64 `json:"mbps"`
			Dirty_pages_rate int64 `json:"dirty-pages-rate"`
			Page_size int64 `json:"page-size"`
		}
	} `json:"return"`
}

func Get_migration_info(uuid string) (openapi.MigrationInfo, error) {
	var (
		err error
		conn *libvirt.Connect
		qemu_info QemuMigrationInfo
		info openapi.MigrationInfo
		result_json string
		domain *libvirt.Domain
		len int
		bytes [16]byte
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return info, err
	}
	len = hexstring.Encode(bytes[:], uuid)
	if (len <= 0) {
		return info, errors.New("failed to encode uuid from hexstring")
	}
	domain, err = conn.LookupDomainByUUID(bytes[:])
	if (err != nil) {
		return info, err
	}
	defer domain.Free()
	result_json, err = domain.QemuMonitorCommand(
		"{ \"execute\": \"query-migrate\" }",
		libvirt.DOMAIN_QEMU_MONITOR_COMMAND_DEFAULT,
	)
	if (err != nil) {
		return info, err
	}
	err = json.Unmarshal([]byte(result_json), &qemu_info)
	if (err != nil) {
		return info, err
	}
	logger.Log("result_json = %s", result_json)
	logger.Log("qemu_info.R.Status = %s", qemu_info.R.Status)
	logger.Log("qemu_info = %+v", qemu_info)
	err = info.State.Parse(qemu_info.R.Status)
	if (err != nil) {
		return info, err
	}
	if (info.State != openapi.MIGRATION_ACTIVE && info.State != openapi.MIGRATION_COMPLETED) {
		return info, nil
	}
	info.Progress.Total = qemu_info.R.Ram.Total
	info.Progress.Remaining = qemu_info.R.Ram.Remaining
	info.Progress.Transferred = qemu_info.R.Ram.Transferred
	info.Progress.Rate = float32(qemu_info.R.Ram.Mbps / 8)
	return info, nil
}

func Dumpxml(uuid string) (string, error) {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		xml string
		bytes [16]byte
		len int
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return "", err
	}
	defer conn.Close()
	len = hexstring.Encode(bytes[:], uuid)
	if (len <= 0) {
		return "", errors.New("failed to encode uuid from hexstring")
	}
	domain, err = conn.LookupDomainByUUID(bytes[:])
	if (err != nil) {
		return "", err
	}
	defer domain.Free()
	xml, err = domain.GetXMLDesc(0)
	if (err != nil) {
		return "", err
	}
	return xml, nil
}

func Boot_domain(uuid string) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		bytes [16]byte
		len int
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	len = hexstring.Encode(bytes[:], uuid)
	if (len <= 0) {
		return errors.New("failed to encode uuid from hexstring")
	}
	domain, err = conn.LookupDomainByUUID(bytes[:])
	if (err != nil) {
		return err
	}
	defer domain.Free()
	err = domain.Create()
	if (err != nil) {
		return err
	}
	return nil
}

func Pause_domain(uuid string) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		bytes [16]byte
		len int
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	len = hexstring.Encode(bytes[:], uuid)
	if (len <= 0) {
		return errors.New("failed to encode uuid from hexstring")
	}
	domain, err = conn.LookupDomainByUUID(bytes[:])
	if (err != nil) {
		return err
	}
	defer domain.Free()
	err = domain.Suspend()
	if (err != nil) {
		return err
	}
	return nil
}

func Resume_domain(uuid string) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		bytes [16]byte
		len int
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	len = hexstring.Encode(bytes[:], uuid)
	if (len <= 0) {
		return errors.New("failed to encode uuid from hexstring")
	}
	domain, err = conn.LookupDomainByUUID(bytes[:])
	if (err != nil) {
		return err
	}
	defer domain.Free()
	err = domain.Resume()
	if (err != nil) {
		return err
	}
	return nil
}

func Shutdown_domain(uuid string, force int16) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		bytes [16]byte
		len int
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	len = hexstring.Encode(bytes[:], uuid)
	if (len <= 0) {
		return errors.New("failed to encode uuid from hexstring")
	}
	domain, err = conn.LookupDomainByUUID(bytes[:])
	if (err != nil) {
		return err
	}
	defer domain.Free()
	if (force == 0) {
		err = domain.Shutdown()
	} else if (force == 1) {
		err = domain.DestroyFlags(libvirt.DOMAIN_DESTROY_GRACEFUL)
	} else {
		err = domain.DestroyFlags(0)
	}
	return err
}

func Delete_domain(uuid string) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		bytes [16]byte
		len int
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	len = hexstring.Encode(bytes[:], uuid)
	if (len <= 0) {
		return errors.New("failed to encode uuid from hexstring")
	}
	domain, err = conn.LookupDomainByUUID(bytes[:])
	if (err != nil) {
		return err
	}
	defer domain.Free()
	var (
		ds libvirt.DomainState
		//reason int
	)
	ds, _, err = domain.GetState()
	if (err != nil) {
		return err
	}
	if (ds != libvirt.DOMAIN_SHUTOFF && ds != libvirt.DOMAIN_CRASHED) {
		return errors.New("libvirt domain is not SHUTOFF or CRASHED")
	}
	err = domain.UndefineFlags(libvirt.DOMAIN_UNDEFINE_MANAGED_SAVE |
		libvirt.DOMAIN_UNDEFINE_SNAPSHOTS_METADATA |
		libvirt.DOMAIN_UNDEFINE_NVRAM |
		libvirt.DOMAIN_UNDEFINE_CHECKPOINTS_METADATA)
	//libvirt.DOMAIN_UNDEFINE_TPM
	if (err != nil) {
		return err
	}
	/* remove the registered xml file */
	err = vmreg.Delete(hv.uuid, uuid)
	if (err != nil) {
		logger.Log("Delete_domain: failed to vmreg.Delete(%s, %s)", hv.uuid, uuid)
	}
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
	/* we still need nodeinfo specifically and only for the memory size. Ugh. */
	imm.info, err = hv.conn.GetNodeInfo()
	if (err != nil) {
		return err
	}
	raw, err = os.ReadFile(max_freq_path)
	if (err != nil) {
		return err
	}
	mhz, err = strconv.Atoi(strings.TrimSpace(string(raw)))
	if (err != nil) {
		return err
	}
	imm.info.MHz = uint(mhz / 1000) /* input from sysfs is measured in Hz */
	return nil
}

func get_system_info(si *SystemInfo, old *SystemInfo) error {
	hv.m.RLock()
	defer hv.m.RUnlock()
	var (
		host openapi.Host
		vms inventory.VmsInventory
		err error
		caps *libvirtxml.Caps
		info *libvirt.NodeInfo
	)
	var (
		doms []libvirt.Domain
		d libvirt.Domain
	)
	var (
		free_memory uint64
		total_memory_capacity uint64
		total_memory_used uint64
		total_vcpus_mhz uint32
		total_cpus_used_percent int32
		cpustats *libvirt.NodeCPUStats
	)

	if (old == nil) {
		err = get_system_info_immutable(&si.imm)
		if (err != nil) {
			goto out
		}
	} else {
		si.imm = old.imm
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

	vms = make(inventory.VmsInventory)
	for _, d = range doms {
		var (
			vm inventory.Vmdata
			oldvm inventory.Vmdata
			present bool
		)
		vm.Name, vm.Uuid, vm.Runinfo.Runstate, err = get_domain_info(&d)
		if (err != nil) {
			logger.Log("could not get_domain_info: %s", err.Error())
			continue
		}
		if (old != nil) {
			oldvm, present = old.Vms[vm.Uuid]
		}
		vm.Runinfo.Host = host.Uuid
		vm.Ts = host.Ts
		if (present) {
			err = get_domain_stats(&d, &vm, &oldvm)
		} else {
			err = get_domain_stats(&d, &vm, nil)
		}
		total_memory_capacity += uint64(vm.Stats.MemoryCapacity)
		total_memory_used += uint64(vm.Stats.MemoryUsed)
		total_vcpus_mhz += uint32(vm.Stats.Vcpus) * uint32(info.MHz) /* should be equal to Topology Sockets * Cores, since we do not use threads */
		total_cpus_used_percent += vm.Stats.CpuUtilization
		vms[vm.Uuid] = vm
	}
	/* now calculate host resources */
	free_memory, err = hv.conn.GetFreeMemory()
	if (err != nil) {
		goto out
	}
	cpustats, err = hv.conn.GetCPUStats(-1, 0)
	if (err != nil) {
		goto out
	}

	/* Memory */
	host.Resources.Memory.Total = int32(info.Memory / KiB) /* info returns memory in KiB, translate to MiB */
	host.Resources.Memory.Free = int32(free_memory / MiB) /* this returns in bytes, translate to MiB */
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
	si.cpu_user_ns = cpustats.User /* unfortunately this includes guest time */

	/* some of the data we can only calculate as comparison from the previous measurement */
	if (old != nil) {
		interval := float64(host.Ts - old.Host.Ts)
		if (interval <= 0.0) {
			logger.Log("get_system_info: host timestamps not in order?")
		} else {
			var delta float64 = float64(Counter_delta_uint64(si.cpu_idle_ns, old.cpu_idle_ns))
			/* idle counters are completely unreliable, behavior depends on cpu model */
			//host.Resources.Cpu.Free = int32(delta / (interval * 1000000) * float64(info.MHz) / float64(info.Threads))
			delta = float64(Counter_delta_uint64(si.cpu_kernel_ns, old.cpu_kernel_ns))
			host.Resources.Cpu.Used = int32(delta / (interval * 1000000) * float64(info.MHz))
			delta = float64(Counter_delta_uint64(si.cpu_user_ns, old.cpu_user_ns))
			host.Resources.Cpu.Used += int32(delta / (interval * 1000000) * float64(info.MHz))
			host.Resources.Cpu.Free = host.Resources.Cpu.Total - host.Resources.Cpu.Used

			host.Resources.Cpu.Usedvms = total_cpus_used_percent * int32(info.MHz) / 100
			host.Resources.Cpu.Usedos = host.Resources.Cpu.Used - host.Resources.Cpu.Usedvms
			host.Resources.Cpu.Availablevms = host.Resources.Cpu.Total - host.Resources.Cpu.Reservedvms - host.Resources.Cpu.Usedos
		}
	} else {
		/*
		 * first run, check the registry to warn about mismatches between it and libvirt
		 * and decide how to handle them:
		 *
		 * - a file exists but libvirt has no domain with that uuid. Warning?
		 * - libvirt has a domain for which no file exists. Warning?
		 *
		 * We will need a feature to force creation of a libvirt domain from the xml file,
		 * and to create the xml file from the domain?
		 */
	}
out:
	si.Host = host
	si.Vms = vms

	delete_ghosts(si.Vms, host.Ts)
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

func get_domain_stats(d *libvirt.Domain, vm *inventory.Vmdata, old *inventory.Vmdata) error {
	var err error
	{
		var info *libvirt.DomainInfo
		var memstat []libvirt.DomainMemoryStat
		info, err = d.GetInfo()
		if (err != nil) {
			return err
		}
		vm.Stats.Vcpus = int16(info.NrVirtCpu)
		vm.Cpu_time = info.CpuTime
		vm.Stats.MemoryCapacity = int64(info.Memory / KiB) /* convert from KiB to MiB */
		memstat, err = d.MemoryStats(20, 0)
		if (err != nil) {
			return err
		}
		for _, stat := range memstat {
			if (libvirt.DomainMemoryStatTags(stat.Tag) == libvirt.DOMAIN_MEMORY_STAT_RSS) {
				vm.Stats.MemoryUsed = int64(stat.Val / KiB) /* convert from KiB to MiB */
				break
			}
		}
	}
	if (old != nil) {
		/* calculate deltas from previous Vm info */
		if (vm.Runinfo.Runstate == openapi.RUNSTATE_RUNNING &&
			old.Runinfo.Runstate == openapi.RUNSTATE_RUNNING) {
			var udelta uint64 = Counter_delta_uint64(vm.Cpu_time, old.Cpu_time)
			if (udelta > 0 && (vm.Ts - old.Ts) > 0 && vm.Stats.Vcpus > 0) {
				vm.Stats.CpuUtilization = int32((udelta * 100) / (uint64(vm.Ts - old.Ts) * 1000000))
			}
			var delta int64 = Counter_delta_int64(vm.Net_rx, old.Net_rx)
			if (delta > 0 && (vm.Ts - old.Ts) > 0) {
				vm.Stats.NetRxBw = int32((delta * 1000) / ((vm.Ts - old.Ts) * KiB))
			}
			delta = Counter_delta_int64(vm.Net_tx, old.Net_tx)
			if (delta > 0 && (vm.Ts - old.Ts) > 0) {
				vm.Stats.NetTxBw = int32((delta * 1000) / ((vm.Ts - old.Ts) * KiB))
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
				vm.Stats.DiskCapacity += int64(blockinfo.Capacity / MiB)
				vm.Stats.DiskAllocation += int64(blockinfo.Allocation / MiB)
				vm.Stats.DiskPhysical += int64(blockinfo.Physical / MiB)
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
func GetVmEventCh() (chan inventory.VmEvent) {
	return hv.vm_event_ch
}

/* Return the systemInfo Events Channel */
func GetSystemInfoCh() (chan SystemInfo) {
	return hv.system_info_ch
}

func freeDomains(doms []libvirt.Domain) {
	for _, d := range doms {
		d.Free()
	}
}

func init_vm_event_loop() {
	var err error
	logger.Log("init_vm_event_loop: Entering")
	for {
		err = libvirt.EventRunDefaultImpl()
		if (err != nil) {
			panic(err)
		}
	}
	logger.Fatal("init vm_event_loop: Exiting (should never happen!)")
}

func init_system_info_loop() {
	logger.Log("init_vm_system_info_loop: Waiting for a libvirt connection...")
	for ; hv.is_connected.Load() == false; {
		time.Sleep(time.Duration(1) * time.Second)
	}
	for {
		var (
			err error
			libvirt_err libvirt.Error
			ok bool
		)
		err = system_info_loop(libvirt_system_info_seconds)
		libvirt_err, ok = err.(libvirt.Error)
		if (ok) {
			if (libvirt_err.Level >= libvirt.ERR_ERROR) {
				logger.Log(err.Error())
				logger.Log("reconnect, attempt every %d seconds...", libvirt_reconnect_seconds)
				for ; err != nil; err = Connect() {
					time.Sleep(time.Duration(libvirt_reconnect_seconds) * time.Second)
				}
			}
		} else {
			logger.Log(err.Error())
		}
	}
	logger.Fatal("init vm_system_info_loop (should never happen!)")
}

/*
 * init() is guaranteed to be called before main starts, so we can guarantee that EventRegisterDefaultImpl
 * is always called before Connect() in main.
 */
func init() {
	hv.m.Lock()
	defer hv.m.Unlock()
	var err error
	err = libvirt.EventRegisterDefaultImpl();
	if (err != nil) {
		panic(err)
	}
	hv.vm_event_ch = make(chan inventory.VmEvent, 64)
	hv.system_info_ch = make(chan SystemInfo, 64)
	hv.vcpu_load_factor = read_numa_preplace_conf()
	logger.Log("init, vcpu_load_factor %f", hv.vcpu_load_factor)
	go init_vm_event_loop()
	go init_system_info_loop()
}

func read_numa_preplace_conf() float64 {
	var (
		factor float64 = 25.0
		err error
		data []byte
		scanner *bufio.Scanner
	)
	data, err = os.ReadFile("/etc/numa-preplace.conf")
	if (err != nil) {
		logger.Log("could not read /etc/numa-preplace.conf")
		return factor
	}
	scanner = bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		/* remove comments after # */
		idx := strings.Index(line, "#")
		if (idx >= 0) {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if (line == "") {
			continue;
		}
		/* now split option name and value */
		option := strings.SplitN(line, " ", 2)
		if (len(option) != 2) {
			logger.Log("skipping malformed line: %s", line)
			continue
		}
		if (strings.TrimSpace(option[0]) == "-o") {
			var value float64
			value, err = strconv.ParseFloat(strings.TrimSpace(option[1]), 64)
			if (err != nil) {
				logger.Log("skipping malformed option value: %s", option[1])
				continue
			}
			factor = value
			break
		}
	}
	return factor
}

func Arch() string {
	hv.m.RLock()
	defer hv.m.RUnlock()
	return hv.cpuarch.Arch
}

func Uuid() string {
	hv.m.RLock()
	defer hv.m.RUnlock()
	return hv.uuid
}

func check_vmreg(host_uuid string, si *SystemInfo) {
	var (
		err error
		uuid string
		uuids []string
		conn *libvirt.Connect
	)
	err = os.MkdirAll(fmt.Sprintf("%s/%s", REG_DIR, host_uuid), 0750)
	if (err != nil) {
		logger.Fatal("could not create %s/%s: %s", REG_DIR, host_uuid, err.Error())
	}
	/* check that all vms in libvirt are registered in vmreg */
	for uuid, _ = range(si.Vms) {
		err = vmreg.Access(host_uuid, uuid)
		if (err == nil) {
			continue
		}
		if (!os.IsNotExist(err)) {
			logger.Fatal("could not access file in %s/%s: %s", REG_DIR, host_uuid, err.Error())
		}
		/* os.IsNotExist */
		logger.Log("WARNING: local libvirt domain %s/%s is not registered in vmreg", host_uuid, uuid)
	}
	uuids, err = vmreg.Uuids(host_uuid)
	if (err != nil) {
		logger.Fatal("could not get the list of VM uuids for host %s", host_uuid)
	}
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		logger.Fatal("could not connect to libvirt: %s", err.Error())
	}
	defer conn.Close()

	/* check that all vms in vmreg are registered in libvirt */
	for _, uuid = range(uuids) {
		var (
			domain *libvirt.Domain
			bytes [16]byte
			len int
		)
		len = hexstring.Encode(bytes[:], uuid)
		if (len <= 0) {
			logger.Fatal("failed to encode uuid from hexstring")
		}
		domain, err = conn.LookupDomainByUUID(bytes[:])
		if (err != nil) {
			logger.Log("WARNING: vmreg VM %s/%s is not registered in libvirt", host_uuid, uuid)
		} else {
			domain.Free()
		}
	}
}
