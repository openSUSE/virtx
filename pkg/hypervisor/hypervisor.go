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
	"sync"
	"sync/atomic"
	"os"
	"bytes"
	"bufio"
	"strings"
	"strconv"
	"fmt"
	"errors"
	"regexp"

	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/vmlog"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/cloudinit"
	"suse.com/virtx/pkg/ts"

	. "suse.com/virtx/pkg/constants"
)

const (
	MAX_FREQ_PATH = "/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq"
	LIBVIRT_URI = "qemu:///system"
	LIBVIRT_RECONNECT_SECONDS = 5
	SYSTEM_INFO_LOOP_SECONDS = 15
	WAIT_SYSTEM_INFO_SECONDS = 10
)

type Hypervisor struct {
	is_connected atomic.Bool
	m sync.RWMutex

	conn *libvirt.Connect
	lifecycle_id int
	watchdog_id int
	ioerror_id int
	reboot_id int
	control_id int
	memory_id int

	vm_event_ch chan inventory.VmEvent
	system_info_ch chan SystemInfo
	system_info_loop_done atomic.Bool

	vcpu_load_factor float64
	si *SystemInfo
}
var hv = Hypervisor{
	m: sync.RWMutex{},
	lifecycle_id: -1,
	watchdog_id: -1,
	ioerror_id: -1,
	reboot_id: -1,
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
	conn, err := libvirt.NewConnect(LIBVIRT_URI)
	if (err != nil) {
		return err
	}
	hv.conn = conn
	hv.is_connected.Store(true)
	err = start_listening()
	return err
}

/*
 * system_info_init() initializes important information, including the host
 * libvirt Uuid and CPU arch.
 * We need for this information to be available, before we attempt to
 * initialize the other packages that depend on this information.
 */

func Wait_system_info() error {
	logger.Debug("waiting for hypervisor system_info...")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	timeout := time.After(time.Duration(WAIT_SYSTEM_INFO_SECONDS) * time.Second)
	for {
		select {
		case <- ticker.C:
			if (get_system_info_loop_done()) {
				logger.Debug("system_info now available.")
				return nil
			}
		case <- timeout:
			return errors.New("timeout waiting for system_info")
		}
	}
}

