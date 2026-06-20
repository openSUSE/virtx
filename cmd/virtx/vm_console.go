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
	"time"

	"golang.org/x/sys/unix"

	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
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

/*
 * priority order:
 * 1) command line option -v, --viewer
 * 2) environment variable VNCVIEWER
 * 3) ordered vnc_viewer_list
 */
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

	/* launches viewer in the background, does not wait. */
	err = cmd.Start()
	if (err != nil) {
		logger.Log("vnc_launch_viewer: failed to launch %s: %s", path, err.Error())
	}
}

/*
 * vm_should_reconnect checks whether the VM is in a state where reconnecting
 * makes sense: only RUNNING, PAUSED, or MIGRATING warrant a retry.
 */
func vm_should_reconnect(uuid string) bool {
	var (
		resp *http.Response
		runinfo openapi.Vmruninfo
		err error
	)
	url := "/vms/" + uuid + "/runstate"
	resp, err = httpx.Do_request(virtx.api_server, "GET", url, nil)
	if (err != nil) {
		logger.Log("vm_should_reconnect: Do_request %s failed: %s", url, err.Error())
		return false
	}
	_, err = httpx.Decode_response_body(resp, &runinfo)
	if (err != nil) {
		logger.Log("vm_should_reconnect: Decode_resp %s failed: %s", url, err.Error())
		return false
	}
	switch (runinfo.Runstate) {
	case openapi.RUNSTATE_RUNNING, openapi.RUNSTATE_PAUSED, openapi.RUNSTATE_MIGRATING:
		return true
	}
	return false
}

/*
 * console_try_reconnect retries console_dial_tunnel for up to reconnect_timeout
 * seconds. Returns the new tunnel and true on success, or false if the VM is
 * not in a reconnectable state or the timeout expires.
 */
func console_try_reconnect(uuid string, console_type string) (httpx.ConsolePipe, bool) {
	var (
		tunnel httpx.ConsolePipe
		err error
		deadline time.Time
	)
	if (!vm_should_reconnect(uuid)) {
		return tunnel, false
	}
	deadline = time.Now().Add(time.Duration(virtx.reconnect_timeout) * time.Second)
	for time.Now().Before(deadline) {
		tunnel, err = console_dial_tunnel(virtx.api_server, uuid, console_type)
		if (err == nil) {
			return tunnel, true
		}
		logger.Debug("console reconnect attempt failed: %s", err.Error())
		time.Sleep(1 * time.Second)
	}
	return tunnel, false
}

/*
 * console_dial_tunnel opens a raw TCP connection to virtxd and upgrades it
 * to a console tunnel by sending an HTTP GET and reading the 101 or 200 response.
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
		ok bool
	)
	listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if (err != nil) {
		logger.Fatal("failed to listen for vncviewer: %s", err.Error())
	}
	defer listener.Close()
	tunnel, err = console_dial_tunnel(virtx.api_server, uuid, "vnc")
	if (err != nil) {
		logger.Fatal("failed to establish VNC tunnel: %s", err.Error())
	}
	logger.Debug("VNC console ready: %s", listener.Addr())
	for {
		vnc_launch_viewer(listener.Addr())
		vnc_conn, err = listener.Accept()
		if (err != nil) {
			tunnel.Close()
			logger.Fatal("failed to accept vncviewer connection: %s", err.Error())
		}
		if (!httpx.Console_splice(vnc_conn, tunnel)) {
			break /* viewer closed */
		}
		if (virtx.reconnect_timeout == 0) {
			break
		}
		logger.Debug("VNC connection lost, reconnecting...\r")
		tunnel, ok = console_try_reconnect(uuid, "vnc")
		if (!ok) {
			break
		}
		logger.Debug("VNC console reconnected.")
	}
}

func vm_console_serial_req(uuid string) {
	var (
		err error
		tunnel httpx.ConsolePipe
		old *unix.Termios
		fd int
		pipe httpx.ConsolePipe
		ok bool
		reader *serial_stdin_reader
		cancel_fds [2]int
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
	for {
		err = unix.Pipe(cancel_fds[:])
		if (err != nil) {
			logger.Fatal("vm_console_serial_req: failed to create cancel pipe: %s", err.Error())
		}
		reader = &serial_stdin_reader{ cancel_r: cancel_fds[0], cancel_w: cancel_fds[1] }
		pipe = httpx.ConsolePipe{ R: reader, W: os.Stdout, C: reader }
		if (!httpx.Console_splice(pipe, tunnel)) {
			break /* Ctrl-] */
		}
		if (virtx.reconnect_timeout == 0) {
			break
		}
		logger.Debug("\r\nconnection lost, reconnecting...\r")
		tunnel, ok = console_try_reconnect(uuid, "serial")
		if (!ok) {
			break
		}
		logger.Debug("\r\nserial console reconnected.\r")
	}
	fmt.Print("\r\n")
}

/*
 * serial_stdin_reader wraps os.Stdin but intercepts Ctrl-] (0x1d) to exit.
 * It uses unix.Poll to multiplex stdin with a cancel pipe, so that Close()
 * can immediately unblock a pending Read without closing stdin itself.
 */
type serial_stdin_reader struct {
	cancel_r int
	cancel_w int
}

func (s *serial_stdin_reader) Read(b []byte) (int, error) {
	var (
		fds []unix.PollFd
		n int
		err error
	)
	fds = []unix.PollFd{
		{ Fd: int32(os.Stdin.Fd()), Events: unix.POLLIN },
		{ Fd: int32(s.cancel_r), Events: unix.POLLIN },
	}
	for {
		_, err = unix.Poll(fds, -1)
		if (err == unix.EINTR) {
			continue
		}
		if (err != nil) {
			return 0, err
		}
		if (fds[1].Revents & (unix.POLLIN | unix.POLLHUP | unix.POLLERR) != 0) {
			unix.Close(s.cancel_r)
			return 0, io.EOF /* cancel signal received */
		}
		if (fds[0].Revents & unix.POLLIN != 0) {
			n, err = unix.Read(int(os.Stdin.Fd()), b)
			if (err != nil) {
				return 0, err
			}
			for i := 0; i < n; i++ {
				if (b[i] == 0x1d) { /* Ctrl-] */
					return 0, io.EOF
				}
			}
			return n, nil
		}
	}
}

func (s *serial_stdin_reader) Close() error {
	unix.Close(s.cancel_w)
	return nil
}
