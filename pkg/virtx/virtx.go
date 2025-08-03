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
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"errors"
	"context"
	"time"
	"io"
	"bytes"
	"encoding/json"

	g_uuid "github.com/google/uuid"

	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/model"
	. "suse.com/virtx/pkg/constants"
)

const (
	CLIENT_TIMEOUT = 10
	CLIENT_IDLE_CONN_MAX = 100
	CLIENT_IDLE_CONN_MAX_PER_HOST = 10
	CLIENT_IDLE_TIMEOUT = 15
	CLIENT_TLS_TIMEOUT = 5
)

type Vmdata map[string]hypervisor.Vmdata
type Hosts map[string]openapi.Host

type Service struct {
	servemux *http.ServeMux
	server http.Server
	client http.Client
	m      sync.RWMutex

	cluster openapi.Cluster
	hosts   Hosts
	vmdata  Vmdata
}

var service Service

func Init() {
	/*
	 * configure UUID generation to not use the RandPool.
	 * This is a tradeoff. If we notice that uuid generation is a hot path,
	 * change this to .EnableRandPool()
	 */
	g_uuid.DisableRandPool()

	var servemux *http.ServeMux = http.NewServeMux()
	servemux.HandleFunc("POST /vms", vm_create)
	servemux.HandleFunc("GET /vms", vm_list)
	servemux.HandleFunc("PUT /vms/{uuid}", vm_update)
	servemux.HandleFunc("GET /vms/{uuid}", vm_get)
	servemux.HandleFunc("DELETE /vms/{uuid}", vm_delete)
	servemux.HandleFunc("GET /vms/{uuid}/runstate", vm_runstate_get)
	servemux.HandleFunc("POST /vms/{uuid}/runstate/start", vm_start)
	servemux.HandleFunc("DELETE /vms/{uuid}/runstate/start", vm_shutdown)
	servemux.HandleFunc("POST /vms/{uuid}/runstate/pause", vm_pause)
	servemux.HandleFunc("DELETE /vms/{uuid}/runstate/pause", vm_unpause)
	servemux.HandleFunc("POST /vms/{uuid}/runstate/migrate", vm_migrate)
	servemux.HandleFunc("GET /vms/{uuid}/runstate/migrate", vm_migrate_get)
	servemux.HandleFunc("DELETE /vms/{uuid}/runstate/migrate", vm_migrate_cancel)

	servemux.HandleFunc("GET /hosts", host_list)
	servemux.HandleFunc("GET /hosts/{uuid}", host_get)

	service = Service{
		servemux: servemux,
		server: http.Server{
			Addr: ":8080",
			Handler: servemux,
		},
		client: http.Client{
			Timeout: CLIENT_TIMEOUT * time.Second,
			Transport: &http.Transport{
				MaxIdleConns: CLIENT_IDLE_CONN_MAX,
				MaxIdleConnsPerHost: CLIENT_IDLE_CONN_MAX_PER_HOST,
				IdleConnTimeout: CLIENT_IDLE_TIMEOUT * time.Second,
				TLSHandshakeTimeout: CLIENT_TLS_TIMEOUT * time.Second,
			},
		},
		m:         sync.RWMutex{},
		cluster:   openapi.Cluster{},
		hosts:     make(Hosts),
		vmdata:    make(Vmdata),
	}
}

func New_uuid() string {
	var (
		g g_uuid.UUID
		err error
	)
	g, err = g_uuid.NewRandom()
	if (err != nil) {
		logger.Log("g_uuid.NewRandom() failed: %s", err.Error())
		return ""
	}
	return g.String()
}

func Shutdown(ctx context.Context) error {
	var err error
	err = service.server.Shutdown(ctx)
	if (err != nil) {
		return err
	}
	transport, ok := service.client.Transport.(*http.Transport)
	if (ok) {
		transport.CloseIdleConnections()
	}
	if (err != nil) {
		return err
	}
	return nil
}

func Close() error {
	return service.server.Close()
}

func Update_host(host *openapi.Host) error {
	service.m.Lock()
	defer service.m.Unlock()

	return update_host(host)
}

