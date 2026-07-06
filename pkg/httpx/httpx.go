/*
 * Copyright (c) 2024-2026 SUSE LLC
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
	"net/http/httptrace"
	"net/url"
	"net/textproto"
	"context"
	"errors"
	"encoding/json"
	"bytes"
	"io"
	"sync"
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
	r    *http.Response
	Body []byte
}

const (
	CLIENT_TIMEOUT = 15
	CLIENT_IDLE_CONN_MAX = 100
	CLIENT_IDLE_CONN_MAX_PER_HOST = 10
	CLIENT_IDLE_TIMEOUT = 15
	CLIENT_TLS_TIMEOUT = 5

	SERVER_TIMEOUT = 10
)

/*
 * No global Timeout: each request manages its own deadline via a per-request
 * context + time.AfterFunc, reset on each 102 keepalive received.
 */
var client http.Client = http.Client{
	Transport: &http.Transport{
		MaxIdleConns: CLIENT_IDLE_CONN_MAX,
		MaxIdleConnsPerHost: CLIENT_IDLE_CONN_MAX_PER_HOST,
		IdleConnTimeout: CLIENT_IDLE_TIMEOUT * time.Second,
		TLSHandshakeTimeout: CLIENT_TLS_TIMEOUT * time.Second,
	},
}

type conn_ctx_key struct{}

func Context_with_conn(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, conn_ctx_key{}, c)
}

/* Send an HTTP 102 Processing informational response, optionally with headers. */
func Send_progress(r *http.Request, h http.Header) {
	conn, ok := r.Context().Value(conn_ctx_key{}).(net.Conn)
	if (!ok) {
		return
	}
	var buf bytes.Buffer
	buf.WriteString("HTTP/1.1 102 Processing\r\n")
	if (h != nil) {
		h.Write(&buf)
	}
	buf.WriteString("\r\n")
	conn.Write(buf.Bytes())
}

/*
 * Start_progress sends an initial 102 to reset the client's per-request activity
 * timer, then starts a goroutine that sends a 102 every 5 seconds as a keepalive
 * while a long-running operation is in progress. The caller must invoke the
 * returned stop function before writing the final response.
 */
func Start_progress(r *http.Request) func() {
	Send_progress(r, nil)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				Send_progress(r, nil)
			case <-done:
				return
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
	}
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
	if (result == nil && r.StatusCode >= 200 && r.StatusCode <= 299) {
		return vr, nil
	}
	if (r.ContentLength <= 0) {
		return vr, errors.New("Body expected but not found")
	}
	if (r.ContentLength >= HTTP_MAX_BODY_LEN) {
		return vr, errors.New("content-length exceeded")
	}
	vr.Body, err = io.ReadAll(io.LimitReader(r.Body, HTTP_MAX_BODY_LEN))
	if (err != nil) {
		return vr, errors.New("failed to read body")
	}
	if (int64(len(vr.Body)) > r.ContentLength) {
		return vr, errors.New("body len exceeds content-length")
	}
	if (r.StatusCode >= 200 && r.StatusCode <= 299) {
		err = json.NewDecoder(bytes.NewReader(vr.Body)).Decode(result)
	}
	return vr, err
}

/*
 * CancelBody wraps a response body so the context cancel function is called
 * exactly once when the body is closed, releasing per-request context resources.
 */
type CancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (b *CancelBody) Close() error {
	b.once.Do(b.cancel)
	return b.ReadCloser.Close()
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
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(CLIENT_TIMEOUT * time.Second, cancel)
	defer func() {
		timer.Stop()
		if (err != nil) {
			cancel()
		}
	}()
	trace := &httptrace.ClientTrace{
		Got1xxResponse: func(code int, header textproto.MIMEHeader) error {
			timer.Reset(CLIENT_TIMEOUT * time.Second)
			return nil
		},
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), method, addr.String(), &buf)
	if (err != nil) {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if (err != nil) {
		return nil, err
	}
	resp.Body = &CancelBody{ ReadCloser: resp.Body, cancel: cancel }
	return resp, nil
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
		http.Error(w, "failed to forward request", http.StatusBadGateway)
		return
	}
	proxyreq.Header = vr.r.Header.Clone()
	client_ip, _, err := net.SplitHostPort(vr.r.RemoteAddr)
	if (err != nil) {
		logger.Log("proxy_request could not decode client address")
		http.Error(w, "failed to forward request", http.StatusBadGateway)
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

	/* per-request timeout, reset on each 102 keepalive */
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(CLIENT_TIMEOUT * time.Second, cancel)
	defer func() {
		timer.Stop()
		cancel()
	}()
	/* forward 102 responses from the target back to the caller */
	trace := &httptrace.ClientTrace{
		Got1xxResponse: func(code int, header textproto.MIMEHeader) error {
			timer.Reset(CLIENT_TIMEOUT * time.Second)
			Send_progress(vr.r, http.Header(header))
			return nil
		},
	}
	proxyreq = proxyreq.WithContext(httptrace.WithClientTrace(ctx, trace))

	resp, err := client.Do(proxyreq)
	if (err != nil) {
		logger.Log("proxy_request failed: %s", err.Error())
		http.Error(w, "failed to forward request", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for name, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	if (err != nil) {
		logger.Log("proxy_request failed during io.Copy: %s", err.Error())
	}
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
