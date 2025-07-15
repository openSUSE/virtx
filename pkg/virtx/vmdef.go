package virtx

import (
	"math/bits"
	"errors"
	"strings"
	"path/filepath"

	"suse.com/virtx/pkg/model"
	"libvirt.org/go/libvirtxml"
)

/*
 * Return the index of the OS disk from a Vmdef, as per virtx convention.
 * The main OS disk is the first non-CDROM disk.
 */
func vmdef_find_os_disk(vmdef *openapi.Vmdef) int {
	var i, n int = 0, len(vmdef.Disks)
	for i = 0; i < n; i++ {
		if (vmdef.Disks[i].Device == openapi.DEVICE_DISK) {
			return i
		}
	}
	return -1
}

/* calculate the virtxml path from the os disk */
func vmdef_xml_path(vmdef *openapi.Vmdef) string {
	var os_disk int = vmdef_find_os_disk(vmdef)
	if (os_disk < 0) {
		return ""
	}
	var p string = vmdef.Disks[os_disk].Path
	p = strings.TrimSuffix(p, filepath.Ext(p)) + ".xml"
	return p
}

/*
 * Return the number of vcpus from a Vmdef
 */
func vmdef_get_vcpus(vmdef *openapi.Vmdef) uint {
	return uint(vmdef.Cpudef.Sockets * vmdef.Cpudef.Cores * vmdef.Cpudef.Threads);
}

/* get disk driver type from path, or "" if not recognized */
func disk_driver_from_path(p string) string {
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

/* validate before generating the xml */
func vmdef_validate(vmdef *openapi.Vmdef) error {
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
	if (len(vmdef.Disks) < 1 || len(vmdef.Disks) > DISKS_MAX) {
		return errors.New("invalid Disks")
	}
	if (vmdef_find_os_disk(vmdef) < 0) {
		return errors.New("no OS Disk")
	}
	if (len(vmdef.Nets) > NETS_MAX) {
		return errors.New("invalid Nets")
	}
	if (vmdef.Vlanid < 0 || vmdef.Vlanid > VLAN_MAX) {
		return errors.New("invalid Vlanid")
	}
	/* *** DISKS *** */
	for _, disk := range vmdef.Disks {
		if (disk.Size < 0) {
			return errors.New("invalid Disk Size")
		}
		if (disk.Path == "" || !filepath.IsAbs(disk.Path)) {
			return errors.New("invalid Disk Path")
		}
		var path string = filepath.Clean(disk.Path)
		/* XXX disable for now
		path, err = filepath.EvalSymlinks(path)
		if (err != nil) {
			return errors.New("invalid Disk Path")
		}
		*/
		var disk_driver string = disk_driver_from_path(path)
		if (path != disk.Path || !strings.HasPrefix(disk.Path, VMS_DIR) || disk_driver == "") {
			/* symlink shenanigans, or not starting with /vms/ or invalid ext : bail */
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

/* vmdef_validate needs to be called before this! */
func vmdef_to_xml(vmdef *openapi.Vmdef) (string, error) {
	var (
		xml string
		err error
	)
	var vcpus uint = vmdef_get_vcpus(vmdef)
	domain_vcpu := libvirtxml.DomainVCPU{
		Value: vcpus,
	}
	domain_cpu := libvirtxml.DomainCPU{
		Migratable: "on",
		Check: "none",
		MaxPhysAddr: func() *libvirtxml.DomainCPUMaxPhysAddr {
			phys_bits := uint(bits.Len64(uint64(vmdef.Memory.Total)) + 20)
			if (phys_bits < 36) { /* minimum required by AMD64 spec */
				phys_bits = 36;
			}
			return &libvirtxml.DomainCPUMaxPhysAddr{
				Mode: "emulate",
				Bits: phys_bits,
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
		BootDevices: []libvirtxml.DomainBootDevice{
			{ Dev: "hd" },
			{ Dev: "cdrom" },
		},
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
		iothread_count int
	)
	disk_count := make(map[string]int)      /* keep track of how many disks require a bus type */
	for _, disk := range vmdef.Disks {
		var path string = filepath.Clean(disk.Path)
		path, err = filepath.EvalSymlinks(path)
		if (err != nil) {
			return "", errors.New("invalid Disk Path")
		}
		var disk_driver string = disk_driver_from_path(path)
		if (path != disk.Path || !strings.HasPrefix(disk.Path, VMS_DIR) || disk_driver == "") {
			/* symlink shenanigans, or not starting with /vms/ or invalid ext : bail */
			return "", errors.New("invalid Disk Path")
		}
		var (
			ctrl_type, ctrl_model string
			use_iothread bool
			device_prefix, device_name string
		)
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
			domain_controller := libvirtxml.DomainController{
				/* XMLName: */
				Type: ctrl_type,
				Index: &controller_index,
				Model: ctrl_model,
				Driver: func() *libvirtxml.DomainControllerDriver {
					if (use_iothread) {
						/* iothread index starts from 1! */
						iothread_count += 1
						return &libvirtxml.DomainControllerDriver{
							IOThread: uint(iothread_count),
						}
					}
					return nil
				}(),
			}
			domain_controllers = append(domain_controllers, domain_controller)
		}
		//domain_controllers = append(domain_controllers, libvirtxml.DomainController{ Type: "usb", Model: "none" })
		domain_disk := libvirtxml.DomainDisk{
			/* XMLName:, */
			Device: disk.Device.String(),
			/* Model:, */
			Driver: &libvirtxml.DomainDiskDriver{
				Name: "qemu",
				Type: disk_driver,
				Cache: "none",
				IOThread: func() *uint {
					if (ctrl_type == "virtio" && use_iothread) { /* virtio-blk. */
						iothread_count += 1
						var iothread_index uint = uint(iothread_count)
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
			/* Boot:, */
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
		domain_disks = append(domain_disks, domain_disk)
		/* controller index per type starts from 0 */
		disk_count[ctrl_type] += 1
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
					Type: "ramfb",
				},
			}, {
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
	domain_metadata_xml := `<virtx:data xmlns:virtx="virtx">`
	xmlpath := vmdef_xml_path(vmdef)
	if (xmlpath == "") {
		return "", errors.New("missing DEVICE_DISK")
	}
	domain_metadata_xml += `<virtxml>` + xmlpath + `</virtxml>`
	for _, custom := range vmdef.Custom {
		if (custom.Name == "") {
			continue
		}
		domain_metadata_xml += `<field name="` + custom.Name + `">` + custom.Value + `</field>`
	}
	domain_metadata_xml += `</virtx:data>`
	/* build xml */
	domain := libvirtxml.Domain{
		/* XMLName:, */
		Type: "kvm",
		/* ID:, */
		Name: vmdef.Name,
		/* UUID:, */
		GenID: domain_genid,
		Title: vmdef.Name,
		Description: vmdef.Name,
		Metadata: &libvirtxml.DomainMetadata{ XML: domain_metadata_xml, },
		Memory: &domain_memory,
		MemoryBacking: domain_memory_backing,
		VCPU: &domain_vcpu,
		IOThreads: uint(iothread_count),
		NUMATune: domain_numatune,
		OS: &domain_os,
		CPU: &domain_cpu,
		Clock: &domain_clock,
		PM: &domain_pm,
		Devices: &domain_devices,
	}
	xml, err = domain.Marshal()
	return xml, err
}
