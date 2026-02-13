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
	"encoding/json"
	"errors"

	"libvirt.org/go/libvirt"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/vmreg"
	"suse.com/virtx/pkg/metadata"
	"suse.com/virtx/pkg/ts"
)

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
	xmlstr, err = meta.To_xml(op, state, errstr, ts.Now())
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
	xmlstr, err = domain.GetMetadata(libvirt.DOMAIN_METADATA_ELEMENT, "virtx-op-" + op.String(), impact)
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
		op openapi.Operation = openapi.OpVmMigrate
		state openapi.OperationState
		errstr string
		ts int64
	)
	err = load_domain_op(domain, &op, &state, &errstr, &ts)
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
		op openapi.Operation = openapi.OpVmMigrate
		state openapi.OperationState
		errstr string
		ts int64
	)
	err = load_domain_op(domain, &op, &state, &errstr, &ts)
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
		op openapi.Operation = openapi.OpVmBoot
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
	_ = record_domain_op(domain, op, openapi.OPERATION_STARTED, "")
	err = domain.Create()
	if (err != nil) {
		_ = record_domain_op(domain, op, openapi.OPERATION_FAILED, err.Error())
		return err
	}
	_ = record_domain_op(domain, op, openapi.OPERATION_COMPLETED, "")
	return nil
}

func Pause_domain(uuid string) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		op openapi.Operation = openapi.OpVmPause
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
	_ = record_domain_op(domain, op, openapi.OPERATION_STARTED, "")
	err = domain.Suspend()
	if (err != nil) {
		_ = record_domain_op(domain, op, openapi.OPERATION_FAILED, err.Error())
		return err
	}
	_ = record_domain_op(domain, op, openapi.OPERATION_COMPLETED, "")
	return nil
}

func Resume_domain(uuid string) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		op openapi.Operation = openapi.OpVmResume
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
	_ = record_domain_op(domain, op, openapi.OPERATION_STARTED, "")
	err = domain.Resume()
	if (err != nil) {
		_ = record_domain_op(domain, op, openapi.OPERATION_FAILED, err.Error())
		return err
	}
	_ = record_domain_op(domain, op, openapi.OPERATION_COMPLETED, "")
	return nil
}

func Shutdown_domain(uuid string, force int16) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		op openapi.Operation = openapi.OpVmShutdown
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
	_ = record_domain_op(domain, op, openapi.OPERATION_STARTED, "")
	if (force == 0) {
		err = domain.Shutdown()
	} else if (force == 1) {
		err = domain.DestroyFlags(libvirt.DOMAIN_DESTROY_GRACEFUL)
	} else {
		err = domain.DestroyFlags(0)
	}
	if (err != nil) {
		_ = record_domain_op(domain, op, openapi.OPERATION_FAILED, err.Error())
	} else {
		_ = record_domain_op(domain, op, openapi.OPERATION_COMPLETED, "")
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
