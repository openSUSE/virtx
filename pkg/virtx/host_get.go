package virtx

import (
	"net/http"
	"encoding/json"
	"bytes"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/inventory"
)

func host_get(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		uuid string
		host openapi.Host
		buf bytes.Buffer
	)
	_, err = httpx.Decode_request_body(r, nil)
	if (err != nil) {
		logger.Log(err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	uuid = r.PathValue("uuid")
	if (uuid == "") {
		http.Error(w, "could not get uuid", http.StatusBadRequest)
		return
	}
	host, err = inventory.Get_host(uuid)
	if (err != nil) {
		http.Error(w, "unknown uuid", http.StatusNotFound)
		return
	}
	err = json.NewEncoder(&buf).Encode(&host)
	if (err != nil) {
		http.Error(w, "Failed to encode JSON", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
