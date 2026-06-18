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
package httpx

import (
	"bufio"
	"io"
	"net"
	"net/http"

	"suse.com/virtx/pkg/logger"
)

type ConsolePipe struct {
	R io.Reader
	W io.Writer
	C io.Closer
}

func (p ConsolePipe) Read(b []byte) (int, error)  { return p.R.Read(b) }
func (p ConsolePipe) Write(b []byte) (int, error) { return p.W.Write(b) }
func (p ConsolePipe) Close() error                { return p.C.Close() }

func Console_splice(a io.ReadWriteCloser, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(b, a); done <- struct{}{} }()
	go func() { io.Copy(a, b); done <- struct{}{} }()
	<-done
	a.Close()
	b.Close()
	<-done
}

/*
 * Console_serve hijacks the HTTP connection, splices it bidirectionally
 * with remote_end. remote_end is closed on exit.
 * Responds 101 if the client requested an upgrade, 200 otherwise.
 */
func Console_serve(w http.ResponseWriter, r *http.Request, remote_end io.ReadWriteCloser) {
	var (
		err error
		client_conn net.Conn
		client_buf *bufio.ReadWriter
		hj http.Hijacker
		ok bool
	)
	hj, ok = w.(http.Hijacker)
	if (!ok) {
		remote_end.Close()
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	client_conn, client_buf, err = hj.Hijack()
	if (err != nil) {
		remote_end.Close()
		logger.Log("Console_serve: hijack failed: %s", err.Error())
		return
	}
	if (r.Header.Get("Upgrade") == "tcp") {
		client_buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: tcp\r\nConnection: Upgrade\r\n\r\n")
	} else {
		client_buf.WriteString("HTTP/1.1 200 OK\r\n\r\n")
	}
	client_buf.Flush()
	pipe := ConsolePipe{ R: client_buf.Reader, W: client_conn, C: client_conn }
	Console_splice(pipe, remote_end)
}

/*
 * Proxy_console forwards a console tunnel request to another virtxd host.
 * Mirrors Proxy_request but for raw TCP tunnels instead of HTTP.
 */
func Proxy_console(api_server string, uuid string, console_type string, w http.ResponseWriter, r *http.Request) {
	var (
		err error
		req *http.Request
		resp *http.Response
		target_conn net.Conn
		target_reader *bufio.Reader
	)
	if (r.Header.Get("X-VirtX-Loop") != "") {
		logger.Log("Proxy_console: loop detected")
		http.Error(w, "loop detected", http.StatusLoopDetected)
		return
	}
	target_conn, err = net.Dial("tcp", api_server + ":8080")
	if (err != nil) {
		logger.Log("Proxy_console: dial %s failed: %s", api_server, err.Error())
		http.Error(w, "failed to reach target host", http.StatusBadGateway)
		return
	}
	req, err = http.NewRequest("GET", "http://" + api_server + ":8080/vms/" + uuid + "/console/" + console_type, nil)
	if (err != nil) {
		target_conn.Close()
		http.Error(w, "failed to build request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("X-VirtX-Loop", "1")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	err = req.Write(target_conn)
	if (err != nil) {
		target_conn.Close()
		logger.Log("Proxy_console: write request failed: %s", err.Error())
		http.Error(w, "failed to forward request", http.StatusBadGateway)
		return
	}
	target_reader = bufio.NewReader(target_conn)
	resp, err = http.ReadResponse(target_reader, req)
	if (err != nil) {
		target_conn.Close()
		logger.Log("Proxy_console: read response failed: %s", err.Error())
		http.Error(w, "failed to read target response", http.StatusBadGateway)
		return
	}
	resp.Body.Close()
	if (resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK) {
		target_conn.Close()
		http.Error(w, "target host refused console connection", resp.StatusCode)
		return
	}
	pipe := ConsolePipe{ R: target_reader, W: target_conn, C: target_conn }
	Console_serve(w, r, pipe)
}
