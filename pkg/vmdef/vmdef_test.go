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
package vmdef

import (
	"testing"

	"suse.com/virtx/pkg/model"
)

func valid_vmdef() openapi.Vmdef {
	return openapi.Vmdef{
		Name: "testvm",
		Cpudef: openapi.Cpudef{
			Model:   "host-passthrough",
			Nodes:   1,
			Sockets: 2,
			Cores:   4,
			Threads: 1,
		},
		Memory: openapi.VmdefMemory{Total: 4096, Hp: false},
		Numa:   openapi.Numa{Placement: false},
		Osdisk: openapi.Disk{
			Path:   "/vms/ds/testvm/testvm.qcow2",
			Device: openapi.DEVICE_DISK,
			Bus:    openapi.BUS_VIRTIO_BLK,
			Prov:   openapi.DISK_PROV_THIN,
			Man:    openapi.DISK_MAN_MANAGED,
			Size:   16384,
		},
		Disks:    []openapi.Disk{},
		Nets:     []openapi.Net{},
		Vlanid:   0,
		Firmware: openapi.FIRMWARE_UEFI,
		Genid:    "",
		Custom:   []openapi.CustomField{},
	}
}

/* *** Disks *** */

func Test_disks_osdisk_only(t *testing.T) {
	vm := valid_vmdef()
	ptrs := Disks(&vm)
	if (len(ptrs) != 1) {
		t.Fatalf("Disks: expected 1, got %d", len(ptrs))
	}
	if (ptrs[0] != &vm.Osdisk) {
		t.Fatal("Disks[0] should point to Osdisk")
	}
}

func Test_disks_with_additional(t *testing.T) {
	vm := valid_vmdef()
	vm.Disks = []openapi.Disk{
		{Path: "/vms/ds/testvm/data.qcow2", Device: openapi.DEVICE_DISK, Bus: openapi.BUS_VIRTIO_BLK,
			Prov: openapi.DISK_PROV_THIN, Man: openapi.DISK_MAN_MANAGED, Size: 8192},
		{Path: "/vms/ds/testvm/extra.qcow2", Device: openapi.DEVICE_DISK, Bus: openapi.BUS_VIRTIO_BLK,
			Prov: openapi.DISK_PROV_THIN, Man: openapi.DISK_MAN_MANAGED, Size: 4096},
	}
	ptrs := Disks(&vm)
	if (len(ptrs) != 3) {
		t.Fatalf("Disks: expected 3, got %d", len(ptrs))
	}
	if (ptrs[0] != &vm.Osdisk) {
		t.Fatal("Disks[0] should be Osdisk")
	}
	if (ptrs[1] != &vm.Disks[0]) {
		t.Fatal("Disks[1] should be Disks[0]")
	}
	if (ptrs[2] != &vm.Disks[1]) {
		t.Fatal("Disks[2] should be Disks[1]")
	}
}

/* *** Has_path *** */

func Test_has_path(t *testing.T) {
	vm := valid_vmdef()
	vm.Disks = []openapi.Disk{
		{Path: "/vms/ds/testvm/data.qcow2", Device: openapi.DEVICE_DISK, Bus: openapi.BUS_VIRTIO_BLK,
			Prov: openapi.DISK_PROV_THIN, Man: openapi.DISK_MAN_MANAGED},
	}
	if (!Has_path(&vm, "/vms/ds/testvm/testvm.qcow2")) {
		t.Error("should find osdisk path")
	}
	if (!Has_path(&vm, "/vms/ds/testvm/data.qcow2")) {
		t.Error("should find additional disk path")
	}
	if (Has_path(&vm, "/vms/ds/testvm/nonexistent.qcow2")) {
		t.Error("should not find nonexistent path")
	}
}

/* *** Disk_driver *** */

