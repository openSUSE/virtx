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
package vmdef

import (
	"fmt"
	"os"
	"testing"

	"libvirt.org/go/libvirtxml"

	"suse.com/virtx/pkg/lockman"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/metadata"
	"suse.com/virtx/pkg/model"
	. "suse.com/virtx/pkg/constants"
)

/*
 * TestMain sets the host architecture expected by To_xml. All tests in this
 * package run under x86_64. Tests that need a different arch temporarily
 * override and restore it.
 */
func TestMain(m *testing.M) {
	machine.Set_arch("x86_64")
	os.Exit(m.Run())
}

/* --- helpers --- */

/* Call To_xml and parse the result; fail fast on any error. */
func xml_to_domain(t *testing.T, vm *openapi.Vmdef, uuid string) libvirtxml.Domain {
	t.Helper()
	xmlstr, err := To_xml(vm, uuid)
	if (err != nil) {
		t.Fatalf("To_xml: %v", err)
	}
	var d libvirtxml.Domain
	err = d.Unmarshal(xmlstr)
	if (err != nil) {
		t.Fatalf("Unmarshal: %v", err)
	}
	return d
}

/*
 * Build a metadata XML string for an empty custom field list.
 * This is required by From_xml, which errors if the metadata element is absent.
 */
func empty_meta_xml(t *testing.T) string {
	t.Helper()
	var m metadata.Vm
	xml, err := m.To_xml(nil)
	if (err != nil) {
		t.Fatalf("metadata.Vm.To_xml: %v", err)
	}
	return xml
}

/*
 * Wrap inner domain XML in a <domain> element with a metadata section.
 * The result is suitable for passing to From_xml.
 *
 * Memory should be in KiB here (as libvirt normalises it) so that From_xml
 * sees the expected MiB value after its /KiB division.
 */
func wrap_domain(t *testing.T, inner string) string {
	t.Helper()
	return fmt.Sprintf(
		`<domain type='kvm'>%s<metadata>%s</metadata></domain>`,
		inner, empty_meta_xml(t),
	)
}

/* Call From_xml and return the parsed Vmdef; fail fast on any error. */
func xml_to_vmdef(t *testing.T, xmlstr string) openapi.Vmdef {
	t.Helper()
	var vm openapi.Vmdef
	err := From_xml(&vm, xmlstr)
	if (err != nil) {
		t.Fatalf("From_xml: %v", err)
	}
	return vm
}

/*
 * A minimal valid Vmdef for To_xml tests. Uses valid_vmdef() from vmdef_test.go
 * (same package) so the two test files share the same baseline definition.
 */
func xml_base_vmdef() openapi.Vmdef {
	return valid_vmdef()
}

/* ========================== To_xml tests ========================== */

func Test_to_xml_basic(t *testing.T) {
	vm := xml_base_vmdef()
	d := xml_to_domain(t, &vm, "test-uuid-1")

	if (d.Type != "kvm") {
		t.Errorf("Type: got %q, want kvm", d.Type)
	}
	if (d.UUID != "test-uuid-1") {
		t.Errorf("UUID: got %q", d.UUID)
	}
	if (d.Title != "testvm") {
		t.Errorf("Title: got %q, want testvm", d.Title)
	}
	if (d.Memory == nil) {
		t.Fatal("Memory is nil")
	}
	if (d.Memory.Value != 4096) {
		t.Errorf("Memory.Value: got %d, want 4096", d.Memory.Value)
	}
	if (d.Memory.Unit != "MiB") {
		t.Errorf("Memory.Unit: got %q, want MiB", d.Memory.Unit)
	}
	if (d.VCPU == nil) {
		t.Fatal("VCPU is nil")
	}
	/* 2 sockets * 4 cores * 1 thread = 8 */
	if (d.VCPU.Value != 8) {
		t.Errorf("VCPU.Value: got %d, want 8", d.VCPU.Value)
	}
	if (d.OS == nil || d.OS.Type == nil) {
		t.Fatal("OS or OS.Type is nil")
	}
	if (d.OS.Type.Machine != "q35") {
		t.Errorf("OS.Type.Machine: got %q, want q35", d.OS.Type.Machine)
	}
	if (d.OS.Firmware != "efi") {
		t.Errorf("OS.Firmware: got %q, want efi", d.OS.Firmware)
	}
}

func Test_to_xml_bios_firmware(t *testing.T) {
	vm := xml_base_vmdef()
	vm.Firmware = openapi.FIRMWARE_BIOS
	d := xml_to_domain(t, &vm, "test-uuid-2")

	if (d.OS == nil) {
		t.Fatal("OS is nil")
	}
	if (d.OS.Type == nil || d.OS.Type.Machine != "pc") {
		t.Errorf("OS.Type.Machine: got %q, want pc", d.OS.Type.Machine)
	}
	if (d.OS.Firmware != "bios") {
		t.Errorf("OS.Firmware: got %q, want bios", d.OS.Firmware)
	}
	if (d.OS.BIOS == nil) {
		t.Error("OS.BIOS should be set for BIOS firmware")
	}
}

