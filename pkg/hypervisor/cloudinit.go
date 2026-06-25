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
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, see
 * <https://www.gnu.org/licenses/>
 */

package hypervisor

import (
	"fmt"
	"errors"

	"encoding/xml" /* XXX necessary due to missing Marshal() for libvirtxml.DomainLease XXX */
	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"

	"suse.com/virtx/pkg/cloudinit"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/vmdef"
)

/*
 * cloudinit_boot_domain validates options and routes to the appropriate delivery method
 */
func cloudinit_boot_domain(uuid string, conn *libvirt.Connect, domain *libvirt.Domain, ci []openapi.CloudInitOption, method openapi.CloudInitMethod) error {
	err := cloudinit.Validate_options(ci)
	if (err != nil) {
		return fmt.Errorf("cloudinit: %w", err)
	}
	switch (method) {
	case openapi.CI_METHOD_FWCFG:
		return cloudinit_fwcfg_boot_domain(uuid, conn, domain, ci)
	case openapi.CI_METHOD_ISO:
		return cloudinit_iso_boot_domain(uuid, domain, ci)
	default:
		return fmt.Errorf("cloudinit: unknown method: %d", int16(method))
	}
}

/*
 * cloudinit_iso_boot_domain starts the domain, then creates the NoCloud ISO
 * and hot-attaches it as a CDROM.
 */
func cloudinit_iso_boot_domain(uuid string, domain *libvirt.Domain, ci []openapi.CloudInitOption) error {
	var (
		err error
		disk openapi.Disk
	)
	/*
	 * XXX
	 * We should do
	 * err = domain.CreateWithFlags(libvirt.DOMAIN_START_PAUSED)
	 * but unfortunately, another libvirt bug:
	 * https://gitlab.com/libvirt/libvirt/-/work_items/877
	 * XXX
	 */
	err = domain.Create()
	if (err != nil) {
		return fmt.Errorf("cloudinit iso: %w", err)
	}
	/*
	 * build the ISO after the domain is created so that domain destruction
	 * is detected and the ISO resource is removed.
	 */
	err = cloudinit.Create_disk(&disk, uuid, ci)
	if (err != nil) {
		_ = domain.DestroyFlags(0)
		return fmt.Errorf("cloudinit iso: %w", err)
	}
	err = cloudinit_attach(&disk, domain)
	if (err != nil) {
		_ = domain.DestroyFlags(0)
		return fmt.Errorf("cloudinit iso: %w", err)
	}
	/*
	 * XXX
	 * Here if we could do things properly (starting PAUSED),
	 * we would resume the domain
	 * err = domain.Resume()
	 * if (err != nil) {
	 *     _ = domain.DestroyFlags(0)
	 *     return err
	 * }
	 * XXX
	 */
	return nil
}

/*
 * cloudinit_attach attaches the cloud init Disk to a domain.
 */
func cloudinit_attach(disk *openapi.Disk, domain *libvirt.Domain) error {
	var (
		err error
		lease_xml, disk_xml string
	)
	lease_xml, disk_xml, err = get_iso_xml(disk)
	if (err != nil) {
		return err
	}
	err = domain.AttachDeviceFlags(lease_xml, libvirt.DOMAIN_DEVICE_MODIFY_LIVE)
	if (err != nil) {
		return fmt.Errorf("attaching Lease: %w", err)
	}
	err = domain.AttachDeviceFlags(disk_xml, libvirt.DOMAIN_DEVICE_MODIFY_LIVE)
	if (err != nil) {
		return fmt.Errorf("attaching Disk: %w", err)
	}
	logger.Debug("attached seed ISO %s", disk.Path)
	return nil
}

/*
 * Returns libvirt <lease> XML and the <disk> for the SCSI cloudinit CDROM
 * in this order (lease, disk, error)
 */
