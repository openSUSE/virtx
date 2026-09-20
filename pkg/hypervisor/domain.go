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
	"encoding/json"
	"errors"
	"fmt"

	"libvirt.org/go/libvirt"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/oplog"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/metadata"
)

/*
 * get_domain_event fills the runstate essentials (Uuid, Runstate, Host) of a Domain.
 *
 * Note that the timestamp is not set (ve.Ts). This is left to the caller, because in
 * most cases for a systeminfo collection we want to keep the same timestamp for all
 * related systeminfovms, equal to the host one. This is easier for debugging.
 * We also want to avoid tight loops calling gettimeofday() just to update the Ts for all
 * the VMs, if we have a large number of them.
 */
func get_domain_event(d *libvirt.Domain, ve *inventory.VmEvent) error {
	/* assert (hv.m.IsRLocked) */
	var (
		reason int
		state libvirt.DomainState
		err error
	)
	ve.Uuid, err = d.GetUUIDString()
	if (err != nil) {
		return err
	}
	state, reason, err = d.GetState()
	if (err != nil) {
		return err
	}
	logger.Debug("get_domain_event: state %d, reason %d", state, reason)
	switch (state) {
	//case libvirt.DOMAIN_NOSTATE: /* leave ve.Runstate RUNSTATE_NONE */
	case libvirt.DOMAIN_RUNNING:
		ve.Runstate = openapi.RUNSTATE_RUNNING
	case libvirt.DOMAIN_BLOCKED: /* should be Xen only IIUC */
		logger.Log("XXX DOMAIN_BLOCKED encountered XXX")
		ve.Runstate = openapi.RUNSTATE_PAUSED
	case libvirt.DOMAIN_PAUSED:
		switch (reason) {
		case int(libvirt.DOMAIN_PAUSED_MIGRATION): /* paused for offline migration */
			ve.Runstate = openapi.RUNSTATE_MIGRATING
		case int(libvirt.DOMAIN_PAUSED_SHUTTING_DOWN):
			ve.Runstate = openapi.RUNSTATE_TERMINATING
		case int(libvirt.DOMAIN_PAUSED_CRASHED):
			ve.Runstate = openapi.RUNSTATE_CRASHED
		case int(libvirt.DOMAIN_PAUSED_STARTING_UP):
			ve.Runstate = openapi.RUNSTATE_STARTUP
		default:
			ve.Runstate = openapi.RUNSTATE_PAUSED
		}
	case libvirt.DOMAIN_SHUTDOWN:
		ve.Runstate = openapi.RUNSTATE_TERMINATING
	case libvirt.DOMAIN_SHUTOFF:
		switch (reason) {
		case int(libvirt.DOMAIN_SHUTOFF_CRASHED):
			ve.Runstate = openapi.RUNSTATE_CRASHED
		case int(libvirt.DOMAIN_SHUTOFF_MIGRATED):
			/* XXX I started to see this in my migration tests since 16.1 XXX */
			logger.Log("XXX DOMAIN_SHUTOFF_MIGRATED encountered, started to see since 16.1 XXX")
			ve.Runstate = openapi.RUNSTATE_MIGRATING
		case int(libvirt.DOMAIN_SHUTOFF_DESTROYED):
			err = oplog.Complete(ve.Uuid, openapi.OpVmShutdown, "forced shutdown")
			if (err != nil) {
				logger.Log("get_domain_event: oplog.Complete: %s", err.Error())
			}
			ve.Runstate = openapi.RUNSTATE_POWEROFF
		case int(libvirt.DOMAIN_SHUTOFF_SHUTDOWN):
			err = oplog.Complete(ve.Uuid, openapi.OpVmShutdown, "graceful shutdown")
			if (err != nil) {
				logger.Log("get_domain_event: oplog.Complete: %s", err.Error())
			}
			fallthrough
		default:
			ve.Runstate = openapi.RUNSTATE_POWEROFF
		}
	case libvirt.DOMAIN_CRASHED:
		ve.Runstate = openapi.RUNSTATE_CRASHED
	case libvirt.DOMAIN_PMSUSPENDED:
		ve.Runstate = openapi.RUNSTATE_PMSUSPENDED
	default:
		logger.Log("Unhandled state %d, reason %d", state, reason)
	}
	ve.Host = machine.Uuid()
	return nil
}

/*
 * get_domain_details fills the inventory.VmDetails read from the domain metadata: the
 * Name and the Custom fields. A domain without virtx-vm metadata is not an error, the
 * Custom fields will just be empty.
 */
