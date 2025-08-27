package virtx

import (
	"net/http"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/inventory"
)

func http_host_is_remote(uuid string) bool {
	return uuid != "" && uuid != hypervisor.Uuid()
}

func http_do_request(uuid string, method string, path string, arg any) (*http.Response, error) {
	var (
		host openapi.Host
		err error
		resp *http.Response
	)
	host, err = inventory.Get_host(uuid)
	if (err != nil) {
		return nil, err
	}
	resp, err = httpx.Do_request(host.Def.Name, method, path, arg)
	return resp, err
}

func http_proxy_request(uuid string, w http.ResponseWriter, vr httpx.Request) {
	var (
		host openapi.Host
		err error
	)
	host, err = inventory.Get_host(uuid)
	if (err != nil) {
		http.Error(w, "unknown host", http.StatusUnprocessableEntity)
		return
	}
	httpx.Proxy_request(host.Def.Name, w, vr)
}
