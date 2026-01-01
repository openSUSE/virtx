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

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/vmreg"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/metadata"

	. "suse.com/virtx/pkg/constants"
)

const (
	max_freq_path = "/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq"
	libvirt_uri = "qemu:///system"
	libvirt_reconnect_seconds = 5
	libvirt_system_info_seconds = 15
)

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
	si *SystemInfo
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
	logger.Debug("shutdown started...")
	stop_listening()
	hv.conn.Close();
	close(hv.vm_event_ch)
	close(hv.system_info_ch)
	hv.conn = nil
	hv.vm_event_ch = nil
	hv.system_info_ch = nil
	hv.lifecycle_id = -1
	logger.Debug("shutdown complete.")
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
	logger.Debug("get_domain_info: state %d, reason %d", state, reason)
	switch (state) {
	//case libvirt.DOMAIN_NOSTATE: /* leave enum_state RUNSTATE_NONE */
	case libvirt.DOMAIN_RUNNING:
		enum_state = openapi.RUNSTATE_RUNNING
	case libvirt.DOMAIN_BLOCKED: /* should be Xen only IIUC */
		logger.Log("XXX DOMAIN_BLOCKED encountered XXX")
		enum_state = openapi.RUNSTATE_PAUSED
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
		logger.Debug("[VmEvent] %s/%s: %v state: %d", name, uuid, e, state)
		_ = name
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
	xml, err = domain.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
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
	domain, err = conn.LookupDomainByUUIDString(uuid)
	if (err != nil) {
		return err
	}
	defer domain.Free()
	err = record_domain_op(domain, openapi.OpVmMigrate, openapi.OPERATION_STARTED, "")
	if (err != nil) {
		return err
	}
	domain2, err = domain.Migrate3(conn2, &params, flags)
	if (err != nil) {
		logger.Log("Migrate_domain: failed to Migrate3: %s", err.Error())
		_ = record_domain_op(domain, openapi.OpVmMigrate, openapi.OPERATION_FAILED, err.Error())
		return err
	}
	defer domain2.Free()
	/* move the xml file to /vms/xml/host_uuid/uuid.xml */
	err = vmreg.Move(host_uuid, host_old, uuid)
	if (err != nil) {
		logger.Log("Migrate_domain: failed to vmreg.Move(%s, %s, %s)", host_uuid, host_old, uuid)
	}
	_ = record_domain_op(domain2, openapi.OpVmMigrate, openapi.OPERATION_COMPLETED, "")
	return nil
}

/* record the domain-altering operation metadata into the domain XML */
func record_domain_op(domain *libvirt.Domain, op openapi.Operation, state openapi.OperationState, errstr string) error {
	var (
		err error
		xmlstr string
		meta metadata.Operation
		impact libvirt.DomainModificationImpact = libvirt.DOMAIN_AFFECT_CONFIG
	)
	xmlstr, err = meta.To_xml(op, state, errstr, time.Now().UTC().UnixMilli())
	if (err != nil) {
		return err
	}
	err = domain.SetMetadata(libvirt.DOMAIN_METADATA_ELEMENT, string(xmlstr),
		meta.XMLName.Local, meta.XMLName.Space, impact)
	if (err != nil) {
		return err
	}
	return nil
}

/* load the record from the domain XML */
func load_domain_op(domain *libvirt.Domain, op *openapi.Operation, state *openapi.OperationState, errstr *string, ts *int64) error {
	var (
		err error
		xmlstr string
		meta metadata.Operation
		impact libvirt.DomainModificationImpact = libvirt.DOMAIN_AFFECT_CONFIG
	)
	xmlstr, err = domain.GetMetadata(libvirt.DOMAIN_METADATA_ELEMENT, "virtx-op", impact)
	if (err != nil) {
		return err
	}
	err = meta.From_xml(xmlstr, op, state, errstr, ts)
	if (err != nil) {
		return err
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
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return info, err
	}
	domain, err = conn.LookupDomainByUUIDString(uuid)
	if (err != nil) {
		return info, err
	}
	defer domain.Free()

	/*
	 * just doing query-migrate is not enough due to the interactions
	 * between libvirt and QEMU. An error on the libvirt side only
	 * is not known to QEMU, so it might be happily reporting info
	 * about an old migration, just as an example.
	 *
	 * So, check instead the virtx migration operation record first.
	 */
	var (
		op openapi.Operation
		state openapi.OperationState
		errstr string
		ts int64
	)
	err = load_domain_op(domain, &op, &state, &errstr, &ts)
	if (err != nil) {
		return info, err
	}
	if (op != openapi.OpVmMigrate) {
		return info, errors.New("Get_migration_info: no OpVmMigrate operation")
	}
	switch (state) {
	case openapi.OPERATION_FAILED:
		info.State = openapi.MIGRATION_FAILED
		return info, nil
	case openapi.OPERATION_COMPLETED:
		info.State = openapi.MIGRATION_COMPLETED
		return info, nil
	}
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
	err = info.State.Parse(qemu_info.R.Status)
	if (err != nil) {
		return info, err
	}
	info.Progress.Total = qemu_info.R.Ram.Total
	info.Progress.Remaining = qemu_info.R.Ram.Remaining
	info.Progress.Transferred = qemu_info.R.Ram.Transferred
	info.Progress.Rate = float32(qemu_info.R.Ram.Mbps / 8)
	return info, nil
}

