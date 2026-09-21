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
package hypervisor

import (
	"libvirt.org/go/libvirt"

	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/machine"
)

func Define_domain(xml string, uuid string) error {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
	)
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
	if (err != nil) {
		return err
	}
	defer conn.Close()
	domain, err = conn.DomainDefineXML(xml)
	if (err != nil) {
		return err
	}
	defer domain.Free()
	xml, err = domain.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if (err != nil) {
		return err
	}
	/* store the processed XML in /vms/reg/host-uuid/vm-uuid/vm-uuid.xml */
	err = reg.Save(machine.Uuid(), uuid, xml)
	if (err != nil) {
		logger.Log("Define_domain: failed to reg.Save(%s, %s)", machine.Uuid(), uuid)
	}
	return nil
}
