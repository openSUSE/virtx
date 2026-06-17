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
package virtx

import (
	"fmt"
	"io"
	"net"
	"net/http"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/logger"
)

func vm_console_vnc(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		uuid string
		vminfo inventory.VmInfo
		port int
		qemu_conn net.Conn
	)
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
	if (http_host_is_remote(vminfo.Host)) {
		http_proxy_console(vminfo.Host, uuid, "vnc", w, r)
		return
	}
	port, err = hypervisor.Get_vnc_port(uuid)
	if (err != nil) {
		logger.Log("vm_console_vnc: Get_vnc_port failed: %s", err.Error())
		http.Error(w, "VNC not available", http.StatusServiceUnavailable)
		return
	}
	qemu_conn, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if (err != nil) {
		logger.Log("vm_console_vnc: dial VNC failed: %s", err.Error())
		http.Error(w, "failed to connect to VNC", http.StatusServiceUnavailable)
		return
	}
	httpx.Console_serve(w, r, qemu_conn)
}

func vm_console_serial(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		uuid string
		vminfo inventory.VmInfo
		serial io.ReadWriteCloser
	)
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
	if (http_host_is_remote(vminfo.Host)) {
		http_proxy_console(vminfo.Host, uuid, "serial", w, r)
		return
	}
	serial, err = hypervisor.Open_serial(uuid)
	if (err != nil) {
		logger.Log("vm_console_serial: Open_serial failed: %s", err.Error())
		http.Error(w, "serial console not available", http.StatusServiceUnavailable)
		return
	}
	httpx.Console_serve(w, r, serial)
}