func Test_disk_driver(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/vms/ds/vm/disk.qcow2", "qcow2"},
		{"/vms/ds/vm/disk.iso", "raw"},
		{"/vms/ds/vm/disk.raw", "raw"},
		{"/vms/ds/vm/disk.txt", ""},
		{"/vms/ds/vm/disk", ""},
		{"disk.qcow2", "qcow2"},
	}
	for _, tc := range cases {
		got := Disk_driver(tc.path)
		if (got != tc.want) {
			t.Errorf("Disk_driver(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

/* *** Validate_disk_path *** */

func Test_validate_disk_path(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"valid_qcow2", "/vms/ds/vm/disk.qcow2", "qcow2"},
		{"valid_iso", "/vms/ds/vm/disk.iso", "raw"},
		{"valid_raw", "/vms/ds/vm/disk.raw", "raw"},
		{"valid_dev", "/dev/sda", "raw"},
		{"valid_dev_mapper", "/dev/mapper/vg-lv", "raw"},
		{"empty", "", ""},
		{"relative", "vms/ds/vm/disk.qcow2", ""},
		{"unclean", "/vms/ds/../ds/vm/disk.qcow2", ""},
		{"wrong_prefix", "/tmp/disk.qcow2", ""},
		{"no_extension_under_ds", "/vms/ds/vm/disk", ""},
		{"unknown_extension", "/vms/ds/vm/disk.vmdk", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Validate_disk_path(tc.path)
			if (got != tc.want) {
				t.Errorf("Validate_disk_path(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

/* *** vmdef_validate_disk *** */

func Test_vmdef_validate_disk_valid(t *testing.T) {
	disk := openapi.Disk{
		Path:   "/vms/ds/testvm/testvm.qcow2",
		Device: openapi.DEVICE_DISK,
		Bus:    openapi.BUS_VIRTIO_BLK,
		Prov:   openapi.DISK_PROV_THIN,
		Man:    openapi.DISK_MAN_MANAGED,
		Size:   16384,
	}
	err := vmdef_validate_disk(&disk)
	if (err != nil) {
		t.Fatalf("valid disk rejected: %v", err)
	}
}

func Test_vmdef_validate_disk_negative_size(t *testing.T) {
	disk := openapi.Disk{
		Path: "/vms/ds/testvm/testvm.qcow2", Device: openapi.DEVICE_DISK,
		Bus: openapi.BUS_VIRTIO_BLK, Prov: openapi.DISK_PROV_THIN, Man: openapi.DISK_MAN_MANAGED,
		Size: -1,
	}
	err := vmdef_validate_disk(&disk)
	if (err == nil) {
		t.Fatal("negative size: expected error")
	}
}

func Test_vmdef_validate_disk_bad_path(t *testing.T) {
	disk := openapi.Disk{
		Path: "/tmp/bad.qcow2", Device: openapi.DEVICE_DISK,
		Bus: openapi.BUS_VIRTIO_BLK, Prov: openapi.DISK_PROV_THIN, Man: openapi.DISK_MAN_MANAGED,
		Size: 1024,
	}
	err := vmdef_validate_disk(&disk)
	if (err == nil) {
		t.Fatal("bad path: expected error")
	}
}

func Test_vmdef_validate_disk_lun_requires_virtio_scsi(t *testing.T) {
	disk := openapi.Disk{
		Path: "/dev/sda", Device: openapi.DEVICE_LUN,
		Bus: openapi.BUS_SATA, Prov: openapi.DISK_PROV_NONE, Man: openapi.DISK_MAN_UNMANAGED,
		Size: 0,
	}
	err := vmdef_validate_disk(&disk)
	if (err == nil) {
		t.Fatal("LUN on SATA bus: expected error")
	}

	disk.Bus = openapi.BUS_VIRTIO_SCSI
	err = vmdef_validate_disk(&disk)
	if (err != nil) {
		t.Fatalf("LUN on VIRTIO_SCSI: unexpected error: %v", err)
	}
}

func Test_vmdef_validate_disk_cdrom_bus_restrictions(t *testing.T) {
	base := openapi.Disk{
		Path: "/vms/ds/testvm/boot.iso", Device: openapi.DEVICE_CDROM,
		Prov: openapi.DISK_PROV_NONE, Man: openapi.DISK_MAN_UNMANAGED, Size: 0,
	}

	allowed := []openapi.DiskBus{openapi.BUS_VIRTIO_SCSI, openapi.BUS_SCSI, openapi.BUS_SATA}
	for _, bus := range allowed {
		disk := base
		disk.Bus = bus
		err := vmdef_validate_disk(&disk)
		if (err != nil) {
			t.Errorf("CDROM on bus %d: unexpected error: %v", bus, err)
		}
	}

	disk := base
	disk.Bus = openapi.BUS_VIRTIO_BLK
	err := vmdef_validate_disk(&disk)
	if (err == nil) {
		t.Error("CDROM on VIRTIO_BLK: expected error")
	}
}

/* *** Validate *** */

func Test_validate_valid_vmdef(t *testing.T) {
	vm := valid_vmdef()
	err := Validate(&vm)
	if (err != nil) {
		t.Fatalf("valid vmdef rejected: %v", err)
	}
}

func Test_validate_name(t *testing.T) {
	cases := []struct {
		name    string
		vmname  string
		wantErr bool
	}{
		{"empty", "", true},
		{"valid", "myvm", false},
		{"max_length", "abcdefghijklmnopqrstuvwxyz123456", false},
		{"too_long", "abcdefghijklmnopqrstuvwxyz1234567", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vm := valid_vmdef()
			vm.Name = tc.vmname
			err := Validate(&vm)
			if (tc.wantErr && err == nil) {
				t.Error("expected error")
			}
			if (!tc.wantErr && err != nil) {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func Test_validate_memory(t *testing.T) {
	vm := valid_vmdef()
	vm.Memory.Total = 0
	err := Validate(&vm)
	if (err == nil) {
		t.Error("zero memory: expected error")
	}

	vm.Memory.Total = -1
	err = Validate(&vm)
	if (err == nil) {
		t.Error("negative memory: expected error")
	}
}

func Test_validate_cpu(t *testing.T) {
	vm := valid_vmdef()
	vm.Cpudef.Model = ""
	err := Validate(&vm)
	if (err == nil) {
		t.Error("empty cpu model: expected error")
	}

	vm = valid_vmdef()
	vm.Cpudef.Sockets = 0
	err = Validate(&vm)
	if (err == nil) {
		t.Error("zero sockets: expected error")
	}

	vm = valid_vmdef()
	vm.Cpudef.Cores = 0
	err = Validate(&vm)
	if (err == nil) {
		t.Error("zero cores: expected error")
	}

	vm = valid_vmdef()
	vm.Cpudef.Threads = 0
	err = Validate(&vm)
	if (err == nil) {
		t.Error("zero threads: expected error")
	}

	vm = valid_vmdef()
	vm.Cpudef.Threads = 2
	err = Validate(&vm)
	if (err == nil) {
		t.Error("threads > 1: expected error (unsupported)")
	}
}

func Test_validate_genid(t *testing.T) {
	cases := []struct {
		name    string
		genid   string
		wantErr bool
	}{
		{"empty", "", false},
		{"auto", "auto", false},
		{"valid_uuid", "43dc0cf8-809b-4adb-9bea-a9abb5f3d90e", false},
		{"too_short", "abc", true},
		{"wrong_length", "43dc0cf8-809b-4adb-9bea", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vm := valid_vmdef()
			vm.Genid = tc.genid
			err := Validate(&vm)
			if (tc.wantErr && err == nil) {
				t.Error("expected error")
			}
			if (!tc.wantErr && err != nil) {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func Test_validate_no_osdisk(t *testing.T) {
	vm := valid_vmdef()
	vm.Osdisk.Path = ""
	err := Validate(&vm)
	if (err == nil) {
		t.Error("no osdisk: expected error")
	}
}

func Test_validate_too_many_disks(t *testing.T) {
	vm := valid_vmdef()
	vm.Disks = make([]openapi.Disk, 21)
	for i := range vm.Disks {
		vm.Disks[i] = openapi.Disk{
			Path: "/vms/ds/testvm/d.qcow2", Device: openapi.DEVICE_DISK,
			Bus: openapi.BUS_VIRTIO_BLK, Prov: openapi.DISK_PROV_THIN, Man: openapi.DISK_MAN_MANAGED,
			Size: 1024,
		}
	}
	err := Validate(&vm)
	if (err == nil) {
		t.Error("21 disks: expected error (max 20)")
	}
}

func Test_validate_too_many_nets(t *testing.T) {
	vm := valid_vmdef()
	vm.Nets = make([]openapi.Net, 9)
	for i := range vm.Nets {
		vm.Nets[i] = openapi.Net{
			Name: "br0", Nettype: openapi.NET_BRIDGE, Model: openapi.NET_MODEL_VIRTIO,
		}
	}
	err := Validate(&vm)
	if (err == nil) {
		t.Error("9 nets: expected error (max 8)")
	}
}

func Test_validate_vlanid(t *testing.T) {
	vm := valid_vmdef()
	vm.Vlanid = -1
	err := Validate(&vm)
	if (err == nil) {
		t.Error("vlanid -1: expected error")
	}

	vm.Vlanid = 4095
	err = Validate(&vm)
	if (err == nil) {
		t.Error("vlanid 4095: expected error (max 4094)")
	}

	vm.Vlanid = 4094
	err = Validate(&vm)
	if (err != nil) {
		t.Errorf("vlanid 4094: unexpected error: %v", err)
	}
}

func Test_validate_net_mac(t *testing.T) {
	vm := valid_vmdef()
	vm.Nets = []openapi.Net{
		{Name: "br0", Nettype: openapi.NET_BRIDGE, Model: openapi.NET_MODEL_VIRTIO,
			Mac: "52:54:00:8c:25:ef"},
	}
	err := Validate(&vm)
	if (err != nil) {
		t.Fatalf("valid MAC: unexpected error: %v", err)
	}

	vm.Nets[0].Mac = "bad"
	err = Validate(&vm)
	if (err == nil) {
		t.Error("bad MAC: expected error")
	}
}

func Test_validate_custom_field(t *testing.T) {
	vm := valid_vmdef()
	vm.Custom = []openapi.CustomField{
		{Name: "CID", Value: "1217"},
	}
	err := Validate(&vm)
	if (err != nil) {
		t.Fatalf("valid custom field: unexpected error: %v", err)
	}

	vm.Custom = []openapi.CustomField{
		{Name: "bad-name", Value: "1217"},
	}
	err = Validate(&vm)
	if (err == nil) {
		t.Error("non-alnum custom field name: expected error")
	}
}
