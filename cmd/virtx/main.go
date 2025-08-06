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
package main

import (
	"fmt"
	"os"
	"time"
	"net/http"
	"suse.com/virtx/pkg/model"
)

const (
	CLIENT_TIMEOUT = 10
	CLIENT_IDLE_CONN_MAX = 100
	CLIENT_IDLE_CONN_MAX_PER_HOST = 10
	CLIENT_IDLE_TIMEOUT = 15
	CLIENT_TLS_TIMEOUT = 5
)
type VirtxClient struct {
	path string                 // relative path of the REST request we will do
	api_server string           // the API server (default VIRTX_API_SERVER env)
	client http.Client          // the HTTP client

	force int                   // how much force to apply
	host_list_options openapi.HostListOptions
	vm_list_options openapi.VmListOptions
	vm_create_options openapi.VmCreateOptions
	vm_update_options openapi.VmUpdateOptions
	vm_shutdown_options openapi.VmShutdownOptions
	vm_delete_options openapi.VmDeleteOptions
	vm_migrate_options openapi.VmMigrateOptions
}
var virtx VirtxClient = VirtxClient{
	client: http.Client{
		Timeout: CLIENT_TIMEOUT * time.Second,
		Transport: &http.Transport{
			MaxIdleConns: CLIENT_IDLE_CONN_MAX,
			MaxIdleConnsPerHost: CLIENT_IDLE_CONN_MAX_PER_HOST,
			IdleConnTimeout: CLIENT_IDLE_TIMEOUT * time.Second,
			TLSHandshakeTimeout: CLIENT_TLS_TIMEOUT * time.Second,
		},
	},
}

func main() {
	var err error
	err = cmd_exec()
	if (err != nil) {
		fmt.Fprintf(os.Stderr, "failed to execute: %s\n",err.Error())
		os.Exit(1)
	}
	os.Exit(0)
}
