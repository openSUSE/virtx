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

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/vmdef"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/storage"
)

func vm_create(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		o openapi.VmCreateOptions
		xml, uuid string
		vr httpx.Request
	)
	vr, err = httpx.Decode_request_body(r, &o)
	if (err != nil) {
		logger.Log(err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	if (http_host_is_remote(o.Host)) { /* need to proxy */
		http_proxy_request(o.Host, w, vr)
		return
	}
	/* Validate vmdef first */
	err = vmdef.Validate(&o.Vmdef)
	if (err != nil) {
		logger.Log("vmdef.Validate failed: %s", err.Error())
		http.Error(w, "invalid parameters", http.StatusBadRequest)
		return
	}
	uuid = New_uuid()
	if (uuid == "") {
		http.Error(w, "failed", http.StatusInternalServerError)
		return
	}
	/* create storage if needed, can change o.Vmdef in some cases */
	err = storage.Create(&o.Vmdef, nil)
	if (err != nil) {
		logger.Log("vm_create_storage failed: %s", err.Error())
		http.Error(w, "storage creation failed", http.StatusInsufficientStorage)
		return
	}
	xml, err = vmdef.To_xml(&o.Vmdef, uuid)
	if (err != nil) {
		logger.Log("vmdef.To_xml failed: %s", err.Error())
		http.Error(w, "invalid parameters", http.StatusBadRequest)
		return
	}
	err = hypervisor.Define_domain(xml, uuid)
	if (err != nil) {
		logger.Log("hypervisor.Define_domain failed: %s", err.Error())
		http.Error(w, "could not define VM", http.StatusFailedDependency)
		return
	}
	var buf bytes.Buffer
	err = json.NewEncoder(&buf).Encode(&uuid)
	if (err != nil) {
		logger.Log("failed to encode JSON")
	}
	httpx.Do_response(w, http.StatusCreated, &buf)
}