func Test_to_xml_disk_virtio_blk(t *testing.T) {
	vm := xml_base_vmdef()
	/* Osdisk is BUS_VIRTIO_BLK in valid_vmdef() */
	d := xml_to_domain(t, &vm, "test-uuid-3")

	if (d.Devices == nil || len(d.Devices.Disks) == 0) {
		t.Fatal("no disks in output")
	}
	disk := d.Devices.Disks[0]
	if (disk.Target == nil || disk.Target.Dev != "vda") {
		t.Errorf("disk target dev: got %q, want vda", disk.Target.Dev)
	}
	if (disk.Target.Bus != "virtio") {
		t.Errorf("disk target bus: got %q, want virtio", disk.Target.Bus)
	}
	if (disk.Alias == nil) {
		t.Fatal("disk Alias is nil")
	}
	/* alias encodes man=M prov=t ctrl=virtio model=(empty) index=0 */
	if (disk.Alias.Name != "ua-M_t_virtio__0") {
		t.Errorf("disk Alias.Name: got %q, want ua-M_t_virtio__0", disk.Alias.Name)
	}
	if (disk.Boot == nil || disk.Boot.Order != 1) {
		t.Error("osdisk should have boot order 1")
	}
}

func Test_to_xml_disk_virtio_scsi(t *testing.T) {
	vm := xml_base_vmdef()
	vm.Osdisk.Bus = openapi.BUS_VIRTIO_SCSI
	d := xml_to_domain(t, &vm, "test-uuid-4")

	if (d.Devices == nil || len(d.Devices.Disks) == 0) {
		t.Fatal("no disks in output")
	}
	disk := d.Devices.Disks[0]
	/*
	 * scsi controller 0 is reserved for cloud-init, so the first regular
	 * scsi disk is index 1, which maps to device letter 'b' → "sdb".
	 */
	if (disk.Target == nil || disk.Target.Dev != "sdb") {
		t.Errorf("disk target dev: got %q, want sdb", disk.Target.Dev)
	}
	if (disk.Target.Bus != "scsi") {
		t.Errorf("disk target bus: got %q, want scsi", disk.Target.Bus)
	}
	/* alias: man=M prov=t ctrl=scsi model=virtio-scsi index=1 */
	if (disk.Alias == nil || disk.Alias.Name != "ua-M_t_scsi_virtio-scsi_1") {
		t.Errorf("disk Alias.Name: got %q, want ua-M_t_scsi_virtio-scsi_1", disk.Alias.Name)
	}
}

func Test_to_xml_disk_cdrom(t *testing.T) {
	vm := xml_base_vmdef()
	vm.Disks = []openapi.Disk{
		{
			Path:   "/vms/ds/testvm/install.iso",
			Device: openapi.DEVICE_CDROM,
			Bus:    openapi.BUS_SATA,
			Prov:   openapi.DISK_PROV_NONE,
			Man:    openapi.DISK_MAN_UNMANAGED,
		},
	}
	d := xml_to_domain(t, &vm, "test-uuid-5")

	/* find the cdrom disk */
	var cdrom *libvirtxml.DomainDisk
	for i, disk := range d.Devices.Disks {
		if (disk.Device == "cdrom") {
			cdrom = &d.Devices.Disks[i]
			break
		}
	}
	if (cdrom == nil) {
		t.Fatal("no cdrom disk found in output")
	}
	if (cdrom.ReadOnly == nil) {
		t.Error("cdrom should have ReadOnly set")
	}
	if (cdrom.Boot == nil) {
		t.Error("cdrom should have a boot order")
	}
}

func Test_to_xml_multiple_disks_boot_order(t *testing.T) {
	vm := xml_base_vmdef()
	vm.Disks = []openapi.Disk{
		{
			/* data disk: not bootable */
			Path:   "/vms/ds/testvm/data.qcow2",
			Device: openapi.DEVICE_DISK,
			Bus:    openapi.BUS_VIRTIO_BLK,
			Prov:   openapi.DISK_PROV_THIN,
			Man:    openapi.DISK_MAN_MANAGED,
			Size:   8192,
		},
		{
			/* cdrom: always gets a boot order */
			Path:   "/vms/ds/testvm/boot.iso",
			Device: openapi.DEVICE_CDROM,
			Bus:    openapi.BUS_SATA,
			Prov:   openapi.DISK_PROV_NONE,
			Man:    openapi.DISK_MAN_UNMANAGED,
		},
	}
	d := xml_to_domain(t, &vm, "test-uuid-6")

	var osdisk_order, cdrom_order uint
	var data_has_boot bool
	for _, disk := range d.Devices.Disks {
		switch disk.Device {
		case "cdrom":
			if (disk.Boot != nil) {
				cdrom_order = disk.Boot.Order
			}
		case "disk":
			if (disk.Boot != nil && disk.Boot.Order == 1) {
				osdisk_order = disk.Boot.Order
			} else if (disk.Boot != nil && disk.Boot.Order > 1) {
				data_has_boot = true
			}
		}
	}
	if (osdisk_order != 1) {
		t.Errorf("osdisk should have boot order 1, got %d", osdisk_order)
	}
	if (cdrom_order == 0) {
		t.Error("cdrom should have a boot order")
	}
	if (data_has_boot) {
		t.Error("data disk should not have a boot order > 1")
	}
}