func get_domain_details(d *libvirt.Domain, vd *inventory.VmDetails) error {
	/* assert (hv.m.IsRLocked) */
	var (
		meta metadata.Vm
		meta_xml string
		err error
	)
	vd.Name, err = d.GetMetadata(libvirt.DOMAIN_METADATA_TITLE, "", libvirt.DOMAIN_AFFECT_CONFIG)
	if (err != nil) {
		return err
	}
	meta_xml, err = d.GetMetadata(libvirt.DOMAIN_METADATA_ELEMENT, "virtx-vm", libvirt.DOMAIN_AFFECT_CONFIG)
	if (err != nil) {
		return nil
	}
	return meta.From_xml(meta_xml, &vd.Custom)
}

func Define_domain(xml string, uuid string) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
	)
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
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
	/* store the processed XML in /vms/reg/host-uuid/vm-uuid/vm-uuid.xml */
	err = reg.Save(machine.Uuid(), uuid, xml)
	if (err != nil) {
		logger.Log("Define_domain: failed to reg.Save(%s, %s)", machine.Uuid(), uuid)
	}
	return nil
}

func Migrate_domain(hostname string, migration_addr string, host_uuid string, host_old string, uuid string, live bool) error {
	var (
		err error
		conn, conn2 *libvirt.Connect
		domain, domain2 *libvirt.Domain
		params libvirt.DomainMigrateParameters
		flags libvirt.DomainMigrateFlags
		msg string
	)
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	domain, err = conn.LookupDomainByUUIDString(uuid)
	if (err != nil) {
		return err
	}
	defer domain.Free()
	if (migration_addr != "") {
		params.URI = "tcp://" + migration_addr
	} else {
		params.URI = "tcp://" + hostname
	}
	params.URISet = true
	if (live) {
		var info *libvirt.DomainInfo
		info, err = domain.GetInfo()
		if (err != nil) {
			return err
		}
		var vcpus int = int(info.NrVirtCpu)
		params.ParallelConnectionsSet = true
		if (vcpus < 1) {
			params.ParallelConnections = 1
		} else if (vcpus > 8) {
			params.ParallelConnections = 8
		} else {
			params.ParallelConnections = vcpus
		}
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
	logger.Debug("Migrate_domain: params=%+v flags=%+v", params, flags)
	conn2, err = libvirt.NewConnect("qemu+tcp://" + hostname + "/system")
	if (err != nil) {
		return err
	}
	defer conn2.Close()
	if (live) {
		msg = "live"
	} else {
		msg = "offline"
	}
	msg += fmt.Sprintf(" migration from %s to %s.", host_old, host_uuid)
	oplog_off, oplog_err := oplog.Start(uuid, openapi.OpVmMigrate, msg)
	defer func() {
		if (oplog_err != nil) {
			logger.Log("Migrate_domain: oplog: %s", oplog_err.Error())
		}
	}()
	domain2, err = domain.Migrate3(conn2, &params, flags)
	if (err != nil) {
		logger.Log("Migrate_domain: failed to Migrate3: %s", err.Error())
		if (oplog_err == nil) {
			oplog_err = oplog.End(uuid, openapi.OpVmMigrate, openapi.OPERATION_FAILED, err.Error(), oplog_off)
		}
		return err
	}
	defer domain2.Free()
	/*
	 * log COMPLETED before reg.Move so that machine.Uuid() is still the
	 * correct host (the file moves with the VM directory in the rename).
	 */
	if (oplog_err == nil) {
		oplog_err = oplog.End(uuid, openapi.OpVmMigrate, openapi.OPERATION_COMPLETED, "Migrated.", oplog_off)
	}
	/* move the per-VM directory to /vms/reg/host_uuid/uuid/ (carries the oplog files with it) */
	err = reg.Move(host_uuid, host_old, uuid)
	if (err != nil) {
		logger.Log("Migrate_domain: failed to reg.Move(%s, %s, %s)", host_uuid, host_old, uuid)
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
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
	if (err != nil) {
		return info, err
	}
	defer conn.Close()
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
		op openapi.Operation = openapi.OpVmMigrate
		state openapi.OperationState
		msgs, msge string
		mts, tse int64
	)
	err = oplog.Load_last(uuid, op, &state, &msgs, &msge, &mts, &tse)
	if (err != nil) {
		return info, err
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
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
	if (err != nil) {
		return err
	}
	defer conn.Close()
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
		op openapi.Operation = openapi.OpVmMigrate
		state openapi.OperationState
		msgs, msge string
		mts, tse int64
	)
	err = oplog.Load_last(uuid, op, &state, &msgs, &msge, &mts, &tse)
	if (err != nil) {
		return err
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
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
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

func Boot_domain(uuid string, o *openapi.VmBootOptions) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		op openapi.Operation = openapi.OpVmBoot
	)
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	domain, err = conn.LookupDomainByUUIDString(uuid)
	if (err != nil) {
		return err
	}
	defer domain.Free()
	oplog_off, oplog_err := oplog.Start(uuid, op, "")
	defer func() {
		if (oplog_err != nil) {
			logger.Log("Boot_domain: oplog: %s", oplog_err.Error())
		}
	}()
	if (len(o.CloudInit) > 0) {
		err = cloudinit_boot_domain(uuid, domain, o.CloudInit)
	} else {
		err = domain.Create()
	}
	if (err != nil) {
		if (oplog_err == nil) {
			oplog_err = oplog.End(uuid, op, openapi.OPERATION_FAILED, err.Error(), oplog_off)
		}
		return err
	}
	if (oplog_err == nil) {
		oplog_err = oplog.End(uuid, op, openapi.OPERATION_COMPLETED, "", oplog_off)
	}
	return nil
}

func Pause_domain(uuid string) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		op openapi.Operation = openapi.OpVmPause
	)
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	domain, err = conn.LookupDomainByUUIDString(uuid)
	if (err != nil) {
		return err
	}
	defer domain.Free()
	oplog_off, oplog_err := oplog.Start(uuid, op, "")
	defer func() {
		if (oplog_err != nil) {
			logger.Log("Pause_domain: oplog: %s", oplog_err.Error())
		}
	}()
	err = domain.Suspend()
	if (err != nil) {
		if (oplog_err == nil) {
			oplog_err = oplog.End(uuid, op, openapi.OPERATION_FAILED, err.Error(), oplog_off)
		}
		return err
	}
	if (oplog_err == nil) {
		oplog_err = oplog.End(uuid, op, openapi.OPERATION_COMPLETED, "", oplog_off)
	}
	return nil
}

