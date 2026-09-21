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
)

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