func Test_to_xml_net_bridge(t *testing.T) {
	vm := xml_base_vmdef()
	vm.Nets = []openapi.Net{
		{
			Name:    "br0",
			Nettype: openapi.NET_BRIDGE,
			Model:   openapi.NET_MODEL_VIRTIO,
			Mac:     "52:54:00:12:34:56",
		},
	}
	d := xml_to_domain(t, &vm, "test-uuid-7")

	if (len(d.Devices.Interfaces) != 1) {
		t.Fatalf("expected 1 interface, got %d", len(d.Devices.Interfaces))
	}
	iface := d.Devices.Interfaces[0]
	if (iface.Source == nil || iface.Source.Bridge == nil) {
		t.Fatal("interface Source.Bridge is nil")
	}
	if (iface.Source.Bridge.Bridge != "br0") {
		t.Errorf("bridge name: got %q, want br0", iface.Source.Bridge.Bridge)
	}
	if (iface.MAC == nil || iface.MAC.Address != "52:54:00:12:34:56") {
		t.Errorf("MAC: got %q, want 52:54:00:12:34:56", iface.MAC.Address)
	}
	if (iface.Model == nil || iface.Model.Type != "virtio") {
		t.Errorf("model type: got %q, want virtio", iface.Model.Type)
	}
}

func Test_to_xml_net_libvirt(t *testing.T) {
	vm := xml_base_vmdef()
	vm.Nets = []openapi.Net{
		{
			Name:    "default",
			Nettype: openapi.NET_LIBVIRT,
			Model:   openapi.NET_MODEL_VIRTIO,
		},
	}
	d := xml_to_domain(t, &vm, "test-uuid-8")

	if (len(d.Devices.Interfaces) != 1) {
		t.Fatalf("expected 1 interface, got %d", len(d.Devices.Interfaces))
	}
	iface := d.Devices.Interfaces[0]
	if (iface.Source == nil || iface.Source.Network == nil) {
		t.Fatal("interface Source.Network is nil")
	}
	if (iface.Source.Network.Network != "default") {
		t.Errorf("network name: got %q, want default", iface.Source.Network.Network)
	}
}

func Test_to_xml_vlan(t *testing.T) {
	vm := xml_base_vmdef()
	vm.Vlanid = 100
	vm.Nets = []openapi.Net{
		{Name: "br0", Nettype: openapi.NET_BRIDGE, Model: openapi.NET_MODEL_VIRTIO},
	}
	d := xml_to_domain(t, &vm, "test-uuid-9")

	if (len(d.Devices.Interfaces) == 0) {
		t.Fatal("no interfaces in output")
	}
	iface := d.Devices.Interfaces[0]
	if (iface.VLan == nil || len(iface.VLan.Tags) == 0) {
		t.Fatal("VLan tags not set")
	}
	if (iface.VLan.Tags[0].ID != 100) {
		t.Errorf("vlan tag ID: got %d, want 100", iface.VLan.Tags[0].ID)
	}
}

func Test_to_xml_hugepages(t *testing.T) {
	vm := xml_base_vmdef()
	vm.Memory.Hp = true
	vm.Numa.Placement = true
	d := xml_to_domain(t, &vm, "test-uuid-10")

	if (d.MemoryBacking == nil || d.MemoryBacking.MemoryHugePages == nil) {
		t.Error("MemoryBacking.MemoryHugePages should be set for Hp=true")
	}
	if (d.NUMATune == nil || d.NUMATune.Memory == nil) {
		t.Error("NUMATune should be set for Placement=true")
	}
	if (d.NUMATune.Memory.Placement != "auto") {
		t.Errorf("NUMATune.Memory.Placement: got %q, want auto", d.NUMATune.Memory.Placement)
	}
}

func Test_to_xml_genid_auto(t *testing.T) {
	vm := xml_base_vmdef()
	vm.Genid = "auto"
	d := xml_to_domain(t, &vm, "test-uuid-11")

	if (d.GenID == nil) {
		t.Error("GenID should be present for Genid=auto")
	}
	/* auto means no explicit value: libvirt generates one */
	if (d.GenID.Value != "") {
		t.Errorf("GenID.Value: got %q, want empty for auto", d.GenID.Value)
	}
}

func Test_to_xml_genid_explicit(t *testing.T) {
	vm := xml_base_vmdef()
	vm.Genid = "43dc0cf8-809b-4adb-9bea-a9abb5f3d90e"
	d := xml_to_domain(t, &vm, "test-uuid-12")

	if (d.GenID == nil) {
		t.Fatal("GenID is nil")
	}
	if (d.GenID.Value != "43dc0cf8-809b-4adb-9bea-a9abb5f3d90e") {
		t.Errorf("GenID.Value: got %q", d.GenID.Value)
	}
}

func Test_to_xml_custom_fields(t *testing.T) {
	vm := xml_base_vmdef()
	vm.Custom = []openapi.CustomField{
		{Name: "CID", Value: "1217"},
		{Name: "ENV", Value: "prod"},
	}
	xmlstr, err := To_xml(&vm, "test-uuid-13")
	if (err != nil) {
		t.Fatalf("To_xml: %v", err)
	}
	/* parse metadata back out via From_xml to confirm the round-trip */
	var vm2 openapi.Vmdef
	err = From_xml(&vm2, xmlstr)
	if (err != nil) {
		t.Fatalf("From_xml: %v", err)
	}
	/*
	 * To_xml writes MiB; From_xml expects KiB, so memory will be wrong in
	 * a direct round-trip. We only check the custom fields here.
	 */
	if (len(vm2.Custom) != 2) {
		t.Fatalf("custom fields: got %d, want 2", len(vm2.Custom))
	}
	if (vm2.Custom[0].Name != "CID" || vm2.Custom[0].Value != "1217") {
		t.Errorf("custom[0]: got {%q,%q}", vm2.Custom[0].Name, vm2.Custom[0].Value)
	}
	if (vm2.Custom[1].Name != "ENV" || vm2.Custom[1].Value != "prod") {
		t.Errorf("custom[1]: got {%q,%q}", vm2.Custom[1].Name, vm2.Custom[1].Value)
	}
}