func lifecycle_cb(_ *libvirt.Connect, d *libvirt.Domain, e *libvirt.DomainEventLifecycle) {
	/* e.Detail: see all DomainEvent*DetailType types */
	var (
		vi inventory.VmInfo
		persistent, pending bool
		err error
	)
	if (!hv.system_info_loop_done.Load()) {
		/*
		 * too early, we cannot generate events for our host.
		 * For one, we will not be able to set the host uuid properly.
		 */
		return
	}
	if (e.Event == libvirt.DOMAIN_EVENT_UNDEFINED) {
		/* VM has been DELETED */
		vi.Uuid, err = d.GetUUIDString()
		if (err != nil) {
			logger.Log("lifecycle_cb: GetUUIDString error: %s", err.Error())
			return
		}
		vi.Runstate = openapi.RUNSTATE_DELETED
		vi.Host = machine.Uuid()
	} else {
		persistent, err = d.IsPersistent()
		if (err != nil) {
			logger.Log("lifecycle_cb: IsPersistent err: %s", err.Error())
			return
		}
		if (!persistent) {
			return /* ignore transient domains (ongoing migrations) */
		}
		err = get_domain_event(d, &vi.VmEvent)
		if (err != nil) {
			logger.Log("lifecycle_cb: event %d: %s:", e.Event, err.Error())
		}
	}
	vi.Ts = ts.Now()
	if (e.Event == libvirt.DOMAIN_EVENT_DEFINED) {
		var (
			si SystemInfo
			vm SystemInfoVm
		)
		si.Host.Uuid = "" /* not necessary, but for documentation, do not send Host Data */
		si.Vms = make(SystemInfoVms)
		err = get_domain_details(d, &vi.VmDetails)
		if (err != nil) {
			logger.Log("lifecycle_cb: failed to get_domain_details for uuid %s", vi.Uuid)
		} else {
			vm.VmInfo = vi
			err = get_domain_stats(d, &vm, nil, &si.imm)
			if (err != nil) {
				logger.Log("lifecycle_cb: failed to get_domain_stats for uuid %s", vi.Uuid)
			} else {
				si.Vms[vm.Uuid] = vm
				hv.system_info_ch <- si
			}
		}
	} else if (vi.Runstate != openapi.RUNSTATE_NONE) {
		logger.Debug("[VmEvent] %s: %v state: %d", vi.Uuid, e, vi.Runstate)
		hv.vm_event_ch <- vi.VmEvent
	}
	if (e.Event == libvirt.DOMAIN_EVENT_STOPPED) {
		var msg string
		switch (e.Detail) {
		case int(libvirt.DOMAIN_EVENT_STOPPED_DESTROYED):
			msg = "Shutdown (forced)."
		case int(libvirt.DOMAIN_EVENT_STOPPED_SHUTDOWN):
			msg = "Shutdown (graceful)."
		}
		/* complete the pending vm_shutdown, if any: a guest-initiated shutdown has none */
		if (msg != "") {
			pending, err = vmlog.Pending(vi.Uuid, openapi.OpVmShutdown)
			if (err != nil) {
				logger.Log("lifecycle_cb: vmlog.Pending: %s", err.Error())
			} else if (pending) {
				err = vmlog.Complete(vi.Uuid, openapi.OpVmShutdown, msg)
				if (err != nil) {
					logger.Log("lifecycle_cb: vmlog.Complete: %s", err.Error())
				}
			}
		}
	}
	/* Events */
	switch (e.Event) {
	case libvirt.DOMAIN_EVENT_CRASHED:
		err = vmlog.Event(vi.Uuid, openapi.EVENT_PANIC, "Guest crashed.")
		if (err != nil) {
			logger.Log("lifecycle_cb: vmlog.Event: %s", err.Error())
		}
	case libvirt.DOMAIN_EVENT_STOPPED:
		switch (e.Detail) {
		case int(libvirt.DOMAIN_EVENT_STOPPED_CRASHED):
			err = vmlog.Event(vi.Uuid, openapi.EVENT_CRASH, "QEMU crashed.")
			if (err != nil) {
				logger.Log("lifecycle_cb: vmlog.Event: %s", err.Error())
			}
		case int(libvirt.DOMAIN_EVENT_STOPPED_FAILED):
			err = vmlog.Event(vi.Uuid, openapi.EVENT_CRASH, "QEMU failed.")
			if (err != nil) {
				logger.Log("lifecycle_cb: vmlog.Event: %s", err.Error())
			}
		}
	case libvirt.DOMAIN_EVENT_SHUTDOWN:
		/*
		 * GUEST also covers a vm_shutdown via ACPI, since the guest then
		 * shuts down by itself: it is a guest event only if no vm_shutdown is pending.
		 */
		if (e.Detail == int(libvirt.DOMAIN_EVENT_SHUTDOWN_GUEST)) {
			pending, err = vmlog.Pending(vi.Uuid, openapi.OpVmShutdown)
			if (err != nil) {
				logger.Log("lifecycle_cb: vmlog.Pending: %s", err.Error())
			} else if (!pending) {
				err = vmlog.Event(vi.Uuid, openapi.EVENT_SHUTDOWN, "Graceful.")
				if (err != nil) {
					logger.Log("lifecycle_cb: vmlog.Event: %s", err.Error())
				}
			}
		}
	}
	/* check for the need to remove a cloudinit disk resource file */
	if ((e.Event == libvirt.DOMAIN_EVENT_STOPPED && e.Detail != int(libvirt.DOMAIN_EVENT_STOPPED_MIGRATED)) ||
		e.Event == libvirt.DOMAIN_EVENT_CRASHED) {
		/*
		 * XXX
		 * - what should we do for DOMAIN_EVENT_STOPPED_SAVED?
		 *   we don't really use save or managedsave, so a bit theoretical.
		 *
		 * - what should we do for DOMAIN_EVENT_STOPPED_FAILED? This is a problem.
		 *   It is generated in two completely different scenarios:
		 *
		 *   1) a failed migration, generated on the destination, where we had to terminate QEMU in order to
		 *      allow the source QEMU to resume. We do not want to remove the ISO in this case.
		 *
		 *   2) The second scenario is src/qemu_driver.c::processMonitorEOFEvent(), when the monitor is closed
		 *      unexpectedly and libvirt assumes the domain has crashed.
		 *
		 *   The rely on the assumption here that we will not be getting the DOMAIN_EVENT_STOPPED_FAILED in the
		 *   migration failure case on the destination since the domain is not persisted yet, and we only
		 *   consider lifecycle events for persisted domains. See code above:
		 *   if (!persistent) {
		 *       return // ignore transient domains (ongoing migrations)
		 *   }
		 *   Needs to be tested.
		 *   XXX
		 */
		err = cloudinit.Delete_disk(vi.Uuid)
		if (err != nil) {
			logger.Log("lifecycle_cb: cloudinit : %s", err)
		}
	}
}

