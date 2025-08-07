package virtx

import (
	"net/http"
	"errors"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/model"
)

func http_host_is_remote(uuid string) bool {
	return uuid != "" && uuid != hypervisor.Uuid()
}

func http_do_request(uuid string, method string, path string, arg any) (*http.Response, error) {
	/* assert (service.m.isRLocked()) */
	var (
		host openapi.Host
		ok bool
		err error
		resp *http.Response
	)
	host, ok = service.hosts[uuid]
	if (!ok) {
		return nil, errors.New("unknown host")
	}
	/* we unlock here to allow other goroutines to make progress while we wait for the response */
	service.m.RUnlock()
	resp, err = httpx.Do_request(host.Def.Name, method, path, arg)
	service.m.RLock()
	return resp, err
}

func http_proxy_request(uuid string, w http.ResponseWriter, vr httpx.Request) {
	/* assert (service.m.isRLocked()) */
	var (
		host openapi.Host
		ok bool
	)
	host, ok = service.hosts[uuid]
	if (!ok) {
		http.Error(w, "unknown host", http.StatusUnprocessableEntity)
		return
	}
	/* we unlock here to allow other goroutines to make progress while we wait for the response */
	service.m.RUnlock()
	httpx.Proxy_request(host.Def.Name, w, vr)
	service.m.RLock()
}