func Test_to_xml_invalid_arch(t *testing.T) {
	machine.Set_arch("unsupported-arch")
	defer machine.Set_arch("x86_64")

	vm := xml_base_vmdef()
	_, err := To_xml(&vm, "test-uuid-14")
	if (err == nil) {
		t.Error("expected error for unsupported arch")
	}
}

/* ========================== From_xml tests ========================== */

/*
 * Minimal domain XML that From_xml accepts. Memory is in KiB as libvirt
 * normalises it; From_xml divides by KiB to recover MiB.
 * 4096 MiB * 1024 = 4194304 KiB.
 *
 * The alias format for a managed thin virtio-blk disk at slot 0 is:
 *   ua-{Man}_{Prov}_{ctrl_type}_{ctrl_model}_{index}
 *   ua-M_t_virtio__0
 */
const base_inner_xml = `
  <title>myvm</title>
  <memory unit='KiB'>4194304</memory>
  <cpu mode='host-passthrough'>
    <topology sockets='2' cores='4' threads='1'/>
  </cpu>
  <os>
    <type machine='q35'>hvm</type>
    <firmware>efi</firmware>
  </os>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2' cache='none'/>
      <source file='/vms/ds/testvm/testvm.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <boot order='1'/>
      <alias name='ua-M_t_virtio__0'/>
    </disk>
  </devices>`

func Test_from_xml_basic(t *testing.T) {
	xml := wrap_domain(t, base_inner_xml)
	vm := xml_to_vmdef(t, xml)

	if (vm.Name != "myvm") {
		t.Errorf("Name: got %q, want myvm", vm.Name)
	}
	if (vm.Memory.Total != 4096) {
		t.Errorf("Memory.Total: got %d, want 4096 MiB", vm.Memory.Total)
	}
	if (vm.Cpudef.Sockets != 2) {
		t.Errorf("Cpudef.Sockets: got %d, want 2", vm.Cpudef.Sockets)
	}
	if (vm.Cpudef.Cores != 4) {
		t.Errorf("Cpudef.Cores: got %d, want 4", vm.Cpudef.Cores)
	}
	if (vm.Cpudef.Threads != 1) {
		t.Errorf("Cpudef.Threads: got %d, want 1", vm.Cpudef.Threads)
	}
	if (vm.Cpudef.Model != "host-passthrough") {
		t.Errorf("Cpudef.Model: got %q, want host-passthrough", vm.Cpudef.Model)
	}
	if (vm.Firmware != openapi.FIRMWARE_UEFI) {
		t.Errorf("Firmware: got %d, want UEFI", vm.Firmware)
	}
	if (vm.Osdisk.Path != "/vms/ds/testvm/testvm.qcow2") {
		t.Errorf("Osdisk.Path: got %q", vm.Osdisk.Path)
	}
	if (vm.Osdisk.Bus != openapi.BUS_VIRTIO_BLK) {
		t.Errorf("Osdisk.Bus: got %d, want BUS_VIRTIO_BLK", vm.Osdisk.Bus)
	}
	if (vm.Osdisk.Man != openapi.DISK_MAN_MANAGED) {
		t.Errorf("Osdisk.Man: got %d, want MANAGED", vm.Osdisk.Man)
	}
	if (vm.Osdisk.Prov != openapi.DISK_PROV_THIN) {
		t.Errorf("Osdisk.Prov: got %d, want THIN", vm.Osdisk.Prov)
	}
}

func Test_from_xml_bios_firmware(t *testing.T) {
	/* no <firmware> attribute; BIOS detected via <bios> element */
	inner := `
  <title>biosvm</title>
  <memory unit='KiB'>2097152</memory>
  <cpu mode='host-model'>
    <topology sockets='1' cores='2' threads='1'/>
  </cpu>
  <os>
    <type machine='pc'>hvm</type>
    <bios useserial='no'/>
  </os>
  <devices>
    <disk type='file' device='disk'>
      <source file='/vms/ds/biosvm/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <boot order='1'/>
      <alias name='ua-M_t_virtio__0'/>
    </disk>
  </devices>`
	xml := wrap_domain(t, inner)
	vm := xml_to_vmdef(t, xml)
	if (vm.Firmware != openapi.FIRMWARE_BIOS) {
		t.Errorf("Firmware: got %d, want FIRMWARE_BIOS", vm.Firmware)
	}
}