func get_iso_xml(disk *openapi.Disk) (string, string, error) {
	/* cloud-init controller is always 0 */
	var (
		err error
		iothread_count uint
		disk_count = make(map[string]int)
		domain_disks []libvirtxml.DomainDisk
		domain_leases []libvirtxml.DomainLease
		domain_controllers []libvirtxml.DomainController /* ignored, controller 0 is already there */
		order int = -1
		lease_bytes []byte
		lease_xml, disk_xml string
	)
	/* disk_count["scsi"] = 0 */
	err = vmdef.Disk_to_xml(disk, disk_count, &iothread_count, &domain_disks, &domain_leases, &domain_controllers, order)
	if (err != nil) {
		return "", "", err
	}
	if (len(domain_disks) != 1 || len(domain_leases) != 1) {
		return "", "", errors.New("failed to convert Disk to XML")
	}
	/*
	 * XXX
	 * libvirtxml package is missing the necessary Marshal() method for leases:
	 * domain_leases[0].Marshal()
	 * https://gitlab.com/libvirt/libvirt-go-module/-/work_items/25
	 * XXX
	 */
	s := struct {
		XMLName xml.Name `xml:"lease"`
		*libvirtxml.DomainLease
	}{
		DomainLease: &domain_leases[0],
	}
	lease_bytes, err = xml.Marshal(&s)
	if (err != nil) {
		return "", "", fmt.Errorf("marshalling lease XML: %w", err)
	}
	lease_xml = string(lease_bytes)
	disk_xml, err = domain_disks[0].Marshal()
	if (err != nil) {
		return "", "", fmt.Errorf("marshalling disk XML: %w", err)
	}
	return lease_xml, disk_xml, nil
}

/*
 * cloudinit_fwcfg_boot_domain injects cloud-init data as fw_cfg sysinfo
 * entries into the domain XML, starts the domain, then restores the original
 * domain definition.
 *
 * fw_cfg is QEMU machine initialization state: unlike CDROM drives, fw_cfg
 * entries are not hot-pluggable and there is no QMP command to add them to a
 * running machine. The data must therefore be present in the domain XML before
 * Create(). The persistent config is restored after boot so that subsequent
 * boots do not carry stale cloud-init data.
 */

func cloudinit_fwcfg_boot_domain(uuid string, conn *libvirt.Connect, domain *libvirt.Domain, ci []openapi.CloudInitOption) error {
	var (
		err, restore_err error
		original_xml, modified_xml string
		d libvirtxml.Domain
		domain2, domain3 *libvirt.Domain
	)
	slots := cloudinit.Create_fwcfg_slots(ci, uuid)

	/* get the current inactive domain XML so we can restore it after boot */
	original_xml, err = domain.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if (err != nil) {
		return fmt.Errorf("cloudinit fwcfg: get XML: %w", err)
	}
	if err = d.Unmarshal(original_xml); err != nil {
		return fmt.Errorf("cloudinit fwcfg: unmarshal: %w", err)
	}
	var entries []libvirtxml.DomainSysInfoEntry
	for _, slot := range slots {
		entries = append(entries, libvirtxml.DomainSysInfoEntry{
			Name:  slot.Name,
			Value: string(slot.Content),
		})
	}
	d.SysInfo = append(d.SysInfo, libvirtxml.DomainSysInfo{
		FWCfg: &libvirtxml.DomainSysInfoFWCfg{
			Entry: entries,
		},
	})
	modified_xml, err = d.Marshal()
	if (err != nil) {
		return fmt.Errorf("cloudinit fwcfg: marshal: %w", err)
	}
	/* temporarily redefine the domain with fw_cfg sysinfo */
	domain2, err = conn.DomainDefineXML(modified_xml)
	if (err != nil) {
		return fmt.Errorf("cloudinit fwcfg: redefine: %w", err)
	}
	defer domain2.Free()
	/* start the domain; fw_cfg data is now visible in the guest sysfs */
	err = domain2.Create()
	/* note: err checked after restoring the original definition */
	domain3, restore_err = conn.DomainDefineXML(original_xml)
	if (restore_err != nil) {
		logger.Log("cloudinit fwcfg: warning: failed to restore domain definition: %s", restore_err)
	} else {
		domain3.Free()
	}
	if (err != nil) {
		return fmt.Errorf("cloudinit fwcfg: %w", err)
	}
	return nil
}
