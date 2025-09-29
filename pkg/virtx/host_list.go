package virtx

import (
	"net/http"
	"encoding/json"
	"bytes"

	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/inventory"
)

func host_list(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		o openapi.HostListOptions
		host_list openapi.HostList
		buf bytes.Buffer
	)
	_, err = httpx.Decode_request_body(r, &o)
	if (err != nil) {
		logger.Log(err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	/* filters: [name, cpuarch, cpudef, hoststate, memoryavailable] */
	host_list = inventory.Search_hosts(o.Filter)
	err = json.NewEncoder(&buf).Encode(&host_list)
	if (err != nil) {
		http.Error(w, "Failed to encode JSON", http.StatusInternalServerError)
		return
	}
	httpx.Do_response(w, http.StatusOK, &buf)
}
