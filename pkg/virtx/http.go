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
	. "suse.com/virtx/pkg/constants"
)

/*
 * every handler needs to call this, whether it needs to read a body or not,
 * since the request might contain a body to read and ignore, but the body
 * needs to be .Close()d because unfortunately r.Body is an io.ReadCloser().
 * Otherwise connections may stay open.
 */

type VirtxRequest struct {
	r *http.Request
	body []byte
}

func http_decode_body(r *http.Request, arg any) (VirtxRequest, error) {
	var (
		err error
		vr VirtxRequest
	)
	vr.r = r
	if (r.Body == nil) {     /* no body found */
		if (arg == nil) {
			return vr, nil       /* ok, did not expect any */
		}
		return vr, errors.New("no body")
	}
	if (r.ContentLength <= 0) {
		r.Body.Close()
		return vr, errors.New("content-length <= 0")
	}
	if (r.ContentLength >= HTTP_MAX_BODY_LEN) {
		r.Body.Close()
		return vr, errors.New("content-length exceeded")
	}
	vr.body, err = io.ReadAll(io.LimitReader(r.Body, HTTP_MAX_BODY_LEN))
	r.Body.Close()
	if (err != nil) {
		return vr, errors.New("failed to read body")
	}
	if (int64(len(vr.body)) > r.ContentLength) {
		return vr, errors.New("body len exceeds content-length")
	}
	err = json.NewDecoder(bytes.NewReader(vr.body)).Decode(arg)
	if (err != nil) {
		return vr, err
	}
	return vr, nil
}

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

func http_proxy_request(uuid string, w http.ResponseWriter, vr VirtxRequest) {
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
	if (vr.r.Header.Get("X-VirtX-Loop") != "") {
		logger.Log("proxy_request loop detected")
		http.Error(w, "loop detected", http.StatusLoopDetected)
		return
	}
	newaddr = *vr.r.URL
	newaddr.Host = host.Def.Name + ":8080"
	if (vr.r.TLS != nil) {
		newaddr.Scheme = "https"
	} else {
		newaddr.Scheme = "http"
	}
	proxyreq, err := http.NewRequest(vr.r.Method, newaddr.String(), bytes.NewReader(vr.body))
	if (err != nil) {
		logger.Log("proxy_request http.NewRequest failed: %s", err.Error())
		http.Error(w, "failed to forward request", http.StatusInternalServerError)
		return
	}

	proxyreq.Header = vr.r.Header.Clone()
	client_ip, _, err := net.SplitHostPort(vr.r.RemoteAddr)
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
