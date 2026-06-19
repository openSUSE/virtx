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
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"

	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/logger"
)

type vnc_viewer_entry struct {
	binary string
	prefix []string
	addr_fmt string
}

var vnc_viewer_list = []vnc_viewer_entry{
	{"vncviewer", nil, "%s::%s"},
	{"xtigervncviewer", nil, "%s::%s"},
	{"xtightvncviewer", nil, "%s::%s"},
	{"remote-viewer", nil, "vnc://%s:%s"},
	{"krdc", nil, "vnc://%s:%s"},
	{"remmina", []string{"-c"}, "vnc://%s:%s"},
}

func vnc_find_viewer() (string, vnc_viewer_entry) {
	var (
		specified string
		path string
		err error
	)
	specified = virtx.vnc_viewer
	if (specified == "") {
		specified = os.Getenv("VNCVIEWER")
	}
	if (specified != "") {
		for _, v := range vnc_viewer_list {
			if (v.binary == filepath.Base(specified)) {
				return specified, v
			}
		}
		/* unknown viewer: use default double-colon format */
		return specified, vnc_viewer_entry{ specified, nil, "%s::%s" }
	}
	for _, v := range vnc_viewer_list {
		path, err = exec.LookPath(v.binary)
		if (err == nil) {
			return path, v
		}
	}
	return "", vnc_viewer_entry{}
}

func vnc_launch_viewer(addr net.Addr) {
	var (
		path string
		viewer vnc_viewer_entry
		host string
		port string
		args []string
		err error
	)
	path, viewer = vnc_find_viewer()
	if (path == "") {
		return
	}
	host, port, err = net.SplitHostPort(addr.String())
	if (err != nil) {
		logger.Log("vnc_launch_viewer: could not parse address: %s", err.Error())
		return
	}
	args = append(args, viewer.prefix...)
	args = append(args, fmt.Sprintf(viewer.addr_fmt, host, port))
	cmd := exec.Command(path, args...)
	logger.Debug("%s %v", path, args)

	err = cmd.Start()
	if (err != nil) {
		logger.Log("vnc_launch_viewer: failed to launch %s: %s", path, err.Error())
	}
}

/*
 * console_dial_tunnel opens a raw TCP connection to virtxd and upgrades it
 * to a console tunnel by sending an HTTP GET and reading the 200 response.
 */
func console_dial_tunnel(api_server string, uuid string, console_type string) (httpx.ConsolePipe, error) {
	var (
		err error
		conn net.Conn
		req *http.Request
		resp *http.Response
		reader *bufio.Reader
		pipe httpx.ConsolePipe
	)
	conn, err = net.Dial("tcp", api_server + ":8080")
	if (err != nil) {
		return pipe, err
	}
	defer func() {
		if (err != nil) {
			conn.Close()
		}
	}()
	url := "http://" + api_server + ":8080/vms/" + uuid + "/console/" + console_type
	req, err = http.NewRequest("GET", url, nil)
	if (err != nil) {
		return pipe, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	err = req.Write(conn)
	if (err != nil) {
		return pipe, err
	}
	reader = bufio.NewReader(conn)
	resp, err = http.ReadResponse(reader, req)
	if (err != nil) {
		return pipe, err
	}
	resp.Body.Close()
	if (resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK) {
		err = fmt.Errorf("server returned %s", resp.Status)
		return pipe, err
	}
	pipe = httpx.ConsolePipe{ R: reader, W: conn, C: conn }
	return pipe, nil
}

func vm_console_vnc_req(uuid string, port int) {
	var (
		err error
		tunnel httpx.ConsolePipe
		listener net.Listener
		vnc_conn net.Conn
	)
	tunnel, err = console_dial_tunnel(virtx.api_server, uuid, "vnc")
	if (err != nil) {
		logger.Fatal("failed to establish VNC tunnel: %s", err.Error())
	}
	listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if (err != nil) {
		tunnel.Close()
		logger.Fatal("failed to listen for vncviewer: %s", err.Error())
	}
	fmt.Printf("VNC console ready: vncviewer %s\n", listener.Addr())
	vnc_launch_viewer(listener.Addr())
	vnc_conn, err = listener.Accept()
	listener.Close()
	if (err != nil) {
		tunnel.Close()
		logger.Fatal("failed to accept vncviewer connection: %s", err.Error())
	}
	httpx.Console_splice(vnc_conn, tunnel)
}

func vm_console_serial_req(uuid string) {
	var (
		err error
		tunnel httpx.ConsolePipe
		old *unix.Termios
		fd int
		pipe httpx.ConsolePipe
	)
	tunnel, err = console_dial_tunnel(virtx.api_server, uuid, "serial")
	if (err != nil) {
		logger.Fatal("failed to establish serial tunnel: %s", err.Error())
	}
	fd = int(os.Stdin.Fd())
	old, err = unix.IoctlGetTermios(fd, unix.TCGETS)
	if (err == nil) {
		var raw unix.Termios
		raw = *old
		raw.Iflag &^= unix.ICRNL | unix.IXON | unix.BRKINT | unix.INPCK | unix.ISTRIP
		raw.Oflag &^= unix.OPOST
		raw.Lflag &^= unix.ECHO | unix.ICANON | unix.ISIG | unix.IEXTEN
		raw.Cflag |= unix.CS8
		raw.Cc[unix.VMIN] = 1
		raw.Cc[unix.VTIME] = 0
		unix.IoctlSetTermios(fd, unix.TCSETS, &raw)
		defer unix.IoctlSetTermios(fd, unix.TCSETS, old)

		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigs
			unix.IoctlSetTermios(fd, unix.TCSETS, old)
			fmt.Print("\r\n")
			os.Exit(0)
		}()

		fmt.Print("Connected to serial console. Press Ctrl-] to exit.\r\n")
	} else {
		logger.Debug("stdin is not a terminal, skipping raw mode")
	}
	pipe = httpx.ConsolePipe{ R: &serial_stdin_reader{tunnel}, W: os.Stdout, C: tunnel }
	httpx.Console_splice(pipe, tunnel)
	fmt.Print("\r\n")
}

/*
 * serial_stdin_reader wraps os.Stdin but intercepts Ctrl-] (0x1d) to exit.
 */
type serial_stdin_reader struct {
	tunnel io.Closer
}

func (s *serial_stdin_reader) Read(b []byte) (int, error) {
	var (
		n int
		err error
	)
	n, err = os.Stdin.Read(b)
	for i := 0; i < n; i++ {
		if (b[i] == 0x1d) { /* Ctrl-] */
			s.tunnel.Close()
			return 0, io.EOF
		}
	}
	return n, err
}
