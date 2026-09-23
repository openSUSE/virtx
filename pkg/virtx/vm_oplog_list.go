/*
 * Copyright (c) 2026 SUSE LLC
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
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/oplog"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/inventory"
)

func vm_oplog_list(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		o openapi.VmOplogListOptions
		uuid string
		vminfo inventory.VmInfo
		vr httpx.Request
		list openapi.OplogList
		buf bytes.Buffer
	)
	vr, err = httpx.Decode_request_body(r, &o)
	if (err != nil) {
		logger.Log("%s", err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	if (!oplog.Is_valid_op(o.Op)) {
		http.Error(w, "unknown operation code", http.StatusBadRequest)
		return
	}
	if (o.From < 0 || o.To < 0) {
		http.Error(w, "from and to must not be negative", http.StatusBadRequest)
		return
	}
	if (o.From != 0 && o.To != 0 && o.From > o.To) {
		http.Error(w, "from must not be after to", http.StatusBadRequest)
		return
	}
	if (o.Head < 0 || o.Tail < 0) {
		http.Error(w, "head and tail must not be negative", http.StatusBadRequest)
		return
	}
	if (o.Head != 0 && o.Tail != 0) {
		http.Error(w, "head and tail are mutually exclusive", http.StatusBadRequest)
		return
	}
	uuid = r.PathValue("uuid")
	if (uuid == "") {
		http.Error(w, "could not get uuid", http.StatusBadRequest)
		return
	}
	vminfo, err = inventory.Get_vminfo(uuid)
	if (err != nil) {
		http.Error(w, "unknown uuid", http.StatusNotFound)
		return
	}
	if (http_host_is_remote(vminfo.Host)) {
		http_proxy_request(vminfo.Host, w, vr)
		return
	}
	list, err = oplog.List(uuid, &o)
	if (err != nil) {
		logger.Log("oplog.List failed: %s", err.Error())
		http.Error(w, "could not read oplog", http.StatusFailedDependency)
		return
	}
	err = json.NewEncoder(&buf).Encode(&list)
	if (err != nil) {
		logger.Log("failed to encode JSON")
		http.Error(w, "Failed to encode JSON", http.StatusInternalServerError)
		return
	}
	httpx.Do_response(w, http.StatusOK, &buf)
}
