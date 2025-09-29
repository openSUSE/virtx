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
package httpx

import (
	"net"
	"net/http"
	"net/url"
	"errors"
	"encoding/json"
	"bytes"
	"io"
	"time"
	"strconv"

	"suse.com/virtx/pkg/logger"
	. "suse.com/virtx/pkg/constants"
)
/*
 * every handler needs to call this, whether it needs to read a body or not,
 * since the request might contain a body to read and ignore, but the body
 * needs to be .Close()d because unfortunately r.Body is an io.ReadCloser().
 * Otherwise connections may stay open.
 */
type Request struct {
	r *http.Request
	body []byte
}
type Response struct {
	r *http.Response
	body []byte
}

const (
	CLIENT_TIMEOUT = 10
	CLIENT_IDLE_CONN_MAX = 100
	CLIENT_IDLE_CONN_MAX_PER_HOST = 10
	CLIENT_IDLE_TIMEOUT = 15
	CLIENT_TLS_TIMEOUT = 5

	SERVER_TIMEOUT = 10
)

var client http.Client = http.Client{
	Timeout: CLIENT_TIMEOUT * time.Second,
	Transport: &http.Transport{
		MaxIdleConns: CLIENT_IDLE_CONN_MAX,
		MaxIdleConnsPerHost: CLIENT_IDLE_CONN_MAX_PER_HOST,
		IdleConnTimeout: CLIENT_IDLE_TIMEOUT * time.Second,
		TLSHandshakeTimeout: CLIENT_TLS_TIMEOUT * time.Second,
	},
}

func Decode_request_body(r *http.Request, arg any) (Request, error) {
	var (
		err error
		vr Request
	)
	vr.r = r
	defer r.Body.Close()
	if (arg == nil) {
		return vr, nil
	}
	if (r.ContentLength <= 0) {
		return vr, errors.New("Body expected but not found")
	}
	if (r.ContentLength >= HTTP_MAX_BODY_LEN) {
		return vr, errors.New("content-length exceeded")
	}
	vr.body, err = io.ReadAll(io.LimitReader(r.Body, HTTP_MAX_BODY_LEN))
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

func Decode_response_body(r *http.Response, result any) (Response, error) {
	var (
		err error
		vr Response
	)
	vr.r = r
	defer r.Body.Close()
	if (result == nil) {
		return vr, nil
	}
	if (r.ContentLength <= 0) {
		return vr, errors.New("Body expected but not found")
	}
	if (r.ContentLength >= HTTP_MAX_BODY_LEN) {
		return vr, errors.New("content-length exceeded")
	}
	if (r.StatusCode >= 200 && r.StatusCode <= 299) {
		vr.body, err = io.ReadAll(io.LimitReader(r.Body, HTTP_MAX_BODY_LEN))
		if (err != nil) {
			return vr, errors.New("failed to read body")
		}
		if (int64(len(vr.body)) > r.ContentLength) {
			return vr, errors.New("body len exceeds content-length")
		}
		err = json.NewDecoder(bytes.NewReader(vr.body)).Decode(result)
	}
	return vr, err
}

func Do_request(api_server string, method string, path string, arg any) (*http.Response, error) {
	var (
		addr url.URL
		buf bytes.Buffer
		err error
	)
	addr.Path = path
	addr.Host = api_server + ":8080"
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

	resp, err := client.Do(req)
	return resp, err
}

func Proxy_request(api_server string, w http.ResponseWriter, vr Request) {
	var (
		newaddr url.URL
		err error
	)
	if (vr.r.Header.Get("X-VirtX-Loop") != "") {
		logger.Log("proxy_request loop detected")
		http.Error(w, "loop detected", http.StatusLoopDetected)
		return
	}
	newaddr = *vr.r.URL
	newaddr.Host = api_server + ":8080"
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

	resp, err := client.Do(proxyreq)
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

func Do_response(w http.ResponseWriter, http_status int, buf *bytes.Buffer) {
	if (buf != nil) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	} else {
		w.Header().Set("Content-Length", "0")
	}
	w.WriteHeader(http_status)
	if (buf != nil) {
		w.Write(buf.Bytes())
	}
}

func Shutdown() {
	transport, ok := client.Transport.(*http.Transport)
	if (ok) {
		transport.CloseIdleConnections()
	}
}
