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
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/inventory"
)

func http_host_is_remote(uuid string) bool {
	return uuid != "" && uuid != hypervisor.Uuid()
}

func http_do_request(uuid string, method string, path string, arg any) (*http.Response, error) {
	var (
		host openapi.Host
		err error
		resp *http.Response
	)
	host, err = inventory.Get_host(uuid)
	if (err != nil) {
		return nil, err
	}
	resp, err = httpx.Do_request(host.Def.Name, method, path, arg)
	return resp, err
}

func http_proxy_request(uuid string, w http.ResponseWriter, vr httpx.Request) {
	var (
		host openapi.Host
		err error
	)
	host, err = inventory.Get_host(uuid)
	if (err != nil) {
		http.Error(w, "unknown host", http.StatusServiceUnavailable)
		return
	}
	if (host.State != openapi.HOST_ACTIVE) {
		http.Error(w, "inactive host", http.StatusServiceUnavailable)
		return
	}
	httpx.Proxy_request(host.Def.Name, w, vr)
}
