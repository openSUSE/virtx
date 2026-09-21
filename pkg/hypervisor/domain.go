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
	"errors"
	"libvirt.org/go/libvirt"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/oplog"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/metadata"
)

/*
 * get_domain_event fills the runstate essentials (Uuid, Runstate, Host) of a Domain.
 *
 * Note that the timestamp is not set (ve.Ts). This is left to the caller, because in
 * most cases for a systeminfo collection we want to keep the same timestamp for all
 * related systeminfovms, equal to the host one. This is easier for debugging.
 * We also want to avoid tight loops calling gettimeofday() just to update the Ts for all
 * the VMs, if we have a large number of them.
 */
func get_domain_event(d *libvirt.Domain, ve *inventory.VmEvent) error {
	/* assert (hv.m.IsRLocked) */
	var (
		reason int
		state libvirt.DomainState
		err error
	)
	ve.Uuid, err = d.GetUUIDString()
	if (err != nil) {
		return err
	}
	state, reason, err = d.GetState()
	if (err != nil) {
		return err
	}
	logger.Debug("get_domain_event: state %d, reason %d", state, reason)
	switch (state) {
	//case libvirt.DOMAIN_NOSTATE: /* leave ve.Runstate RUNSTATE_NONE */
	case libvirt.DOMAIN_RUNNING:
		ve.Runstate = openapi.RUNSTATE_RUNNING
	case libvirt.DOMAIN_BLOCKED: /* should be Xen only IIUC */
		logger.Log("XXX DOMAIN_BLOCKED encountered XXX")
		ve.Runstate = openapi.RUNSTATE_PAUSED
	case libvirt.DOMAIN_PAUSED:
		switch (reason) {
		case int(libvirt.DOMAIN_PAUSED_MIGRATION): /* paused for offline migration */
			ve.Runstate = openapi.RUNSTATE_MIGRATING
		case int(libvirt.DOMAIN_PAUSED_SHUTTING_DOWN):
			ve.Runstate = openapi.RUNSTATE_TERMINATING
		case int(libvirt.DOMAIN_PAUSED_CRASHED):
			ve.Runstate = openapi.RUNSTATE_CRASHED
		case int(libvirt.DOMAIN_PAUSED_STARTING_UP):
			ve.Runstate = openapi.RUNSTATE_STARTUP
		default:
			ve.Runstate = openapi.RUNSTATE_PAUSED
		}
	case libvirt.DOMAIN_SHUTDOWN:
		ve.Runstate = openapi.RUNSTATE_TERMINATING
	case libvirt.DOMAIN_SHUTOFF:
		switch (reason) {
		case int(libvirt.DOMAIN_SHUTOFF_CRASHED):
			ve.Runstate = openapi.RUNSTATE_CRASHED
		case int(libvirt.DOMAIN_SHUTOFF_MIGRATED):
			/* XXX I started to see this in my migration tests since 16.1 XXX */
			logger.Log("XXX DOMAIN_SHUTOFF_MIGRATED encountered, started to see since 16.1 XXX")
			ve.Runstate = openapi.RUNSTATE_MIGRATING
		case int(libvirt.DOMAIN_SHUTOFF_DESTROYED):
			err = oplog.Complete(ve.Uuid, openapi.OpVmShutdown, "forced shutdown")
			if (err != nil) {
				logger.Log("get_domain_event: oplog.Complete: %s", err.Error())
			}
			ve.Runstate = openapi.RUNSTATE_POWEROFF
		case int(libvirt.DOMAIN_SHUTOFF_SHUTDOWN):
			err = oplog.Complete(ve.Uuid, openapi.OpVmShutdown, "graceful shutdown")
			if (err != nil) {
				logger.Log("get_domain_event: oplog.Complete: %s", err.Error())
			}
			fallthrough
		default:
			ve.Runstate = openapi.RUNSTATE_POWEROFF
		}
	case libvirt.DOMAIN_CRASHED:
		ve.Runstate = openapi.RUNSTATE_CRASHED
	case libvirt.DOMAIN_PMSUSPENDED:
		ve.Runstate = openapi.RUNSTATE_PMSUSPENDED
	default:
		logger.Log("Unhandled state %d, reason %d", state, reason)
	}
	ve.Host = machine.Uuid()
	return nil
}

/*
 * get_domain_details fills the inventory.VmDetails read from the domain metadata: the
 * Name and the Custom fields. A domain without virtx-vm metadata is not an error, the
 * Custom fields will just be empty.
 */
func get_domain_details(d *libvirt.Domain, vd *inventory.VmDetails) error {
	/* assert (hv.m.IsRLocked) */
	var (
		meta metadata.Vm
		meta_xml string
		err error
	)
	vd.Name, err = d.GetMetadata(libvirt.DOMAIN_METADATA_TITLE, "", libvirt.DOMAIN_AFFECT_CONFIG)
	if (err != nil) {
		return err
	}
	meta_xml, err = d.GetMetadata(libvirt.DOMAIN_METADATA_ELEMENT, "virtx-vm", libvirt.DOMAIN_AFFECT_CONFIG)
	if (err != nil) {
		return nil
	}
	return meta.From_xml(meta_xml, &vd.Custom)
}

func Dumpxml(uuid string) (string, error) {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		xml string
	)
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
	if (err != nil) {
		return "", err
	}
	defer conn.Close()
	domain, err = conn.LookupDomainByUUIDString(uuid)
	if (err != nil) {
		return "", err
	}
	defer domain.Free()
	xml, err = domain.GetXMLDesc(0)
	if (err != nil) {
		return "", err
	}
	return xml, nil
}

func Delete_domain(uuid string) error {
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
	domain, err = conn.LookupDomainByUUIDString(uuid)
	if (err != nil) {
		return err
	}
	defer domain.Free()
	var (
		ds libvirt.DomainState
		//reason int
	)
	ds, _, err = domain.GetState()
	if (err != nil) {
		return err
	}
	if (ds != libvirt.DOMAIN_SHUTOFF && ds != libvirt.DOMAIN_CRASHED) {
		return errors.New("libvirt domain is not SHUTOFF or CRASHED")
	}
	err = domain.UndefineFlags(libvirt.DOMAIN_UNDEFINE_MANAGED_SAVE |
		libvirt.DOMAIN_UNDEFINE_SNAPSHOTS_METADATA |
		libvirt.DOMAIN_UNDEFINE_NVRAM |
		libvirt.DOMAIN_UNDEFINE_CHECKPOINTS_METADATA)
	//libvirt.DOMAIN_UNDEFINE_TPM
	if (err != nil) {
		return err
	}
	/* remove the registered xml file */
	err = reg.Delete(machine.Uuid(), uuid)
	if (err != nil) {
		logger.Log("Delete_domain: failed to reg.Delete(%s, %s)", machine.Uuid(), uuid)
	}
	return nil
}