func watchdog_action_string(a libvirt.DomainEventWatchdogAction) string {
	switch (a) {
	case libvirt.DOMAIN_EVENT_WATCHDOG_PAUSE:
		return "pause"
	case libvirt.DOMAIN_EVENT_WATCHDOG_RESET:
		return "reset"
	case libvirt.DOMAIN_EVENT_WATCHDOG_POWEROFF:
		return "poweroff"
	case libvirt.DOMAIN_EVENT_WATCHDOG_SHUTDOWN:
		return "shutdown"
	case libvirt.DOMAIN_EVENT_WATCHDOG_DEBUG:
		return "debug"
	case libvirt.DOMAIN_EVENT_WATCHDOG_INJECTNMI:
		return "injectnmi"
	default:
		return "none"
	}
}

func watchdog_cb(_ *libvirt.Connect, d *libvirt.Domain, e *libvirt.DomainEventWatchdog) {
	var (
		persistent bool
		uuid, msg string
		err error
	)
	if (!hv.system_info_loop_done.Load()) {
		return
	}
	persistent, err = d.IsPersistent()
	if (err != nil) {
		logger.Log("watchdog_cb: IsPersistent: %s", err.Error())
		return
	}
	if (!persistent) {
		return
	}
	uuid, err = d.GetUUIDString()
	if (err != nil) {
		logger.Log("watchdog_cb: GetUUIDString: %s", err.Error())
		return
	}
	msg = fmt.Sprintf("Watchdog triggered (action: %s).", watchdog_action_string(e.Action))
	err = vmlog.Event(uuid, openapi.EVENT_WATCHDOG, msg)
	if (err != nil) {
		logger.Log("watchdog_cb: vmlog.Event: %s", err.Error())
	}
}

func ioerror_cb(_ *libvirt.Connect, d *libvirt.Domain, e *libvirt.DomainEventIOErrorReason) {
	var (
		persistent bool
		uuid, msg string
		err error
	)
	if (!hv.system_info_loop_done.Load()) {
		return
	}
	persistent, err = d.IsPersistent()
	if (err != nil) {
		logger.Log("ioerror_cb: IsPersistent: %s", err.Error())
		return
	}
	if (!persistent) {
		return
	}
	uuid, err = d.GetUUIDString()
	if (err != nil) {
		logger.Log("ioerror_cb: GetUUIDString: %s", err.Error())
		return
	}
	msg = fmt.Sprintf("Storage I/O error on %s (%s): %s", e.DevAlias, e.SrcPath, e.Reason)
	err = vmlog.Event(uuid, openapi.EVENT_STORAGE, msg)
	if (err != nil) {
		logger.Log("ioerror_cb: vmlog.Event: %s", err.Error())
	}
}

/*
 * reboot_cb has no detail: libvirt drops the reason of the QEMU reset. VirtX
 * has no reboot operation, so any reboot is a guest event.
 */
func reboot_cb(_ *libvirt.Connect, d *libvirt.Domain) {
	var (
		persistent bool
		uuid string
		err error
	)
	if (!hv.system_info_loop_done.Load()) {
		return
	}
	persistent, err = d.IsPersistent()
	if (err != nil) {
		logger.Log("reboot_cb: IsPersistent: %s", err.Error())
		return
	}
	if (!persistent) {
		return
	}
	uuid, err = d.GetUUIDString()
	if (err != nil) {
		logger.Log("reboot_cb: GetUUIDString: %s", err.Error())
		return
	}
	err = vmlog.Event(uuid, openapi.EVENT_REBOOT, "Reset.")
	if (err != nil) {
		logger.Log("reboot_cb: vmlog.Event: %s", err.Error())
	}
}

/* control_cb: libvirt lost control of the domain, ie the QEMU monitor failed */
func control_cb(_ *libvirt.Connect, d *libvirt.Domain) {
	var (
		persistent bool
		uuid string
		err error
	)
	if (!hv.system_info_loop_done.Load()) {
		return
	}
	persistent, err = d.IsPersistent()
	if (err != nil) {
		logger.Log("control_cb: IsPersistent: %s", err.Error())
		return
	}
	if (!persistent) {
		return
	}
	uuid, err = d.GetUUIDString()
	if (err != nil) {
		logger.Log("control_cb: GetUUIDString: %s", err.Error())
		return
	}
	err = vmlog.Event(uuid, openapi.EVENT_MONITOR, "QEMU Monitor error.")
	if (err != nil) {
		logger.Log("control_cb: vmlog.Event: %s", err.Error())
	}
}

