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

package vmdef

import (
	"math/bits"
	"errors"
	"strings"
	"path/filepath"
	"fmt"
	"encoding/json"
	"os"

	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/metadata"

	"libvirt.org/go/libvirtxml"
	. "suse.com/virtx/pkg/constants"
)

/*
 * check if the vmdef contains a certain path.
 * Initially implemented for the vm_update procedure for storage.
 */
func Has_path(vmdef *openapi.Vmdef, path string) bool {
	if (path == vmdef.Osdisk.Path) {
		return true
	}
	for _, disk := range vmdef.Disks {
		if (path == disk.Path) {
			return true
		}
	}
	return false
}

/*
 * get the path to the vmdef json file close to the OS disk, which is stored
 * for convenience for the user to recreate VM in the future if needed.
 */
func vmdef_osdisk_json(vmdef *openapi.Vmdef) string {
	var p string = vmdef.Osdisk.Path
	p = strings.TrimSuffix(p, filepath.Ext(p)) + ".json"
	return p
}

/*
 * Write the vmdef to disk near the OS disk for convenience in its original form
 * so it can be used as reference for the future to recreate VM.
 * Note that the registered processed XML is instead stored in /vms/xml/
 */
func Write_osdisk_json(vmdef *openapi.Vmdef) error {
	var (
		err error
		data []byte
		jsonfile string
	)
	jsonfile = vmdef_osdisk_json(vmdef)
	data, err = json.MarshalIndent(vmdef, "", "    ")
	if (err != nil) {
		return err
	}
	err = os.WriteFile(jsonfile, data, 0640)
	if (err != nil) {
		return err
	}
	return nil
}

/*
 * Return the number of vcpus from a Vmdef
 */
func vmdef_get_vcpus(vmdef *openapi.Vmdef) uint {
	return uint(vmdef.Cpudef.Sockets * vmdef.Cpudef.Cores * vmdef.Cpudef.Threads);
}

/* get disk driver type from path, or "" if not recognized */
func Disk_driver(p string) string {
	var (
		ext string
	)
	ext = filepath.Ext(p)
	switch (ext) {
	case ".qcow2":
		return "qcow2"
	case ".iso":
		fallthrough
	case ".raw":
		return "raw"
	}
	return ""
}

/* validate a disk path and return the driver for the disk, or "" on error */
func Validate_disk_path(path string) string {
	if (path == "" || !filepath.IsAbs(path)) {
		return ""
	}
	if (filepath.Clean(path) != path) {
		return ""
	}
	if (!strings.HasPrefix(path, DS_DIR)) {
		return ""
	}
	return Disk_driver(path)
}

func vmdef_validate_disk(disk *openapi.Disk) error {
	var (
		disk_driver string
	)
	if (disk.Size < 0) {
		return errors.New("invalid Disk Size")
	}
	disk_driver = Validate_disk_path(disk.Path)
	if (disk_driver == "") {
		return errors.New("invalid Disk Path")
	}
	if (!disk.Device.IsValid()) {
		return errors.New("invalid Disk Device")
	}
	if (!disk.Bus.IsValid()) {
		return errors.New("invalid Disk Bus")
	}
	if (!disk.Createmode.IsValid()) {
		return errors.New("invalid Disk Createmode")
	}
	switch (disk.Createmode) {
	case openapi.DISK_NOCREATE:
		if (disk.Size > 0) {
			return errors.New("invalid Disk Size")
		}
	default:
		if (disk.Size <= 0) {
			return errors.New("invalid Disk Size")
		}
	}
	return nil
}

