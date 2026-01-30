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
	"sync"
	"sync/atomic"
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
}

func init_system_info_loop() {
	logger.Debug("init_system_info_loop: Waiting for a libvirt connection...")
	for ; hv.is_connected.Load() == false; {
		time.Sleep(time.Duration(1) * time.Second)
	}
	for {
		var err error
		err = system_info_loop(libvirt_system_info_seconds)
		/* we should from system_info_loop only if there is a libvirt error that requires reconnection */
		/* assert(err != nil) */
		logger.Debug("reconnect, attempt every %d seconds...", libvirt_reconnect_seconds)
		for ; err != nil; err = Connect() {
			time.Sleep(time.Duration(libvirt_reconnect_seconds) * time.Second)
		}
	}
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
