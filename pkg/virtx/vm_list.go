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

func vm_list(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		o openapi.VmListOptions
		vm_list openapi.VmList
		buf bytes.Buffer
	)
	_, err = httpx.Decode_request_body(r, &o)
	if (err != nil) {
		logger.Log(err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	vm_list = inventory.Search_vms(o.Filter)
	err = json.NewEncoder(&buf).Encode(&vm_list)
	if (err != nil) {
		logger.Log("failed to encode JSON")
		http.Error(w, "Failed to encode JSON", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
