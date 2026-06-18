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
	"net/http"

	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/inventory"
)

func http_host_is_remote(uuid string) bool {
	return uuid != "" && uuid != machine.Uuid()
}

func http_proxy_request(uuid string, w http.ResponseWriter, vr httpx.Request) {
	var (
		hostinfo inventory.HostInfo
		err error
	)
	hostinfo, err = inventory.Get_hostinfo(uuid)
	if (err != nil) {
		http.Error(w, "unknown host", http.StatusServiceUnavailable)
		return
	}
	if (hostinfo.Cstate != openapi.CSTATE_ACTIVE) {
		http.Error(w, "inactive host", http.StatusServiceUnavailable)
		return
	}
	httpx.Proxy_request(hostinfo.Name, w, vr)
}

func http_proxy_console(host_uuid string, vm_uuid string, console_type string, w http.ResponseWriter, r *http.Request) {
	var (
		hostinfo inventory.HostInfo
		err error
	)
	hostinfo, err = inventory.Get_hostinfo(host_uuid)
	if (err != nil) {
		http.Error(w, "unknown host", http.StatusServiceUnavailable)
		return
	}
	if (hostinfo.Cstate != openapi.CSTATE_ACTIVE) {
		http.Error(w, "inactive host", http.StatusServiceUnavailable)
		return
	}
	httpx.Proxy_console(hostinfo.Name, vm_uuid, console_type, w, r)
}