func Test_from_xml_multiple_disks(t *testing.T) {
	inner := `
  <title>multivm</title>
  <memory unit='KiB'>4194304</memory>
  <cpu mode='host-passthrough'>
    <topology sockets='1' cores='4' threads='1'/>
  </cpu>
  <os>
    <type machine='q35'>hvm</type>
    <firmware>efi</firmware>
  </os>
  <devices>
    <disk type='file' device='disk'>
      <source file='/vms/ds/multivm/os.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <boot order='1'/>
      <alias name='ua-M_t_virtio__0'/>
    </disk>
    <disk type='file' device='disk'>
      <source file='/vms/ds/multivm/data.qcow2'/>
      <target dev='vdb' bus='virtio'/>
      <alias name='ua-M_t_virtio__1'/>
    </disk>
  </devices>`
	xml := wrap_domain(t, inner)
	vm := xml_to_vmdef(t, xml)

	if (vm.Osdisk.Path != "/vms/ds/multivm/os.qcow2") {
		t.Errorf("Osdisk.Path: got %q", vm.Osdisk.Path)
	}
	if (len(vm.Disks) != 1) {
		t.Fatalf("Disks: got %d entries, want 1", len(vm.Disks))
	}
	if (vm.Disks[0].Path != "/vms/ds/multivm/data.qcow2") {
		t.Errorf("Disks[0].Path: got %q", vm.Disks[0].Path)
	}
}

func Test_from_xml_net(t *testing.T) {
	inner := `
  <title>netvm</title>
  <memory unit='KiB'>2097152</memory>
  <cpu mode='host-passthrough'>
    <topology sockets='1' cores='2' threads='1'/>
  </cpu>
  <os>
    <type machine='q35'>hvm</type>
    <firmware>efi</firmware>
  </os>
  <devices>
    <disk type='file' device='disk'>
      <source file='/vms/ds/netvm/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <boot order='1'/>
      <alias name='ua-M_t_virtio__0'/>
    </disk>
    <interface type='bridge'>
      <mac address='52:54:00:ab:cd:ef'/>
      <source bridge='br0'/>
      <model type='virtio'/>
    </interface>
  </devices>`
	xml := wrap_domain(t, inner)
	vm := xml_to_vmdef(t, xml)

	if (len(vm.Nets) != 1) {
		t.Fatalf("Nets: got %d entries, want 1", len(vm.Nets))
	}
	net := vm.Nets[0]
	if (net.Name != "br0") {
		t.Errorf("Net.Name: got %q, want br0", net.Name)
	}
	if (net.Nettype != openapi.NET_BRIDGE) {
		t.Errorf("Net.Nettype: got %d, want NET_BRIDGE", net.Nettype)
	}
	if (net.Model != openapi.NET_MODEL_VIRTIO) {
		t.Errorf("Net.Model: got %d, want NET_MODEL_VIRTIO", net.Model)
	}
	if (net.Mac != "52:54:00:ab:cd:ef") {
		t.Errorf("Net.Mac: got %q", net.Mac)
	}
}

func Test_from_xml_vlanid(t *testing.T) {
	inner := `
  <title>vlanvm</title>
  <memory unit='KiB'>2097152</memory>
  <cpu mode='host-passthrough'>
    <topology sockets='1' cores='2' threads='1'/>
  </cpu>
  <os>
    <type machine='q35'>hvm</type>
    <firmware>efi</firmware>
  </os>
  <devices>
    <disk type='file' device='disk'>
      <source file='/vms/ds/vlanvm/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <boot order='1'/>
      <alias name='ua-M_t_virtio__0'/>
    </disk>
    <interface type='bridge'>
      <source bridge='br0'/>
      <model type='virtio'/>
      <vlan>
        <tag id='200'/>
      </vlan>
    </interface>
  </devices>`
	xml := wrap_domain(t, inner)
	vm := xml_to_vmdef(t, xml)

	if (vm.Vlanid != 200) {
		t.Errorf("Vlanid: got %d, want 200", vm.Vlanid)
	}
}

func Test_from_xml_hugepages(t *testing.T) {
	inner := `
  <title>hpvm</title>
  <memory unit='KiB'>4194304</memory>
  <memoryBacking><hugepages/></memoryBacking>
  <cpu mode='host-passthrough'>
    <topology sockets='1' cores='4' threads='1'/>
  </cpu>
  <os>
    <type machine='q35'>hvm</type>
    <firmware>efi</firmware>
  </os>
  <devices>
    <disk type='file' device='disk'>
      <source file='/vms/ds/hpvm/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <boot order='1'/>
      <alias name='ua-M_t_virtio__0'/>
    </disk>
  </devices>`
	xml := wrap_domain(t, inner)
	vm := xml_to_vmdef(t, xml)
	if (!vm.Memory.Hp) {
		t.Error("Memory.Hp: expected true when memoryBacking/hugepages present")
	}
}

func Test_from_xml_numa(t *testing.T) {
	inner := `
  <title>numavm</title>
  <memory unit='KiB'>4194304</memory>
  <numatune><memory placement='auto'/></numatune>
  <cpu mode='host-passthrough'>
    <topology sockets='1' cores='4' threads='1'/>
  </cpu>
  <os>
    <type machine='q35'>hvm</type>
    <firmware>efi</firmware>
  </os>
  <devices>
    <disk type='file' device='disk'>
      <source file='/vms/ds/numavm/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <boot order='1'/>
      <alias name='ua-M_t_virtio__0'/>
    </disk>
  </devices>`
	xml := wrap_domain(t, inner)
	vm := xml_to_vmdef(t, xml)
	if (!vm.Numa.Placement) {
		t.Error("Numa.Placement: expected true when numatune present")
	}
}