func update_host(host *openapi.Host) error {
	var (
		present bool
		old openapi.Host
	)
	if (service.hosts == nil) {
		service.hosts = make(map[string]openapi.Host)
	}
	old, present = service.hosts[host.Uuid]
	if (present && old.Ts > host.Ts) {
		logger.Log("Host %s: ignoring obsolete Host information: ts %d > %d",
			old.Def.Name, old.Ts, host.Ts)
		return nil
	}
	service.hosts[host.Uuid] = *host
	return nil
}

func Set_host_state(uuid string, newstate openapi.Hoststate) error {
	service.m.Lock()
	defer service.m.Unlock()

	return set_host_state(uuid, newstate)
}

func set_host_state(uuid string, newstate openapi.Hoststate) error {
	host, ok := service.hosts[uuid]
	if !ok {
		return fmt.Errorf("no such host %s", uuid)
	}
	host.State = newstate
	service.hosts[uuid] = host
	return nil
}

func Update_vm_state(e *hypervisor.VmEvent) error {
	service.m.Lock()
	defer service.m.Unlock()
	vmdata, ok := service.vmdata[e.Uuid]
	if !ok {
		return fmt.Errorf("no such VM %s", e.Uuid)
	}
	vmdata.Runinfo.Runstate = openapi.Vmrunstate(e.State)
	if (vmdata.Runinfo.Runstate == openapi.RUNSTATE_DELETED) {
		delete(service.vmdata, e.Uuid)
	} else {
		service.vmdata[e.Uuid] = vmdata
	}
	return nil
}

func Update_vm(vmdata *hypervisor.Vmdata) error {
	service.m.Lock()
	defer service.m.Unlock()

	return update_vm(vmdata)
}

func update_vm(vmdata *hypervisor.Vmdata) error {
	if (service.vmdata == nil) {
		service.vmdata = make(map[string]hypervisor.Vmdata)
	}
	if old, ok := service.vmdata[vmdata.Uuid]; ok {
		if (old.Ts > vmdata.Ts) {
			logger.Log("Ignoring old guest info: ts %d > %d %s %s",
				old.Ts, vmdata.Ts, vmdata.Uuid, vmdata.Name,
			)
			return nil
		}
		/* calculate deltas from previous Vm info */
		if (vmdata.Runinfo.Runstate > openapi.RUNSTATE_POWEROFF &&
			old.Runinfo.Runstate > openapi.RUNSTATE_POWEROFF) {
			var delta uint64 = hypervisor.Counter_delta_uint64(vmdata.Cpu_time, old.Cpu_time)
			if (delta > 0 && (vmdata.Ts - old.Ts) > 0 && vmdata.Stats.Vcpus > 0) {
				vmdata.Stats.CpuUtilization = int32((delta * 100) / (uint64(vmdata.Ts - old.Ts) * 1000000))
			}
		}
		{
			var delta int64 = hypervisor.Counter_delta_int64(vmdata.Net_rx, old.Net_rx)
			if (delta > 0 && (vmdata.Ts - old.Ts) > 0) {
				vmdata.Stats.NetRxBw = int32((delta * 1000) / ((vmdata.Ts - old.Ts) * KiB))
			}
			delta = hypervisor.Counter_delta_int64(vmdata.Net_tx, old.Net_tx)
			if (delta > 0 && (vmdata.Ts - old.Ts) > 0) {
				vmdata.Stats.NetTxBw = int32((delta * 1000) / ((vmdata.Ts - old.Ts) * KiB))
			}
		}
	}
	service.vmdata[vmdata.Uuid] = *vmdata
	return nil
}

func host_is_remote(uuid string) bool {
	return uuid != "" && uuid != hypervisor.Uuid()
}

func do_request(uuid string, method string, path string, arg any) (*http.Response, error) {
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

func proxy_request(uuid string, w http.ResponseWriter, r *http.Request) {
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

func Start_listening() {
	go func() {
		var err error = service.server.ListenAndServe()
		if (err != nil && errors.Is(err, http.ErrServerClosed)) {
			logger.Log(err.Error())
		} else {
			logger.Log(err.Error())
		}
	}()
}
