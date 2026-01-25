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
	"net/http"
	"encoding/json"
	"bytes"

	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/inventory"
)

func vm_runstate_get(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		uuid string
		vmdata inventory.Vmdata
		runinfo openapi.Vmruninfo
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
		http.Error(w, "vm_runstate_get: Failed to decode parameters", http.StatusBadRequest)
		return
	}
	vmdata, err = inventory.Get_vm(uuid)
	if (err != nil) {
		http.Error(w, "vm_runstate_get: No such VM", http.StatusNotFound)
		return
	}
	runinfo = vmdata.Runinfo
	err = json.NewEncoder(&buf).Encode(&runinfo)
	if (err != nil) {
		http.Error(w, "vm_runstate_get: Failed to encode JSON", http.StatusInternalServerError)
		return
	}
	httpx.Do_response(w, http.StatusOK, &buf)
}