func Test_from_xml_custom_fields(t *testing.T) {
	var m metadata.Vm
	meta_xml, err := m.To_xml([]openapi.CustomField{
		{Name: "CID", Value: "1217"},
		{Name: "ENV", Value: "prod"},
	})
	if (err != nil) {
		t.Fatalf("metadata.To_xml: %v", err)
	}
	xmlstr := fmt.Sprintf(
		`<domain type='kvm'>%s<metadata>%s</metadata></domain>`,
		base_inner_xml, meta_xml,
	)
	vm := xml_to_vmdef(t, xmlstr)

	if (len(vm.Custom) != 2) {
		t.Fatalf("Custom: got %d fields, want 2", len(vm.Custom))
	}
	if (vm.Custom[0].Name != "CID" || vm.Custom[0].Value != "1217") {
		t.Errorf("Custom[0]: got {%q,%q}", vm.Custom[0].Name, vm.Custom[0].Value)
	}
	if (vm.Custom[1].Name != "ENV" || vm.Custom[1].Value != "prod") {
		t.Errorf("Custom[1]: got {%q,%q}", vm.Custom[1].Name, vm.Custom[1].Value)
	}
}

func Test_from_xml_missing_cpu_topology(t *testing.T) {
	inner := `
  <title>badvm</title>
  <memory unit='KiB'>4194304</memory>
  <os><type machine='q35'>hvm</type><firmware>efi</firmware></os>
  <devices>
    <disk type='file' device='disk'>
      <source file='/vms/ds/bad/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <boot order='1'/>
      <alias name='ua-M_t_virtio__0'/>
    </disk>
  </devices>`
	var vm openapi.Vmdef
	err := From_xml(&vm, wrap_domain(t, inner))
	if (err == nil) {
		t.Error("expected error for missing CPU topology")
	}
}

func Test_from_xml_missing_memory(t *testing.T) {
	inner := `
  <title>badvm</title>
  <cpu mode='host-passthrough'><topology sockets='1' cores='1' threads='1'/></cpu>
  <os><type machine='q35'>hvm</type><firmware>efi</firmware></os>
  <devices>
    <disk type='file' device='disk'>
      <source file='/vms/ds/bad/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <boot order='1'/>
      <alias name='ua-M_t_virtio__0'/>
    </disk>
  </devices>`
	var vm openapi.Vmdef
	err := From_xml(&vm, wrap_domain(t, inner))
	if (err == nil) {
		t.Error("expected error for missing memory element")
	}
}

func Test_from_xml_missing_devices(t *testing.T) {
	inner := `
  <title>badvm</title>
  <memory unit='KiB'>4194304</memory>
  <cpu mode='host-passthrough'><topology sockets='1' cores='1' threads='1'/></cpu>
  <os><type machine='q35'>hvm</type><firmware>efi</firmware></os>`
	var vm openapi.Vmdef
	err := From_xml(&vm, wrap_domain(t, inner))
	if (err == nil) {
		t.Error("expected error for missing devices element")
	}
}

func Test_from_xml_missing_disk_alias(t *testing.T) {
	inner := `
  <title>badvm</title>
  <memory unit='KiB'>4194304</memory>
  <cpu mode='host-passthrough'><topology sockets='1' cores='1' threads='1'/></cpu>
  <os><type machine='q35'>hvm</type><firmware>efi</firmware></os>
  <devices>
    <disk type='file' device='disk'>
      <source file='/vms/ds/bad/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <boot order='1'/>
    </disk>
  </devices>`
	var vm openapi.Vmdef
	err := From_xml(&vm, wrap_domain(t, inner))
	if (err == nil) {
		t.Error("expected error for disk without alias")
	}
}

func Test_from_xml_invalid_disk_alias(t *testing.T) {
	inner := `
  <title>badvm</title>
  <memory unit='KiB'>4194304</memory>
  <cpu mode='host-passthrough'><topology sockets='1' cores='1' threads='1'/></cpu>
  <os><type machine='q35'>hvm</type><firmware>efi</firmware></os>
  <devices>
    <disk type='file' device='disk'>
      <source file='/vms/ds/bad/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <boot order='1'/>
      <alias name='ua-bad'/>
    </disk>
  </devices>`
	var vm openapi.Vmdef
	err := From_xml(&vm, wrap_domain(t, inner))
	if (err == nil) {
		t.Error("expected error for alias with too few underscore-separated fields")
	}
}

/* =================== Gap-coverage tests =================== */

/* --- Validate gap --- */

/* Test_validate_net_invalid_model is in vmdef_test.go (same package). */

/* --- Disk_to_xml: LUN path --- */

