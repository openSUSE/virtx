/*
 * Copyright (c) 2026 SUSE LLC
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

package main

import (
	"net/http"

	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
)

/*
 * Host Uuid -> Host Name, for display purposes only.
 * The API is normalized and refers to hosts by Uuid: a Host Name can change,
 * the Uuid cannot, so the name is never stored alongside the VM. We resolve it
 * for display purposes only here, with one extra request on first use.
 */
var host_names map[string]string

/*
 * Return the name of the host with this Uuid, or "" if it cannot be resolved.
 */
func host_name(uuid string) string {
	var (
		err error
		response *http.Response
		options openapi.HostListOptions
		list openapi.HostList
	)
	if (host_names != nil) {
		return host_names[uuid]
	}
	/* only attempt the fetch once, whatever the outcome */
	host_names = make(map[string]string)
	response, err = httpx.Do_request(virtx.api_server, "GET", "/hosts", &options)
	if (err != nil) {
		logger.Log("host_name: failed to list hosts: %s", err.Error())
		return ""
	}
	_, err = httpx.Decode_response_body(response, &list)
	if (err != nil) {
		logger.Log("host_name: failed to decode host list: %s", err.Error())
		return ""
	}
	for _, item := range (list.Items) {
		host_names[item.Uuid] = item.Fields.Name
	}
	return host_names[uuid]
}
