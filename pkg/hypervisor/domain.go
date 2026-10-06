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

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/logger"
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
	ve.Runstate = domain_runstate(state, reason)
	ve.Host = machine.Uuid()
	return nil
}

/* map the libvirt state and reason of a domain to a runstate */
func domain_runstate(state libvirt.DomainState, reason int) openapi.Vmrunstate {
	/*
	 * We try to map states correctly, even though some will not be reachable yet,
	 * as long as we do not have HA enabled
	 */
	switch (state) {
	//case libvirt.DOMAIN_NOSTATE: /* RUNSTATE_NONE */
	case libvirt.DOMAIN_RUNNING:
		return openapi.RUNSTATE_RUNNING
	case libvirt.DOMAIN_BLOCKED: /* should be Xen only IIUC */
		logger.Log("XXX DOMAIN_BLOCKED encountered XXX")
		return openapi.RUNSTATE_PAUSED
	case libvirt.DOMAIN_PAUSED:
		switch (reason) {
		case int(libvirt.DOMAIN_PAUSED_MIGRATION): /* paused for offline migration */
			return openapi.RUNSTATE_MIGRATING
		case int(libvirt.DOMAIN_PAUSED_SHUTTING_DOWN):
			return openapi.RUNSTATE_TERMINATING
		case int(libvirt.DOMAIN_PAUSED_STARTING_UP):
			return openapi.RUNSTATE_STARTUP
		case int(libvirt.DOMAIN_PAUSED_WATCHDOG): fallthrough /* HA=off */
		case int(libvirt.DOMAIN_PAUSED_CRASHED): fallthrough  /* HA=off */
		default:
			return openapi.RUNSTATE_PAUSED
		}
	case libvirt.DOMAIN_SHUTDOWN:
		return openapi.RUNSTATE_TERMINATING
	case libvirt.DOMAIN_SHUTOFF:
		switch (reason) {
		case int(libvirt.DOMAIN_SHUTOFF_FAILED): fallthrough
		case int(libvirt.DOMAIN_SHUTOFF_DAEMON): fallthrough
		case int(libvirt.DOMAIN_SHUTOFF_CRASHED):
			/* If HA=on (unimplemented) on HA we will want to restart */
			return openapi.RUNSTATE_CRASHED
		case int(libvirt.DOMAIN_SHUTOFF_MIGRATED):
			/* XXX I started to see this in my migration tests since 16.1 XXX */
			logger.Log("XXX DOMAIN_SHUTOFF_MIGRATED encountered, started to see since 16.1 XXX")
			return openapi.RUNSTATE_MIGRATING
		default:
			return openapi.RUNSTATE_POWEROFF
		}
	case libvirt.DOMAIN_CRASHED:
		/* If HA=on (unimplemented), on HA we will want to configure on_crash="restart", so we don't even see this */
		return openapi.RUNSTATE_PANIC
	case libvirt.DOMAIN_PMSUSPENDED:
		return openapi.RUNSTATE_RUNNING
	}
	logger.Log("Unhandled state %d, reason %d", state, reason)
	return openapi.RUNSTATE_NONE
}
/*
 * get_domain_details fills the inventory.VmDetails read from the domain metadata: the
 * Name and the Custom fields. A domain without virtx-vm metadata is not an error, the
 * Custom fields will just be empty.
 */
func get_domain_details(d *libvirt.Domain, vd *inventory.VmDetails) error {
	var (
		meta metadata.Vm
		meta_xml, source string
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
	return meta.From_xml(meta_xml, &source, &vd.Custom)
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

/*
 * check with libvirt, unlike the inventory which can be stale, whether the domain is
 * defined on this host. A transient domain (f.e. the destination of a migration in progress)
 * is not defined. Any error other than no such domain is returned.
 */
func Is_defined(uuid string) (bool, error) {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		libvirt_err libvirt.Error
		ok bool
	)
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
	if (err != nil) {
		return false, err
	}
	defer conn.Close()
	domain, err = conn.LookupDomainByUUIDString(uuid)
	if (err != nil) {
		libvirt_err, ok = err.(libvirt.Error)
		if (ok && libvirt_err.Code == libvirt.ERR_NO_DOMAIN) {
			return false, nil
		}
		return false, err
	}
	defer domain.Free()
	return domain.IsPersistent()
}

/* get the current runstate of a domain from libvirt, unlike the inventory which can be stale */
func Get_runstate(uuid string) (openapi.Vmrunstate, error) {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		ve inventory.VmEvent
	)
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
	if (err != nil) {
		return openapi.RUNSTATE_NONE, err
	}
	defer conn.Close()
	domain, err = conn.LookupDomainByUUIDString(uuid)
	if (err != nil) {
		return openapi.RUNSTATE_NONE, err
	}
	defer domain.Free()
	err = get_domain_event(domain, &ve)
	if (err != nil) {
		return openapi.RUNSTATE_NONE, err
	}
	return ve.Runstate, nil
}