func Abort_migration(uuid string) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return err
	}
	domain, err = conn.LookupDomainByUUIDString(uuid)
	if (err != nil) {
		return err
	}
	defer domain.Free()
	/*
	 * migrate_cancel always returns success, whether a migration is ongoing or not.
	 *
	 * So, check instead the virtx migration operation record first.
	 */
	var (
		op openapi.Operation
		state openapi.OperationState
		errstr string
		ts int64
	)
	err = load_domain_op(domain, &op, &state, &errstr, &ts)
	if (err != nil) {
		return err
	}
	if (op != openapi.OpVmMigrate) {
		return errors.New("Abort_migration: no OpVmMigrate operation")
	}
	switch (state) {
	case openapi.OPERATION_FAILED:
		return errors.New("Abort_migration: migration already ended (FAILED)")
	case openapi.OPERATION_COMPLETED:
		return errors.New("Abort_migration: migration already ended (COMPLETED)")
	case openapi.OPERATION_STARTED:
		_, err = domain.QemuMonitorCommand(
			"{ \"execute\": \"migrate_cancel\" }",
			libvirt.DOMAIN_QEMU_MONITOR_COMMAND_DEFAULT,
		)
		return err
	}
	return errors.New("Abort_migration: unknown operation state")
}

func Dumpxml(uuid string) (string, error) {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		xml string
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return "", err
	}
	defer conn.Close()
	domain, err = conn.LookupDomainByUUIDString(uuid)
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
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	domain, err = conn.LookupDomainByUUIDString(uuid)
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
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	domain, err = conn.LookupDomainByUUIDString(uuid)
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
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	domain, err = conn.LookupDomainByUUIDString(uuid)
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
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	domain, err = conn.LookupDomainByUUIDString(uuid)
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
	)
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	domain, err = conn.LookupDomainByUUIDString(uuid)
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

/* Return the libvirt domain Events Channel */
func GetVmEventCh() (chan inventory.VmEvent) {
	return hv.vm_event_ch
}

/* Return the systemInfo Events Channel */
func GetSystemInfoCh() (chan SystemInfo) {
	return hv.system_info_ch
}

func init_vm_event_loop() {
	var err error
	logger.Debug("init_vm_event_loop: Entering")
	for {
		err = libvirt.EventRunDefaultImpl()
		if (err != nil) {
			panic(err)
		}
	}
	logger.Debug("init_vm_event_loop: Exiting")
}

func init_system_info_loop() {
	logger.Debug("init_system_info_loop: Waiting for a libvirt connection...")
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
				logger.Debug("reconnect, attempt every %d seconds...", libvirt_reconnect_seconds)
				for ; err != nil; err = Connect() {
					time.Sleep(time.Duration(libvirt_reconnect_seconds) * time.Second)
				}
			}
		} else {
			logger.Log(err.Error())
		}
	}
	logger.Debug("init_system_info_loop: Exiting")
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
	logger.Debug("init, vcpu_load_factor %f", hv.vcpu_load_factor)
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
		host string
		uuid string
		uuids []string
		conn *libvirt.Connect
	)
	err = os.MkdirAll(fmt.Sprintf("%s/%s", REG_DIR, host_uuid), 0750)
	if (err != nil) {
		logger.Fatal("could not create %s/%s: %s", REG_DIR, host_uuid, err.Error())
	}
	/* check that all vms in libvirt are registered in vmreg, and in the correct host only */
	for uuid, _ = range(si.Vms) {
		uuids, err = vmreg.Hosts()
		for _, host = range(uuids) {
			err = vmreg.Access(host, uuid)
			if (host == host_uuid) {
				/* this is our own host directory. The vm should be registered here. */
				if (err == nil) {
					/* yes, ok: it is registered here */
					continue
				}
				if (!os.IsNotExist(err)) {
					/* it's here but it is not accessible (perm issues?) */
					logger.Fatal("could not access file in %s/%s: %s", REG_DIR, host_uuid, err.Error())
				}
				/* os.IsNotExist */
				logger.Log("WARNING: local libvirt domain %s/%s is not registered in vmreg", host_uuid, uuid)
			} else {
				/* this is not our own host directory. We should NOT find the VM here. */
				if (err != nil && os.IsNotExist(err)) {
					/* all ok, our vm is not in this host */
					continue
				}
				if (err == nil) {
					logger.Fatal("local libvirt domain %s is registered in remote host %s", uuid, host)
				} else {
					logger.Fatal("local libvirt domain %s may be registered in remote host %s and is not accessible", uuid, host)
				}
			}
		}
	}
	/* now check that all vms in vmreg are registered in libvirt */
	uuids, err = vmreg.Uuids(host_uuid)
	if (err != nil) {
		logger.Fatal("could not get the list of VM uuids for host %s", host_uuid)
	}
	conn, err = libvirt.NewConnect(libvirt_uri)
	if (err != nil) {
		logger.Fatal("could not connect to libvirt: %s", err.Error())
	}
	defer conn.Close()

	for _, uuid = range(uuids) {
		var domain *libvirt.Domain
		domain, err = conn.LookupDomainByUUIDString(uuid)
		if (err != nil) {
			logger.Log("WARNING: vmreg VM %s/%s is not registered in libvirt", host_uuid, uuid)
		} else {
			domain.Free()
		}
	}
}
