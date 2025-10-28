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
	"os"
	"time"
	"net/http"
	"fmt"
	"encoding/json"
	"bytes"
	writer "text/tabwriter"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/httpx"
)

const (
	CLIENT_TIMEOUT = 10
	CLIENT_IDLE_CONN_MAX = 100
	CLIENT_IDLE_CONN_MAX_PER_HOST = 10
	CLIENT_IDLE_TIMEOUT = 15
	CLIENT_TLS_TIMEOUT = 5
)
type VirtxClient struct {
	api_server string           // the API server (default VIRTX_API_SERVER env)
	path string                 // relative path of the REST request we will do
	method string               // the REST method
	ok bool                     // used by cmd to know whether to prepare the request or process the response
	client http.Client          // the HTTP client
	force int                   // how much force to apply
	stat bool                   // show resource statistics
	disk bool                   // show VM disks
	net bool                    // show VM nets
	debug bool                  // verbose client output

	/* args */
	host_list_options openapi.HostListOptions
	vm_list_options openapi.VmListOptions
	vm_create_options openapi.VmCreateOptions
	vm_update_options openapi.VmUpdateOptions
	vm_shutdown_options openapi.VmShutdownOptions
	vm_delete_options openapi.VmDeleteOptions
	vm_migrate_options openapi.VmMigrateOptions
	vm_register_options openapi.VmRegisterOptions

	arg any                     // the argument if needed, the struct to be encoded into body of request
	result any                  // pointer to struct to be decoded from body of the response

	w *writer.Writer
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

var version string = "unknown"

func main() {
	var (
		err error
		response *http.Response
	)
	err = cmd_exec()
	if (err != nil) {
		logger.Log("failed to parse command: %s\n", err.Error())
		os.Exit(1)
	}
	if (virtx.debug) {
		logger.Log("version %s", version)
	}
	if (virtx.path == "") {
		/* nothing else to do. */
		os.Exit(0)
	}
	if (virtx.debug) {
		logger.Log("api_server=%s, method=%s, path=%s, arg=%v", virtx.api_server, virtx.method, virtx.path, virtx.arg)
		if (virtx.arg != nil) {
			var buf bytes.Buffer
			err = json.NewEncoder(&buf).Encode(virtx.arg)
			if (err == nil) {
				logger.Log("JSON\n%s", buf.String())
			}
		}
	}
	response, err = httpx.Do_request(virtx.api_server, virtx.method, virtx.path, virtx.arg)
	if (err != nil) {
		logger.Log("failed to send request: %s", err.Error())
		os.Exit(1)
	}
	if (response.Body != nil && virtx.result != nil) {
		_, err = httpx.Decode_response_body(response, virtx.result)
		if (err != nil) {
			logger.Log("failed to decode response: %s", err.Error())
			os.Exit(1)
		}
		if (virtx.debug) {
			logger.Log("result=%v", virtx.result)
			if (virtx.result != nil) {
				var buf bytes.Buffer
				err = json.NewEncoder(&buf).Encode(virtx.result)
				if (err == nil) {
					logger.Log("JSON\n%s", buf.String())
				}
			}
		}
	}
	if (response.StatusCode >= 200 && response.StatusCode <= 299) {
		virtx.ok = true
		virtx.w = writer.NewWriter(os.Stdout, 0, 4, 1, ' ', writer.StripEscape)
		err = cmd_exec()
		virtx.w.Flush()
	} else {
		fmt.Printf("%s\n", response.Status)
	}
}

func read_json(filename string, data any) {
	file, err := os.Open(filename)
	if (err != nil) {
		logger.Log("failed to open json: %s", err.Error())
		os.Exit(1)
	}
	defer file.Close()
	err = json.NewDecoder(file).Decode(data);
	if (err != nil) {
		logger.Log("failed to read json: %s", err.Error())
		os.Exit(1)
	}
}
