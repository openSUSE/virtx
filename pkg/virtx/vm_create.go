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
package virtx

import (
	"net/http"
	"encoding/json"
	"bytes"
	"fmt"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/oplog"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/vmdef"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/sched"
	"suse.com/virtx/pkg/storage"
	"suse.com/virtx/pkg/ts"
)

func vm_create(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		o openapi.VmCreateOptions
		xml, uuid string
		vr httpx.Request
		created storage.CreatedResources
	)
	vr, err = httpx.Decode_request_body(r, &o)
	if (err != nil) {
		logger.Log("%s", err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	/* Validate before scheduling: the scheduler filters on arch and memory size */
	err = vmdef.Validate(&o.Vmdef)
	if (err != nil) {
		logger.Log("vmdef.Validate failed: %s", err.Error())
		http.Error(w, "invalid parameters", http.StatusBadRequest)
		return
	}
	if (o.Host == "") {
		o.Host = sched.Schedule_vmdef(&o.Vmdef)
		if (o.Host == "") {
			http.Error(w, "no suitable host available", http.StatusServiceUnavailable)
			return
		}
		if (http_host_is_remote(o.Host)) {
			/*
			 * re-encode body with the scheduled host so the target creates locally.
			 * Note that here we only switch the body, the actual proxying happens below
			 * in the next http_host_is_remote(o.Host) check.
			 */
			var buf bytes.Buffer
			err = json.NewEncoder(&buf).Encode(&o)
			if (err != nil) {
				logger.Log("vm_create: failed to encode scheduled request: %s", err.Error())
				http.Error(w, "failed", http.StatusInternalServerError)
				return
			}
			vr.Switch_body(buf.Bytes())
		}
	}
	if (http_host_is_remote(o.Host)) { /* need to proxy */
		http_proxy_request(o.Host, w, vr)
		return
	}
	uuid = New_uuid()
	if (uuid == "") {
		http.Error(w, "failed", http.StatusInternalServerError)
		return
	}
	ts_start := ts.Now()
	/* create storage if needed, can change o.Vmdef in some cases */
	stop_progress := httpx.Start_progress(r)
	created, err = storage.Create(&o.Vmdef, nil, uuid)
	stop_progress()
	if (err != nil) {
		logger.Log("vm_create_storage failed: %s", err.Error())
		storage.Rollback(created, uuid)
		http.Error(w, "storage creation failed", http.StatusInsufficientStorage)
		return
	}
	xml, err = vmdef.To_xml(&o.Vmdef, uuid)
	if (err != nil) {
		logger.Log("vmdef.To_xml failed: %s", err.Error())
		storage.Rollback(created, uuid)
		http.Error(w, "invalid parameters", http.StatusBadRequest)
		return
	}
	xml, err = hypervisor.Define_domain(xml)
	if (err != nil) {
		logger.Log("hypervisor.Define_domain failed: %s", err.Error())
		storage.Rollback(created, uuid)
		http.Error(w, "could not define VM", http.StatusFailedDependency)
		return
	}
	reg_err := reg.Save(machine.Uuid(), uuid, xml)
	if (reg_err != nil) {
		logger.Log("vm_create: reg.Save failed: %s", reg_err.Error())
		w.Header().Set("Warning", `299 VirtX "VM created but registration failed"`)
	} else {
		msg := fmt.Sprintf("name: %s, mem: %d MiB(hp: %t), numa: %t, osdisk: %s", o.Vmdef.Name,
			o.Vmdef.Memory.Total, o.Vmdef.Memory.Hp, o.Vmdef.Numa.Placement, o.Vmdef.Osdisk.Path)
		oplog_err := oplog.StartEnd(uuid, openapi.OpVmCreate, openapi.OPERATION_COMPLETED, httpx.Client_ip(r), msg, "Created.", ts_start)
		if (oplog_err != nil) {
			logger.Log("vm_create: oplog: %s", oplog_err.Error())
		}
	}
	var buf bytes.Buffer
	err = json.NewEncoder(&buf).Encode(&uuid)
	if (err != nil) {
		logger.Log("failed to encode JSON")
		w.Header().Set("Warning", `299 VirtX "failed to encode JSON"`)
	}
	httpx.Do_response(w, http.StatusCreated, &buf)
}
