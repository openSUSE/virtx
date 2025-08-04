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
	"net"
	"net/http"
	"net/url"
	"errors"
	"encoding/json"
	"bytes"
	"io"

	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/model"
)

func http_host_is_remote(uuid string) bool {
	return uuid != "" && uuid != hypervisor.Uuid()
}

func http_do_request(uuid string, method string, path string, arg any) (*http.Response, error) {
	/* assert (service.m.isRLocked()) */
	var (
		host openapi.Host
		addr url.URL
		ok bool
		buf bytes.Buffer
		err error
	)
	host, ok = service.hosts[uuid]
	if (!ok) {
		return nil, errors.New("unknown host")
	}
	addr.Path = path
	addr.Host = host.Def.Name + ":8080"
	addr.Scheme = "http"
	err = json.NewEncoder(&buf).Encode(arg)
	if (err != nil) {
		return nil, err
	}
	req, err := http.NewRequest(method, addr.String(), &buf)
	if (err != nil) {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	/* we unlock here to allow other goroutines to make progress while we wait for the response */
	service.m.RUnlock()
	resp, err := service.client.Do(req)
	service.m.RLock()
	if (err != nil) {
		return nil, err
	}
	return resp, nil
}

func http_proxy_request(uuid string, w http.ResponseWriter, r *http.Request) {
	/* assert (service.m.isRLocked()) */
	var (
		host openapi.Host
		newaddr url.URL
		ok bool
		err error
	)
	host, ok = service.hosts[uuid]
	if (!ok) {
		http.Error(w, "unknown host", http.StatusUnprocessableEntity)
		return
	}
	if (r.Header.Get("X-VirtX-Loop") != "") {
		logger.Log("proxy_request loop detected")
		http.Error(w, "loop detected", http.StatusLoopDetected)
		return
	}
	newaddr = *r.URL
	newaddr.Host = host.Def.Name + ":8080"
	if (r.TLS != nil) {
		newaddr.Scheme = "https"
	} else {
		newaddr.Scheme = "http"
	}
	proxyreq, err := http.NewRequest(r.Method, newaddr.String(), r.Body)
	if (err != nil) {
		logger.Log("proxy_request http.NewRequest failed: %s", err.Error())
		http.Error(w, "failed to forward request", http.StatusInternalServerError)
		return
	}

	proxyreq.Header = r.Header.Clone()
	client_ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if (err != nil) {
		logger.Log("proxy_request could not decode client address")
		http.Error(w, "failed to forward request", http.StatusInternalServerError)
		return
	}
	xff := proxyreq.Header.Get("X-Forwarded-For")
	if (xff != "") {
		xff = xff + ", " + client_ip
	} else {
		xff = client_ip
	}
	proxyreq.Header.Set("X-Forwarded-For", xff)
	proxyreq.Header.Set("X-VirtX-Loop", "1")

	/* we unlock here to allow other goroutines to make progress while we wait for the response */
	service.m.RUnlock()
	resp, err := service.client.Do(proxyreq)
	service.m.RLock()

	if (err != nil) {
		logger.Log("proxy_request failed: %s", err.Error())
		http.Error(w, "failed to forward request", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	for name, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