/* validate before generating the xml */
func Validate(vmdef *openapi.Vmdef) error {
	var err error
	if (vmdef.Name == "" || len(vmdef.Name) > VM_NAME_MAX) {
		return errors.New("invalid Name length")
	}
	if (vmdef.Memory.Total < 1) {
		return errors.New("invalid memory size")
	}
	if (vmdef.Cpudef.Model == "") {
		return errors.New("no cpu model provided")
	}
	if (vmdef.Cpudef.Sockets < 1 ||	vmdef.Cpudef.Cores < 1 || vmdef.Cpudef.Threads < 1) {
		return errors.New("no cpu topology provided")
	}
	if (vmdef.Cpudef.Threads > 1) {
		return errors.New("unsupported cpu topology")
	}
	if (vmdef.Genid != "" && vmdef.Genid != "auto" && len(vmdef.Genid) != 36) {
		return errors.New("invalid Genid")
	}
	if (vmdef.Osdisk.Path == "") {
		return errors.New("no OS Disk")
	}
	if (len(vmdef.Disks) > DISKS_MAX) {
		return errors.New("invalid Disks")
	}
	if (len(vmdef.Nets) > NETS_MAX) {
		return errors.New("invalid Nets")
	}
	if (vmdef.Vlanid < 0 || vmdef.Vlanid > VLAN_MAX) {
		return errors.New("invalid Vlanid")
	}
	/* *** DISKS *** */
	err = vmdef_validate_disk(&vmdef.Osdisk)
	if (err != nil) {
		return err
	}
	for _, disk := range vmdef.Disks {
		err = vmdef_validate_disk(&disk)
		if (err != nil) {
			return err
		}
	}
	/* *** NETWORKS *** */
	for _, net := range vmdef.Nets {
		if (net.Mac != "" && len(net.Mac) != MAC_LEN) {
			return errors.New("invalid Mac")
		}
		if (!net.Model.IsValid()) {
			return errors.New("invalid Net model")
		}
	}
	/* *** CUSTOM FIELDS *** */
	for _, custom := range vmdef.Custom {
		if (custom.Name == "") {
			continue
		}
		if (!custom.IsAlnum()) {
			return errors.New("invalid Custom Field")
		}
	}
	return err
}

func vmdef_disk_to_xml(disk *openapi.Disk, disk_count map[string]int, iothread_count *uint,
	domain_disks *[]libvirtxml.DomainDisk, domain_controllers *[]libvirtxml.DomainController, order int) error {
	var (
		domain_disk libvirtxml.DomainDisk
		domain_controller libvirtxml.DomainController
		ctrl_type, ctrl_model, device_prefix, device_name string
		use_iothread bool
		disk_driver string = Validate_disk_path(disk.Path)
	)
	if (disk_driver == "") {
		return errors.New("invalid Disk Path")
	}
	switch (disk.Bus) {
	case openapi.BUS_SCSI:
		device_prefix = "sd"
		ctrl_type = "scsi"
		ctrl_model = "lsilogic"
		device_prefix = "sd"
	case openapi.BUS_VIRTIO_SCSI:
		device_prefix = "sd"
		ctrl_type = "scsi"
		ctrl_model = "virtio-scsi"
		use_iothread = true
	case openapi.BUS_SATA:
		device_prefix = "sd"
		ctrl_type = "sata"
	case openapi.BUS_VIRTIO_BLK:
		device_prefix = "vd"
		ctrl_type = "virtio"
		use_iothread = true
	}
	var controller_index uint = uint(disk_count[ctrl_type])
	var r rune
	if (ctrl_type == "virtio") {
		r = rune('a' + disk_count[ctrl_type])
	} else {
		r = rune('a' + disk_count["scsi"] + disk_count["sata"])
	}
	device_name = device_prefix + string(r);
	if (ctrl_model != "") {
		domain_controller = libvirtxml.DomainController{
			/* XMLName: */
			Type: ctrl_type,
			Index: &controller_index,
			Model: ctrl_model,
			Driver: func() *libvirtxml.DomainControllerDriver {
				if (use_iothread) {
					/* iothread index starts from 1! */
					*iothread_count += 1
					return &libvirtxml.DomainControllerDriver{
						IOThread: *iothread_count,
					}
				}
				return nil
			}(),
		}
		*domain_controllers = append(*domain_controllers, domain_controller)
	}
	domain_disk = libvirtxml.DomainDisk{
		/* XMLName:, */
		Device: disk.Device.String(),
		/* Model:, */
		Driver: &libvirtxml.DomainDiskDriver{
			Name: "qemu",
			Type: disk_driver,
			Cache: "none",
			IOThread: func() *uint {
				if (ctrl_type == "virtio" && use_iothread) { /* virtio-blk. */
					*iothread_count += 1
					var iothread_index uint = *iothread_count
					return &iothread_index
				}
				return nil
			}(),
		},
		Source: &libvirtxml.DomainDiskSource{
			File: &libvirtxml.DomainDiskSourceFile{
				File: disk.Path,
			},
		},
		Target: &libvirtxml.DomainDiskTarget{
			Dev: device_name,
			Bus: ctrl_type,
		},
		ReadOnly: func() *libvirtxml.DomainDiskReadOnly {
			if (disk.Device == openapi.DEVICE_CDROM) {
				return &libvirtxml.DomainDiskReadOnly{}
			}
			return nil
		}(),
		/* Shareable: */
		Boot: func() *libvirtxml.DomainDeviceBoot {
			if (order <= 0) {
				return nil
			} else {
				return &libvirtxml.DomainDeviceBoot{
					Order: uint(order),
				}
			}
		}(),
		Alias: &libvirtxml.DomainAlias{
			Name: fmt.Sprintf("ua-%s_%s_%s_%d",
				disk.Createmode.String(), ctrl_type, ctrl_model, disk_count[ctrl_type]),
		},
		Address: func() *libvirtxml.DomainAddress {
			if (ctrl_model == "") {
				return nil
			}
			return &libvirtxml.DomainAddress{
				Drive: &libvirtxml.DomainAddressDrive{
					Controller: &controller_index,
				},
			}
		}(),
	}
	*domain_disks = append(*domain_disks, domain_disk)
	/* controller index per type starts from 0 */
	disk_count[ctrl_type] += 1
	return nil
}