func Resume_domain(uuid string) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		op openapi.Operation = openapi.OpVmResume
	)
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	domain, err = conn.LookupDomainByUUIDString(uuid)
	if (err != nil) {
		return err
	}
	defer domain.Free()
	oplog_off, oplog_err := oplog.Start(uuid, op, "")
	defer func() {
		if (oplog_err != nil) {
			logger.Log("Resume_domain: oplog: %s", oplog_err.Error())
		}
	}()
	err = domain.Resume()
	if (err != nil) {
		if (oplog_err == nil) {
			oplog_err = oplog.End(uuid, op, openapi.OPERATION_FAILED, err.Error(), oplog_off)
		}
		return err
	}
	if (oplog_err == nil) {
		oplog_err = oplog.End(uuid, op, openapi.OPERATION_COMPLETED, "", oplog_off)
	}
	return nil
}

func Shutdown_domain(uuid string, force int16) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		op openapi.Operation = openapi.OpVmShutdown
	)
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	domain, err = conn.LookupDomainByUUIDString(uuid)
	if (err != nil) {
		return err
	}
	defer domain.Free()
	msg := fmt.Sprintf("shutdown force=%d.", force)
	oplog_off, oplog_err := oplog.Start(uuid, op, msg)
	defer func() {
		if (oplog_err != nil) {
			logger.Log("Shutdown_domain: oplog: %s", oplog_err.Error())
		}
	}()
	if (force == 0) {
		err = domain.Shutdown()
	} else if (force == 1) {
		err = domain.DestroyFlags(libvirt.DOMAIN_DESTROY_GRACEFUL)
	} else {
		err = domain.DestroyFlags(0)
	}
	if (err != nil) {
		if (oplog_err == nil) {
			oplog_err = oplog.End(uuid, op, openapi.OPERATION_FAILED, err.Error(), oplog_off)
		}
	} else {
		/* we will wait for the lifecycle event to set the operation to completed */
	}
	return err
}

func Delete_domain(uuid string) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
	)
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
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
	err = reg.Delete(machine.Uuid(), uuid)
	if (err != nil) {
		logger.Log("Delete_domain: failed to reg.Delete(%s, %s)", machine.Uuid(), uuid)
	}
	return nil
}
