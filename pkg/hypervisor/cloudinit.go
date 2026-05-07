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

func cloudinit_boot_domain(uuid string, domain *libvirt.Domain, ci []openapi.CloudInitOption) error {
	var (
		err error
		disk openapi.Disk
		opts cloudinit.Options
	)
	/* translate []CloudInitOption into cloudinit.Options */
	for _, item := range ci {
		switch item.Name {
		case "ci-userdata":
			opts.UserData = item.Value
		case "ci-metadata":
			opts.MetaData = item.Value
		case "ci-networkconfig":
			opts.NetworkConfig = item.Value
		default:
			return fmt.Errorf("cloudinit: unknown option item name %s", item.Name)
		}
	}
	/* start the VM in paused state */
	err = domain.CreateWithFlags(libvirt.DOMAIN_START_PAUSED)
	if (err != nil) {
		return fmt.Errorf("cloudinit: %w", err)
	}
	/* build the ISO */
	err = cloudinit.Create_disk(&disk, uuid, &opts)
	if (err != nil) {
		_ = domain.DestroyFlags(0)
		return fmt.Errorf("cloudinit: %w", err)
	}
	/* attach the disk to the domain */
	err = cloudinit_attach(&disk, domain)
	if (err != nil) {
		_ = domain.DestroyFlags(0)
		return fmt.Errorf("cloudinit: %w", err)
	}
	err = domain.Resume()
	if (err != nil) {
		_ = domain.DestroyFlags(0)
		return err
	}
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