/*
 * vmdef_validate needs to be called before this!
 */
func To_xml(vmdef *openapi.Vmdef, uuid string) (string, error) {
	var (
		xmlstring string
		err error
		domain_features *libvirtxml.DomainFeatureList
		meta metadata.Vm
		meta_xml string
	)
	var vcpus uint = vmdef_get_vcpus(vmdef)
	domain_vcpu := libvirtxml.DomainVCPU{
		Value: vcpus,
	}
	switch (hypervisor.Arch()) {
	case "aarch64":
		domain_features = &libvirtxml.DomainFeatureList{
			ACPI: &libvirtxml.DomainFeature{},
		}
	case "x86_64":
		domain_features = &libvirtxml.DomainFeatureList{
			ACPI: &libvirtxml.DomainFeature{},
			APIC: &libvirtxml.DomainFeatureAPIC{ EOI: "on" },
			VMPort: &libvirtxml.DomainFeatureState{ State: "off" },
			IOAPIC: &libvirtxml.DomainFeatureIOAPIC{},
		}
	default:
		return "", errors.New("invalid architecture")
	}
	domain_cpu := libvirtxml.DomainCPU{
		Migratable: func() string {
			if (vmdef.Cpudef.Model == "host-passthrough" || vmdef.Cpudef.Model == "maximum") {
				return "on"
			} else {
				return ""
			}
		}(),
		Check: "none",
		MaxPhysAddr: func() *libvirtxml.DomainCPUMaxPhysAddr {
			phys_bits := uint(bits.Len64(uint64(vmdef.Memory.Total)) + 20)
			if (phys_bits < 36) { /* minimum required by AMD64 spec */
				phys_bits = 36;
			}
			return &libvirtxml.DomainCPUMaxPhysAddr{
				Mode: "passthrough",
				Limit: phys_bits,
			}
		}(),
		Topology: &libvirtxml.DomainCPUTopology{
			Sockets: int(vmdef.Cpudef.Sockets),
			Cores: int(vmdef.Cpudef.Cores),
			Threads: int(vmdef.Cpudef.Threads),
		},
		Mode: func() string {
			if (vmdef.Cpudef.Model == "host-model" || vmdef.Cpudef.Model == "host-passthrough" ||
				vmdef.Cpudef.Model == "maximum") {
				return vmdef.Cpudef.Model
			}
			return ""
		}(),
		Model: func() *libvirtxml.DomainCPUModel {
			if (vmdef.Cpudef.Model == "host-model" || vmdef.Cpudef.Model == "host-passthrough" ||
				vmdef.Cpudef.Model == "maximum") {
				return nil
			}
			return &libvirtxml.DomainCPUModel{
				Value: vmdef.Cpudef.Model,
			}
		}(),
	}
	domain_memory := libvirtxml.DomainMemory{
		Value: uint(vmdef.Memory.Total),
		Unit: "MiB",
	}
	domain_memory_backing := func() *libvirtxml.DomainMemoryBacking {
		if (vmdef.Memory.Hp) {
			return &libvirtxml.DomainMemoryBacking{
				MemoryHugePages: &libvirtxml.DomainMemoryHugepages{},
				MemoryAllocation: &libvirtxml.DomainMemoryAllocation{
					Mode: "immediate",
					Threads: vcpus,
				},
			}
		}
		return nil
	}()
	domain_numatune := func() *libvirtxml.DomainNUMATune {
		if (vmdef.Numa.Placement) {
			return &libvirtxml.DomainNUMATune{
				Memory: &libvirtxml.DomainNUMATuneMemory{
					Placement: "auto",
				},
			}
		}
		return nil
	}()
	domain_os := libvirtxml.DomainOS{
		Type: &libvirtxml.DomainOSType{
			Machine: vmdef.Firmware.Machine(), /* always use machine "pc" for BIOS and "q35" for UEFI */
			Type: "hvm",
		},
		Firmware: vmdef.Firmware.String(),
		/* - not used anymore, we use explicit boot order for each disk
		BootDevices: []libvirtxml.DomainBootDevice{
			{ Dev: "hd" },
			{ Dev: "cdrom" },
		},
		*/
	}
	domain_clock := libvirtxml.DomainClock{
		Timer: []libvirtxml.DomainTimer{
			{
				Name: "kvmclock",
				Present: "yes",
			},
		},
	}
	domain_pm := libvirtxml.DomainPM{
		SuspendToMem: &libvirtxml.DomainPMPolicy{
			Enabled: "no",
		},
		SuspendToDisk: &libvirtxml.DomainPMPolicy{
			Enabled: "no",
		},
	}

	/* *** DISKS, CONTROLLERS AND INTERFACES *** */

	var (
		domain_disks []libvirtxml.DomainDisk
		domain_controllers []libvirtxml.DomainController
		domain_interfaces []libvirtxml.DomainInterface
		iothread_count uint
		boot_order int = 1						/* primary disk first, then the cdroms */
	)
	disk_count := make(map[string]int)          /* keep track of how many disks require a bus type */
	err = vmdef_disk_to_xml(&vmdef.Osdisk, disk_count, &iothread_count, &domain_disks, &domain_controllers, boot_order)
	if (err != nil) {
		return "", err
	}
	for _, disk := range vmdef.Disks {
		var order int
		if (disk.Device == openapi.DEVICE_CDROM) {
			boot_order += 1
			order = boot_order
		} else {
			order = -1			/* other disks are not bootable */
		}
		err = vmdef_disk_to_xml(&disk, disk_count, &iothread_count, &domain_disks, &domain_controllers, order)
		if (err != nil) {
			return "", err
		}
	}
	/* *** NETWORKS *** */
	for _, net := range vmdef.Nets {
		iothread_count += 1
		domain_interface := libvirtxml.DomainInterface{
			/* XMLName:,*/
			/* Managed:,*/
			/* TrustGuestRXFilters:,*/
			MAC: func() *libvirtxml.DomainInterfaceMAC {
				if (net.Mac != "") {
					return &libvirtxml.DomainInterfaceMAC{
						Address: net.Mac,
					}
				}
				return nil
			}(),
			Source: func() *libvirtxml.DomainInterfaceSource {
				if (net.Nettype == openapi.NET_BRIDGE) {
					return &libvirtxml.DomainInterfaceSource{
						Bridge: &libvirtxml.DomainInterfaceSourceBridge{
							Bridge: net.Name,
						},
					}
				}
				if (net.Nettype == openapi.NET_LIBVIRT) {
					return &libvirtxml.DomainInterfaceSource{
						Network: &libvirtxml.DomainInterfaceSourceNetwork{
							Network: net.Name,
						},
					}
				}
				return nil
			}(),
			VLan: func() *libvirtxml.DomainInterfaceVLan {
				if (vmdef.Vlanid > 0) {
					return &libvirtxml.DomainInterfaceVLan{
						Tags: []libvirtxml.DomainInterfaceVLanTag{
							{ ID: uint(vmdef.Vlanid), },
						},
					}
				}
				return nil
			}(),
			Model: &libvirtxml.DomainInterfaceModel{
				Type: net.Model.String(),
			},
			Driver: &libvirtxml.DomainInterfaceDriver{
				TXMode: "iothread", /* XXX there is no way in libvirt to assign iothread ID to specific queue or interface XXX */
			},
		}
		domain_interfaces = append(domain_interfaces, domain_interface)
	}
	domain_devices := libvirtxml.DomainDeviceList{
		/* Emulator:, */
		Disks: domain_disks,
		Controllers: domain_controllers,
		/* Leases:, */
		/* Filesystems:, */
		Interfaces: domain_interfaces,
		Serials: nil,
		Parallels: nil,
		Consoles: []libvirtxml.DomainConsole{
			{
				Target: &libvirtxml.DomainConsoleTarget{
					Type: "serial",
				},
			}, {
				Target: &libvirtxml.DomainConsoleTarget{
					Type: "virtio",
				},
			},
		},
		Graphics: []libvirtxml.DomainGraphic{
			{
				VNC: &libvirtxml.DomainGraphicVNC{
					AutoPort: "yes",
				},
			},
		},
		Audios: []libvirtxml.DomainAudio{
			{
				ID: 1,
				None: &libvirtxml.DomainAudioNone{},
			},
		},
		Videos: []libvirtxml.DomainVideo{
			{
				Model: libvirtxml.DomainVideoModel{
					Type: "virtio",
				},
			},
		},
		/* Hostdevs:, */
		/* Watchdogs:, */
		MemBalloon: &libvirtxml.DomainMemBalloon{
			Model: "none",
		},
		RNGs: []libvirtxml.DomainRNG{
			{
				Model: "virtio",
				Backend: &libvirtxml.DomainRNGBackend{
					Random: &libvirtxml.DomainRNGBackendRandom{
						Device: "/dev/urandom",
					},
				},
			},
		},
		/* Panics:, */
		/* VSock:, */
	}
	domain_genid := func() *libvirtxml.DomainGenID {
		if (vmdef.Genid == "") {
			return nil
		}
		if (vmdef.Genid == "auto") {
			return &libvirtxml.DomainGenID{}
		}
		return &libvirtxml.DomainGenID{
			Value: vmdef.Genid,
		}
	}()
	meta_xml, err = meta.To_xml(vmdef.Custom)
	if (err != nil) {
		return "", err
	}
	/* build xml */
	domain := libvirtxml.Domain{
		/* XMLName:, */
		Type: "kvm",
		/* ID:, */
		Name: uuid, /* libvirt Name clashes can be a pain to solve, so just use the uuid */
		UUID: uuid,
		GenID: domain_genid,
		Title: vmdef.Name,		/* this will be used for all effects as the actual Name */
		Description: vmdef.Name,
		Metadata: &libvirtxml.DomainMetadata{ XML: meta_xml },
		Memory: &domain_memory,
		MemoryBacking: domain_memory_backing,
		VCPU: &domain_vcpu,
		IOThreads: uint(iothread_count),
		NUMATune: domain_numatune,
		OS: &domain_os,
		Features: domain_features,
		CPU: &domain_cpu,
		Clock: &domain_clock,
		PM: &domain_pm,
		Devices: &domain_devices,
	}
	xmlstring, err = domain.Marshal()
	return xmlstring, err
}

