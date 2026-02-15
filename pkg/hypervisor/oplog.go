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

package hypervisor

import (
	"errors"

	"libvirt.org/go/libvirt"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/metadata"
	"suse.com/virtx/pkg/ts"
)

/* record the operation metadata into the domain XML */
func oplog_record(domain *libvirt.Domain, op openapi.Operation, state openapi.OperationState, msg string, ts int64, te int64) error {
	var (
		err error
		xmlstr string
		meta metadata.Operation
		impact libvirt.DomainModificationImpact = libvirt.DOMAIN_AFFECT_CONFIG
	)
	xmlstr, err = meta.To_xml(op, state, msg, ts, te)
	if (err != nil) {
		return err
	}
	err = domain.SetMetadata(libvirt.DOMAIN_METADATA_ELEMENT, string(xmlstr),
		meta.XMLName.Local, meta.XMLName.Space, impact)
	if (err != nil) {
		return err
	}
	return nil
}

/* load the record from the domain XML */
func oplog_load(domain *libvirt.Domain, op openapi.Operation, state *openapi.OperationState, msg *string, ts *int64, te *int64) error {
	var (
		err error
		xmlstr string
		meta metadata.Operation
		impact libvirt.DomainModificationImpact = libvirt.DOMAIN_AFFECT_CONFIG
	)
	xmlstr, err = domain.GetMetadata(libvirt.DOMAIN_METADATA_ELEMENT, "virtx-op-" + op.String(), impact)
	if (err != nil) {
		return err
	}
	err = meta.From_xml(xmlstr, &op, state, msg, ts, te)
	if (err != nil) {
		return err
	}
	return nil
}

/* record completion of a long running op when it finally completes */
func oplog_complete(domain *libvirt.Domain, op openapi.Operation, msg string) error {
	var (
		state openapi.OperationState
		oldmsg string
		started, te int64
		err error
	)
	err = oplog_load(domain, op, &state, &oldmsg, &started, &te)
	if (err != nil) {
		return err
	}
	if (state != openapi.OPERATION_STARTED) {
		return errors.New("operation is not in state: started")
	}
	return oplog_record(domain, op, openapi.OPERATION_COMPLETED, msg, started, ts.Now())
}