func Test_to_xml_disk_lun(t *testing.T) {
	vm := xml_base_vmdef()
	vm.Osdisk = openapi.Disk{
		Path:   "/dev/disk/by-id/wwn-0x50014ee0aef8c7a0",
		Device: openapi.DEVICE_LUN,
		Bus:    openapi.BUS_VIRTIO_SCSI,
		Prov:   openapi.DISK_PROV_NONE,
		Man:    openapi.DISK_MAN_MANAGED,
	}
	d := xml_to_domain(t, &vm, "test-uuid-lun")

	if (len(d.Devices.Disks) == 0) {
		t.Fatal("no disks in output")
	}
	disk := d.Devices.Disks[0]
	if (disk.Device != "lun") {
		t.Errorf("Device: got %q, want lun", disk.Device)
	}
	if (disk.RawIO != "yes") {
		t.Errorf("RawIO: got %q, want yes", disk.RawIO)
	}
	if (disk.Driver == nil || disk.Driver.Cache != "directsync") {
		t.Errorf("Driver.Cache: got %q, want directsync", disk.Driver.Cache)
	}
	if (disk.Source == nil || disk.Source.Block == nil) {
		t.Fatal("Source.Block is nil")
	}
	if (disk.Source.Block.Dev != "/dev/disk/by-id/wwn-0x50014ee0aef8c7a0") {
		t.Errorf("Source.Block.Dev: got %q", disk.Source.Block.Dev)
	}
	if (disk.Source.Reservations == nil || disk.Source.Reservations.Managed != "yes") {
		t.Error("Source.Reservations.Managed should be yes")
	}
	/* alias encodes man=M prov=U (NONE) ctrl=scsi model=virtio-scsi index=1 */
	if (disk.Alias == nil || disk.Alias.Name != "ua-M_U_scsi_virtio-scsi_1") {
		t.Errorf("Alias.Name: got %q, want ua-M_U_scsi_virtio-scsi_1", disk.Alias.Name)
	}
}

/* --- Disk_to_xml: BUS_SCSI (lsilogic) --- */

func Test_to_xml_disk_scsi_lsilogic(t *testing.T) {
	vm := xml_base_vmdef()
	vm.Osdisk.Bus = openapi.BUS_SCSI
	d := xml_to_domain(t, &vm, "test-uuid-lsilogic")

	if (len(d.Devices.Disks) == 0) {
		t.Fatal("no disks in output")
	}
	disk := d.Devices.Disks[0]
	/*
	 * scsi slot 0 is reserved for cloud-init; first regular scsi disk is
	 * slot 1 → letter 'b' → "sdb".
	 */
	if (disk.Target == nil || disk.Target.Dev != "sdb") {
		t.Errorf("Target.Dev: got %q, want sdb", disk.Target.Dev)
	}
	if (disk.Target.Bus != "scsi") {
		t.Errorf("Target.Bus: got %q, want scsi", disk.Target.Bus)
	}
	if (disk.Alias == nil || disk.Alias.Name != "ua-M_t_scsi_lsilogic_1") {
		t.Errorf("Alias.Name: got %q, want ua-M_t_scsi_lsilogic_1", disk.Alias.Name)
	}
	/* lsilogic controller should be present */
	var found bool
	for _, ctrl := range d.Devices.Controllers {
		if (ctrl.Type == "scsi" && ctrl.Model == "lsilogic") {
			found = true
			break
		}
	}
	if (!found) {
		t.Error("lsilogic scsi controller not found in output")
	}
}

/* --- Disk_to_xml/vmdef_lease: lease content --- */

func Test_to_xml_lease_content(t *testing.T) {
	vm := xml_base_vmdef()
	/* osdisk: managed thin virtio-blk at /vms/ds/testvm/testvm.qcow2 */
	d := xml_to_domain(t, &vm, "test-uuid-lease")

	if (len(d.Devices.Leases) == 0) {
		t.Fatal("no leases in output for managed disk")
	}
	lease := d.Devices.Leases[0]
	if (lease.Lockspace != LOCK_SPACE) {
		t.Errorf("Lockspace: got %q, want %q", lease.Lockspace, LOCK_SPACE)
	}
	want_key := lockman.Get_resource_name(openapi.DEVICE_DISK, vm.Osdisk.Path)
	if (lease.Key != want_key) {
		t.Errorf("Key: got %q, want %q", lease.Key, want_key)
	}
	want_path := lockman.Get_resource_path(want_key)
	if (lease.Target == nil || lease.Target.Path != want_path) {
		t.Errorf("Target.Path: got %q, want %q", lease.Target.Path, want_path)
	}
}

/* --- To_xml: named CPU model (non host-*) --- */

func Test_to_xml_named_cpu_model(t *testing.T) {
	vm := xml_base_vmdef()
	vm.Cpudef.Model = "Cascadelake-Server"
	d := xml_to_domain(t, &vm, "test-uuid-namedcpu")

	if (d.CPU == nil) {
		t.Fatal("CPU is nil")
	}
	/* named model: mode="" and Model element is set */
	if (d.CPU.Mode != "") {
		t.Errorf("CPU.Mode: got %q, want empty for named model", d.CPU.Mode)
	}
	if (d.CPU.Model == nil) {
		t.Fatal("CPU.Model is nil")
	}
	if (d.CPU.Model.Value != "Cascadelake-Server") {
		t.Errorf("CPU.Model.Value: got %q", d.CPU.Model.Value)
	}
	/* Migratable is only set for host-passthrough and maximum */
	if (d.CPU.Migratable != "") {
		t.Errorf("CPU.Migratable: got %q, want empty for named model", d.CPU.Migratable)
	}
}

/* --- From_xml: LUN disk (Block source) --- */