func From_xml(vmdef *openapi.Vmdef, xmlstr string) error {
	var (
		err error
		domain libvirtxml.Domain
		meta metadata.Vm
	)
	/* unmarshal the XML into the libvirtxml Domain configuration */
	err = domain.Unmarshal(xmlstr)
	if (err != nil) {
		return err
	}
	vmdef.Name = domain.Title
	if (domain.CPU == nil || domain.CPU.Topology == nil) {
		return errors.New("missing CPU.Topology")
	}
	vmdef.Cpudef.Sockets = int16(domain.CPU.Topology.Sockets)
	vmdef.Cpudef.Cores = int16(domain.CPU.Topology.Cores)
	vmdef.Cpudef.Threads = int16(domain.CPU.Topology.Threads)
	if (domain.CPU.Mode != "" && domain.CPU.Mode != "custom") {
		vmdef.Cpudef.Model = domain.CPU.Mode
	} else if (domain.CPU.Model != nil) {
		vmdef.Cpudef.Model = domain.CPU.Model.Value
	} else {
		return errors.New("missing CPU.Mode or CPU.Model")
	}
	if (domain.Memory == nil) {
		return errors.New("missing Memory");
	}
	vmdef.Memory.Total = int32(domain.Memory.Value / KiB) /* convert from KiB to MiB */
	if (domain.MemoryBacking != nil) {
		vmdef.Memory.Hp = true
	}
	if (domain.NUMATune != nil) {
		vmdef.Numa.Placement = true
	}
	if (domain.OS == nil) {
		return errors.New("missing OS");
	}
	err = vmdef.Firmware.Parse(domain.OS.Firmware)
	if (err != nil) {
		return err
	}
	/* DEVICES */
	if (domain.Devices == nil) {
		return errors.New("missing Devices");
	}
	/* DISKS */
	vmdef.Disks = []openapi.Disk{}
	for _, domain_disk := range domain.Devices.Disks {
		var (
			disk openapi.Disk
			ctrl_type, ctrl_model, create_mode string
		)
		if (domain_disk.Source == nil || domain_disk.Source.File == nil) {
			return errors.New("missing Disk Source File")
		}
		disk.Path = domain_disk.Source.File.File
		err = disk.Device.Parse(domain_disk.Device)
		if (err != nil) {
			return err
		}
		if (domain_disk.Target == nil) {
			return errors.New("missing Disk Target")
		}
		if (domain_disk.Alias == nil) {
			return errors.New("missing Disk Alias")
		}
		if (len(domain_disk.Alias.Name) < 5) {
			return errors.New("Disk Alias too short")
		}
		fields := strings.SplitN(domain_disk.Alias.Name[3:], "_", 4)
		if (len(fields) != 4) {
			return errors.New("invalid Disk Alias")
		}
		create_mode = fields[0]
		err = disk.Createmode.Parse(create_mode[0])
		if (err != nil) {
			return err
		}
		ctrl_type = fields[1]
		ctrl_model = fields[2]
		err = disk.Bus.Parse(ctrl_type, ctrl_model)
		if (err != nil) {
			return err
		}
		if (domain_disk.Boot != nil && domain_disk.Boot.Order == 1) {
			vmdef.Osdisk = disk	/* the primary OS disk */
		} else {
			vmdef.Disks = append(vmdef.Disks, disk)
		}
	}
	/* Networks */
	vmdef.Nets = []openapi.Net{}
	for _, domain_interface := range domain.Devices.Interfaces {
		var net openapi.Net
		if (domain_interface.MAC != nil) {
			net.Mac = domain_interface.MAC.Address
		}
		if (domain_interface.Source == nil) {
			return errors.New("missing Interface Source")
		}
		if (domain_interface.Source.Bridge != nil) {
			net.Name = domain_interface.Source.Bridge.Bridge
			net.Nettype = openapi.NET_BRIDGE
		} else if (domain_interface.Source.Network != nil) {
			net.Name = domain_interface.Source.Network.Network
			net.Nettype = openapi.NET_LIBVIRT
		} else {
			return errors.New("missing Interface Source Bridge or Network")
		}
		if (domain_interface.VLan != nil && len(domain_interface.VLan.Tags) > 0) {
			vmdef.Vlanid = int16(domain_interface.VLan.Tags[0].ID)
		} else {
			vmdef.Vlanid = 0
		}
		if (domain_interface.Model == nil) {
			return errors.New("missing Interface Model")
		}
		err = net.Model.Parse(domain_interface.Model.Type)
		if (err != nil) {
			return err
		}
		vmdef.Nets = append(vmdef.Nets, net)
	}
	if (domain.GenID != nil) {
		vmdef.Genid = domain.GenID.Value
	}
	/* METADATA */
	/*
	 * for now we ignore the <virtxml>path</virtxml> since we assume we can reconstruct it
	 * from the first disk path.
	 */
	if (domain.Metadata == nil) {
		return errors.New("missing Metadata")
	}
	err = meta.From_xml(domain.Metadata.XML, &vmdef.Custom)
	if (err != nil) {
		return err
	}
	return nil
}
