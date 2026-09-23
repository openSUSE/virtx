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
	"libvirt.org/go/libvirt"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/oplog"
)

func Pause_domain(uuid string) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		op openapi.OperationCode = openapi.OpVmPause
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
