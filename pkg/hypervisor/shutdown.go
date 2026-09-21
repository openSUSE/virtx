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
	"fmt"

	"libvirt.org/go/libvirt"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/oplog"
)

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
