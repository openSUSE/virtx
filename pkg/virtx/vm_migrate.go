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
package virtx

import (
	"net/http"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/inventory"
)

func vm_migrate(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		o openapi.VmMigrateOptions
		uuid string
		vmdata inventory.Vmdata
		vr httpx.Request
		state openapi.Vmrunstate
		host_old_id string
		host_new openapi.Host
		proxy_hostid string
	)
	vr, err = httpx.Decode_request_body(r, &o)
	if (err != nil) {
		logger.Log(err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	uuid = r.PathValue("uuid")
	if (uuid == "") {
		http.Error(w, "could not get uuid", http.StatusBadRequest)
		return
	}
	vmdata, err = inventory.Get_vm(uuid)
	if (err != nil) {
		http.Error(w, "unknown uuid", http.StatusNotFound)
		return
	}
	state = vmdata.Runinfo.Runstate
	if (o.Host == "") {
		/* Auto migration is not implemented yet */
		http.Error(w, "Not implemented", http.StatusNotImplemented)
		return
	}
	host_old_id = vmdata.Runinfo.Host
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
		if (state != openapi.RUNSTATE_POWEROFF && state != openapi.RUNSTATE_CRASHED) {
			http.Error(w, "VM is not powered off", http.StatusUnprocessableEntity)
			return
		}
	case openapi.MIGRATION_LIVE:
		if (state != openapi.RUNSTATE_RUNNING && state != openapi.RUNSTATE_PAUSED) {
			http.Error(w, "VM is not running or paused", http.StatusUnprocessableEntity)
			return
		}
	default:
		http.Error(w, "invalid migration type", http.StatusBadRequest)
		return
	}
	host_new, err = inventory.Get_host(o.Host)
	if (err != nil) {
		logger.Log("inventory.Get_host(%s) failed: %s", o.Host, err.Error())
		http.Error(w, "failed to get host", http.StatusInternalServerError)
		return
	}
	go func() {
		err = hypervisor.Migrate_domain(host_new.Def.Name, o.Host, host_old_id, uuid, o.MigrationType == openapi.MIGRATION_LIVE, int(vmdata.Vcpus))
		if (err != nil) {
			logger.Log("migration of domain %s failed: %s", uuid, err.Error())
		} else {
			logger.Debug("migration of domain %s successful", uuid)
		}
	} ()
	httpx.Do_response(w, http.StatusAccepted, nil)
}