func memory_failure_string(e *libvirt.DomainEventMemoryFailure) string {
	var recipient, action, flags string
	switch (e.Recipient) {
	case libvirt.DOMAIN_EVENT_MEMORY_FAILURE_RECIPIENT_HYPERVISOR:
		recipient = "hypervisor"
	case libvirt.DOMAIN_EVENT_MEMORY_FAILURE_RECIPIENT_GUEST:
		recipient = "guest"
	}
	switch (e.Action) {
	case libvirt.DOMAIN_EVENT_MEMORY_FAILURE_ACTION_IGNORE:
		action = "ignore"
	case libvirt.DOMAIN_EVENT_MEMORY_FAILURE_ACTION_INJECT:
		action = "inject"
	case libvirt.DOMAIN_EVENT_MEMORY_FAILURE_ACTION_FATAL:
		action = "fatal"
	case libvirt.DOMAIN_EVENT_MEMORY_FAILURE_ACTION_RESET:
		action = "reset"
	}
	if (e.Flags & libvirt.DOMAIN_MEMORY_FAILURE_ACTION_REQUIRED != 0) {
		flags += ", action required"
	}
	if (e.Flags & libvirt.DOMAIN_MEMORY_FAILURE_RECURSIVE != 0) {
		flags += ", recursive"
	}
	return fmt.Sprintf("Memory failure (recipient: %s, action: %s%s).", recipient, action, flags)
}

/* memory_cb: a hardware memory error (machine check) affected the VM */
func memory_cb(_ *libvirt.Connect, d *libvirt.Domain, e *libvirt.DomainEventMemoryFailure) {
	var (
		persistent bool
		uuid string
		err error
	)
	if (!hv.system_info_loop_done.Load()) {
		return
	}
	persistent, err = d.IsPersistent()
	if (err != nil) {
		logger.Log("memory_cb: IsPersistent: %s", err.Error())
		return
	}
	if (!persistent) {
		return
	}
	uuid, err = d.GetUUIDString()
	if (err != nil) {
		logger.Log("memory_cb: GetUUIDString: %s", err.Error())
		return
	}
	err = vmlog.Event(uuid, openapi.EVENT_MEMORY, memory_failure_string(e))
	if (err != nil) {
		logger.Log("memory_cb: vmlog.Event: %s", err.Error())
	}
}

func start_listening() error {
	/* assert(hv.m.IsLocked()) */
	var err error
	hv.lifecycle_id, err = hv.conn.DomainEventLifecycleRegister(nil, lifecycle_cb)
	if (err != nil) {
		return err
	}
	hv.watchdog_id, err = hv.conn.DomainEventWatchdogRegister(nil, watchdog_cb)
	if (err != nil) {
		return err
	}
	hv.ioerror_id, err = hv.conn.DomainEventIOErrorReasonRegister(nil, ioerror_cb)
	if (err != nil) {
		return err
	}
	hv.reboot_id, err = hv.conn.DomainEventRebootRegister(nil, reboot_cb)
	if (err != nil) {
		return err
	}
	hv.control_id, err = hv.conn.DomainEventControlErrorRegister(nil, control_cb)
	if (err != nil) {
		return err
	}
	hv.memory_id, err = hv.conn.DomainEventMemoryFailureRegister(nil, memory_cb)
	if (err != nil) {
		return err
	}
	return nil
}

func stop_listening() {
	/* assert(hv.m.IsLocked()) */
	if (hv.lifecycle_id >= 0) {
		_ = hv.conn.DomainEventDeregister(hv.lifecycle_id)
		hv.lifecycle_id = -1
	}
	if (hv.watchdog_id >= 0) {
		_ = hv.conn.DomainEventDeregister(hv.watchdog_id)
		hv.watchdog_id = -1
	}
	if (hv.ioerror_id >= 0) {
		_ = hv.conn.DomainEventDeregister(hv.ioerror_id)
		hv.ioerror_id = -1
	}
	if (hv.reboot_id >= 0) {
		_ = hv.conn.DomainEventDeregister(hv.reboot_id)
		hv.reboot_id = -1
	}
	if (hv.control_id >= 0) {
		_ = hv.conn.DomainEventDeregister(hv.control_id)
		hv.control_id = -1
	}
	if (hv.memory_id >= 0) {
		_ = hv.conn.DomainEventDeregister(hv.memory_id)
		hv.memory_id = -1
	}
}

