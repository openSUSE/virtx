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
package virtx

import (
	"fmt"
	"net/http"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/vmlog"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/inventory"
)

func vm_migrate(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		o openapi.VmMigrateOptions
		uuid string
		vminfo inventory.VmInfo
		vr httpx.Request
		states []openapi.Vmrunstate
		host_old_id string
		host_new inventory.HostInfo
		proxy_hostid string
		migration_addr string
		msg string
	)
	vr, err = httpx.Decode_request_body(r, &o)
	if (err != nil) {
		logger.Log("%s", err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	uuid = r.PathValue("uuid")
	if (uuid == "") {
		http.Error(w, "could not get uuid", http.StatusBadRequest)
		return
	}
	vminfo, err = inventory.Get_vminfo(uuid)
	if (err != nil) {
		http.Error(w, "unknown uuid", http.StatusNotFound)
		return
	}
	if (o.Host == "") {
		/* Auto migration is not implemented yet */
		http.Error(w, "Not implemented", http.StatusNotImplemented)
		return
	}
	host_old_id = vminfo.Host
	if (o.Host == host_old_id) {
		http.Error(w, "Cannot migrate to the same host", http.StatusUnprocessableEntity)
		return
	}
	proxy_hostid = host_old_id
	if (http_host_is_remote(proxy_hostid)) { /* need to proxy to another host */
		http_proxy_request(proxy_hostid, w, vr);
		return
	}
	switch (o.MigrationType) {
	case openapi.MIGRATION_COLD:
		states = []openapi.Vmrunstate{ openapi.RUNSTATE_POWEROFF, openapi.RUNSTATE_CRASHED }
	case openapi.MIGRATION_LIVE:
		states = []openapi.Vmrunstate{ openapi.RUNSTATE_RUNNING, openapi.RUNSTATE_PAUSED }
	default:
		http.Error(w, "invalid migration type", http.StatusBadRequest)
		return
	}
	host_new, err = inventory.Get_hostinfo(o.Host)
	if (err != nil) {
		logger.Log("inventory.Get_host(%s) failed: %s", o.Host, err.Error())
		http.Error(w, "failed to get host", http.StatusInternalServerError)
		return
	}
	if (o.MigrationType == openapi.MIGRATION_LIVE) {
		var (
			dest openapi.Host
			resp *http.Response
		)
		resp, err = httpx.Do_request(host_new.Name, "GET", "/hosts/" + o.Host, nil)
		if (err != nil) {
			logger.Log("failed to request migration address: %s", err.Error())
			http.Error(w, "failed to request migration address", http.StatusInternalServerError)
			return
		}
		_, err = httpx.Decode_response_body(resp, &dest)
		if (err != nil) {
			logger.Log("failed to decode migration address: %s", err.Error())
			http.Error(w, "failed to decode migration address", http.StatusInternalServerError)
			return
		}
		migration_addr = dest.Net.MigrationAddr
	}
	/* the def lock is released by the migration goroutine, after reg.Move */
	if (!def_lock_acquire(w, uuid, openapi.OpVmMigrate, states...)) {
		return
	}
	if (o.MigrationType == openapi.MIGRATION_LIVE) {
		msg = "live"
	} else {
		msg = "offline"
	}
	msg += fmt.Sprintf(" %s -> %s", host_old_id, o.Host)
	oplog_off, oplog_err := vmlog.Start(uuid, openapi.OpVmMigrate, httpx.Client_ip(r), msg)
	if (oplog_err != nil) {
		logger.Log("vm_migrate: oplog: %s", oplog_err.Error())
	}
	go func() {
		defer def_lock_release(uuid)
		err = hypervisor.Migrate_domain(host_new.Name, migration_addr, o.Host, host_old_id, uuid, o.MigrationType == openapi.MIGRATION_LIVE)
		if (err != nil) {
			logger.Log("migration of domain %s failed: %s", uuid, err.Error())
			if (oplog_err == nil) {
				oplog_err = vmlog.End(uuid, openapi.OpVmMigrate, openapi.OPERATION_FAILED, err.Error(), oplog_off)
				if (oplog_err != nil) {
					logger.Log("vm_migrate: oplog: %s", oplog_err.Error())
				}
			}
			return
		}
		/*
		 * log COMPLETED before reg.Move so that machine.Uuid() is still the
		 * correct host (the file moves with the VM directory in the rename).
		 */
		if (oplog_err == nil) {
			oplog_err = vmlog.End(uuid, openapi.OpVmMigrate, openapi.OPERATION_COMPLETED, "Migrated.", oplog_off)
			if (oplog_err != nil) {
				logger.Log("vm_migrate: oplog: %s", oplog_err.Error())
			}
		}
		/* move the per-VM directory to the destination host path (carries the oplog with it) */
		err = reg.Move(o.Host, host_old_id, uuid)
		if (err != nil) {
			logger.Log("migration of domain %s: reg.Move failed: %s", uuid, err.Error())
		} else {
			logger.Debug("migration of domain %s successful", uuid)
		}
	} ()
	httpx.Do_response(w, http.StatusAccepted, nil)
}