func Test_from_xml_disk_lun(t *testing.T) {
	inner := `
  <title>lunvm</title>
  <memory unit='KiB'>4194304</memory>
  <cpu mode='host-passthrough'>
    <topology sockets='1' cores='4' threads='1'/>
  </cpu>
  <os>
    <type machine='q35'>hvm</type>
    <firmware>efi</firmware>
  </os>
  <devices>
    <disk type='block' device='lun' rawio='yes'>
      <driver name='qemu' type='raw' cache='directsync'/>
      <source dev='/dev/disk/by-id/wwn-0x50014ee0aef8c7a0'/>
      <target dev='sdb' bus='scsi'/>
      <boot order='1'/>
      <alias name='ua-M_U_scsi_virtio-scsi_1'/>
    </disk>
  </devices>`
	vm := xml_to_vmdef(t, wrap_domain(t, inner))

	if (vm.Osdisk.Path != "/dev/disk/by-id/wwn-0x50014ee0aef8c7a0") {
		t.Errorf("Osdisk.Path: got %q", vm.Osdisk.Path)
	}
	if (vm.Osdisk.Device != openapi.DEVICE_LUN) {
		t.Errorf("Osdisk.Device: got %d, want DEVICE_LUN", vm.Osdisk.Device)
	}
	if (vm.Osdisk.Bus != openapi.BUS_VIRTIO_SCSI) {
		t.Errorf("Osdisk.Bus: got %d, want BUS_VIRTIO_SCSI", vm.Osdisk.Bus)
	}
	if (vm.Osdisk.Man != openapi.DISK_MAN_MANAGED) {
		t.Errorf("Osdisk.Man: got %d, want DISK_MAN_MANAGED", vm.Osdisk.Man)
	}
	if (vm.Osdisk.Prov != openapi.DISK_PROV_NONE) {
		t.Errorf("Osdisk.Prov: got %d, want DISK_PROV_NONE", vm.Osdisk.Prov)
	}
}

/* --- From_xml: libvirt network source --- */

func Test_from_xml_net_libvirt(t *testing.T) {
	inner := `
  <title>netvm</title>
  <memory unit='KiB'>2097152</memory>
  <cpu mode='host-passthrough'>
    <topology sockets='1' cores='2' threads='1'/>
  </cpu>
  <os>
    <type machine='q35'>hvm</type>
    <firmware>efi</firmware>
  </os>
  <devices>
    <disk type='file' device='disk'>
      <source file='/vms/ds/netvm/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <boot order='1'/>
      <alias name='ua-M_t_virtio__0'/>
    </disk>
    <interface type='network'>
      <source network='default'/>
      <model type='virtio'/>
    </interface>
  </devices>`
	vm := xml_to_vmdef(t, wrap_domain(t, inner))

	if (len(vm.Nets) != 1) {
		t.Fatalf("Nets: got %d entries, want 1", len(vm.Nets))
	}
	net := vm.Nets[0]
	if (net.Nettype != openapi.NET_LIBVIRT) {
		t.Errorf("Net.Nettype: got %d, want NET_LIBVIRT", net.Nettype)
	}
	if (net.Name != "default") {
		t.Errorf("Net.Name: got %q, want default", net.Name)
	}
}

/* --- From_xml: named CPU model (domain.CPU.Model branch) --- */

func Test_from_xml_named_cpu_model(t *testing.T) {
	inner := `
  <title>namedcpu</title>
  <memory unit='KiB'>2097152</memory>
  <cpu>
    <model>Cascadelake-Server</model>
    <topology sockets='1' cores='2' threads='1'/>
  </cpu>
  <os>
    <type machine='q35'>hvm</type>
    <firmware>efi</firmware>
  </os>
  <devices>
    <disk type='file' device='disk'>
      <source file='/vms/ds/namedcpu/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <boot order='1'/>
      <alias name='ua-M_t_virtio__0'/>
    </disk>
  </devices>`
	vm := xml_to_vmdef(t, wrap_domain(t, inner))

	if (vm.Cpudef.Model != "Cascadelake-Server") {
		t.Errorf("Cpudef.Model: got %q, want Cascadelake-Server", vm.Cpudef.Model)
	}
}

/* --- From_xml: missing OS element --- */

func Test_from_xml_missing_os(t *testing.T) {
	inner := `
  <title>badvm</title>
  <memory unit='KiB'>4194304</memory>
  <cpu mode='host-passthrough'><topology sockets='1' cores='1' threads='1'/></cpu>
  <devices>
    <disk type='file' device='disk'>
      <source file='/vms/ds/bad/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <boot order='1'/>
      <alias name='ua-M_t_virtio__0'/>
    </disk>
  </devices>`
	var vm openapi.Vmdef
	err := From_xml(&vm, wrap_domain(t, inner))
	if (err == nil) {
		t.Error("expected error for missing OS element")
	}
}

/* --- From_xml: missing metadata element --- */

func Test_from_xml_missing_metadata(t *testing.T) {
	/*
	 * Use base_inner_xml (which has cpu/memory/os/devices) but omit the
	 * <metadata> wrapper that wrap_domain would add.
	 */
	xmlstr := fmt.Sprintf(`<domain type='kvm'>%s</domain>`, base_inner_xml)
	var vm openapi.Vmdef
	err := From_xml(&vm, xmlstr)
	if (err == nil) {
		t.Error("expected error for missing metadata element")
	}
}