/* Return the libvirt domain Events Channel */
func Get_vm_event_channel() (chan inventory.VmEvent) {
	return hv.vm_event_ch
}

/* Return the systemInfo Events Channel */
func Get_system_info_channel() (chan SystemInfo) {
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
		err = system_info_loop(SYSTEM_INFO_LOOP_SECONDS)
		/* we should from system_info_loop only if there is a libvirt error that requires reconnection */
		/* assert(err != nil) */
		logger.Log("reconnect, attempt every %d seconds...", LIBVIRT_RECONNECT_SECONDS)
		for ; err != nil; err = Connect() {
			time.Sleep(time.Duration(LIBVIRT_RECONNECT_SECONDS) * time.Second)
		}
		logger.Log("reconnected.")
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

func check_reg(host_uuid string, si *SystemInfo) {
	var (
		err error
		host string
		hosts []string
		uuid string
		uuids []string
		registered map[string]bool = make(map[string]bool)
		present bool
	)
	err = os.MkdirAll(fmt.Sprintf("%s/%s", REG_DIR, host_uuid), 0750)
	if (err != nil) {
		logger.Fatal("could not create %s/%s: %s", REG_DIR, host_uuid, err.Error())
	}
	hosts, err = reg.Hosts()
	if (err != nil) {
		logger.Fatal("could not get list of hosts: %s", err.Error())
	}
	/* read each host directory once, and compare its vms with the vms in libvirt */
	for _, host = range(hosts) {
		uuids, err = reg.Uuids(host)
		if (err != nil) {
			logger.Fatal("could not get the list of VM uuids for host %s: %s", host, err.Error())
		}
		for _, uuid = range(uuids) {
			_, present = si.Vms[uuid]
			if (host == host_uuid) {
				/* our own host directory: the vm should be in libvirt */
				registered[uuid] = true
				if (!present) {
					logger.Log("WARNING: reg VM %s/%s is not registered in libvirt", host_uuid, uuid)
				}
			} else if (present) {
				/* another host directory: our libvirt vm must not be registered here */
				logger.Fatal("local libvirt domain %s is registered in remote host %s", uuid, host)
			}
		}
	}
	/* all vms in libvirt should be registered in our own host directory */
	for uuid = range(si.Vms) {
		if (!registered[uuid]) {
			logger.Log("WARNING: local libvirt domain %s/%s is not registered in reg", host_uuid, uuid)
		}
	}
}

func Get_vmstats(uuid string) (openapi.Vmstats, error) {
	hv.m.RLock()
	defer hv.m.RUnlock()

	return system_info_get_vmstats(hv.si, uuid)
}

/* assert hv.si != nil, guaranteed by system_info_init() before HTTP starts */
func Get_host() openapi.Host {
	hv.m.RLock()
	defer hv.m.RUnlock()
	return system_info_get_host(hv.si)
}

/* assert hv.si != nil, guaranteed by system_info_init() before serfcomm connects */
/* assert hv.si != nil, guaranteed by system_info_init() before HTTP starts */
func Get_hoststats() (openapi.Hoststats) {
	hv.m.RLock()
	defer hv.m.RUnlock()

	return system_info_get_hoststats(hv.si)
}

var cpumodel_suffix *regexp.Regexp = regexp.MustCompile(CPUMODEL_VER)

func Get_cpumodels(arch string) ([]string, error) {
	hv.m.RLock()
	defer hv.m.RUnlock()
	var (
		xml_data string
		caps libvirtxml.DomainCaps
		models []string
		err error
	)
	xml_data, err = hv.conn.GetDomainCapabilities("", arch, "", "kvm", 0)
	if (err != nil) {
		return models, err
	}
	err = caps.Unmarshal(xml_data)
	if (err != nil) {
		return models, err
	}
	if (caps.CPU == nil) {
		return models, errors.New("no CPU section in domain capabilities")
	}
	for _, mode := range caps.CPU.Modes {
		if (mode.Name != "custom" || mode.Supported != "yes") {
			continue
		}
		for _, model := range mode.Models {
			if (model.Usable == "yes" && cpumodel_suffix.MatchString(model.Name)) {
				models = append(models, model.Name)
			}
		}
	}
	return models, nil
}
