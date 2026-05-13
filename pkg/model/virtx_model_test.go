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
package openapi

import (
	"testing"
)

/* *** DiskDevice *** */

func Test_disk_device_string(t *testing.T) {
	cases := []struct {
		device DiskDevice
		want   string
	}{
		{DEVICE_DISK, "disk"},
		{DEVICE_CDROM, "cdrom"},
		{DEVICE_LUN, "lun"},
		{DiskDevice(99), ""},
	}
	for _, tc := range cases {
		got := tc.device.String()
		if (got != tc.want) {
			t.Errorf("DiskDevice(%d).String() = %q, want %q", tc.device, got, tc.want)
		}
	}
}

func Test_disk_device_parse(t *testing.T) {
	cases := []struct {
		input   string
		want    DiskDevice
		wantErr bool
	}{
		{"disk", DEVICE_DISK, false},
		{"cdrom", DEVICE_CDROM, false},
		{"lun", DEVICE_LUN, false},
		{"unknown", 0, true},
		{"", 0, true},
	}
	for _, tc := range cases {
		var device DiskDevice
		err := device.Parse(tc.input)
		if (tc.wantErr) {
			if (err == nil) {
				t.Errorf("DiskDevice.Parse(%q): expected error", tc.input)
			}
		} else {
			if (err != nil) {
				t.Errorf("DiskDevice.Parse(%q): %v", tc.input, err)
			} else if (device != tc.want) {
				t.Errorf("DiskDevice.Parse(%q) = %d, want %d", tc.input, device, tc.want)
			}
		}
	}
}

/* *** NetType *** */

func Test_net_type_string(t *testing.T) {
	cases := []struct {
		nettype NetType
		want    string
	}{
		{NET_BRIDGE, "bridge"},
		{NET_LIBVIRT, "network"},
		{NetType(99), ""},
	}
	for _, tc := range cases {
		got := tc.nettype.String()
		if (got != tc.want) {
			t.Errorf("NetType(%d).String() = %q, want %q", tc.nettype, got, tc.want)
		}
	}
}

/* *** NetModel *** */

func Test_net_model_string(t *testing.T) {
	cases := []struct {
		model NetModel
		want  string
	}{
		{NET_MODEL_VIRTIO, "virtio"},
		{NET_MODEL_E1000E, "e1000e"},
		{NET_MODEL_E1000, "e1000"},
		{NetModel(99), ""},
	}
	for _, tc := range cases {
		got := tc.model.String()
		if (got != tc.want) {
			t.Errorf("NetModel(%d).String() = %q, want %q", tc.model, got, tc.want)
		}
	}
}

func Test_net_model_parse(t *testing.T) {
	cases := []struct {
		input   string
		want    NetModel
		wantErr bool
	}{
		{"virtio", NET_MODEL_VIRTIO, false},
		{"e1000e", NET_MODEL_E1000E, false},
		{"e1000", NET_MODEL_E1000, false},
		{"bogus", 0, true},
	}
	for _, tc := range cases {
		var model NetModel
		err := model.Parse(tc.input)
		if (tc.wantErr) {
			if (err == nil) {
				t.Errorf("NetModel.Parse(%q): expected error", tc.input)
			}
		} else {
			if (err != nil) {
				t.Errorf("NetModel.Parse(%q): %v", tc.input, err)
			} else if (model != tc.want) {
				t.Errorf("NetModel.Parse(%q) = %d, want %d", tc.input, model, tc.want)
			}
		}
	}
}

/* *** FirmwareType *** */

func Test_firmware_type_string(t *testing.T) {
	cases := []struct {
		fw   FirmwareType
		want string
	}{
		{FIRMWARE_BIOS, "bios"},
		{FIRMWARE_UEFI, "efi"},
		{FirmwareType(99), ""},
	}
	for _, tc := range cases {
		got := tc.fw.String()
		if (got != tc.want) {
			t.Errorf("FirmwareType(%d).String() = %q, want %q", tc.fw, got, tc.want)
		}
	}
}

func Test_firmware_type_parse(t *testing.T) {
	cases := []struct {
		input   string
		want    FirmwareType
		wantErr bool
	}{
		{"bios", FIRMWARE_BIOS, false},
		{"efi", FIRMWARE_UEFI, false},
		{"uefi", 0, true},
		{"", 0, true},
	}
	for _, tc := range cases {
		var fw FirmwareType
		err := fw.Parse(tc.input)
		if (tc.wantErr) {
			if (err == nil) {
				t.Errorf("FirmwareType.Parse(%q): expected error", tc.input)
			}
		} else {
			if (err != nil) {
				t.Errorf("FirmwareType.Parse(%q): %v", tc.input, err)
			} else if (fw != tc.want) {
				t.Errorf("FirmwareType.Parse(%q) = %d, want %d", tc.input, fw, tc.want)
			}
		}
	}
}

func Test_firmware_type_machine(t *testing.T) {
	cases := []struct {
		fw   FirmwareType
		want string
	}{
		{FIRMWARE_BIOS, "pc"},
		{FIRMWARE_UEFI, "q35"},
		{FirmwareType(99), ""},
	}
	for _, tc := range cases {
		got := tc.fw.Machine()
		if (got != tc.want) {
			t.Errorf("FirmwareType(%d).Machine() = %q, want %q", tc.fw, got, tc.want)
		}
	}
}

/* *** DiskBus *** */

func Test_disk_bus_string(t *testing.T) {
	cases := []struct {
		bus  DiskBus
		want string
	}{
		{BUS_VIRTIO_BLK, "virtio"},
		{BUS_SATA, "sata"},
		{BUS_VIRTIO_SCSI, "virtio-scsi"},
		{BUS_SCSI, "scsi"},
		{DiskBus(99), ""},
	}
	for _, tc := range cases {
		got := tc.bus.String()
		if (got != tc.want) {
			t.Errorf("DiskBus(%d).String() = %q, want %q", tc.bus, got, tc.want)
		}
	}
}

func Test_disk_bus_parse(t *testing.T) {
	cases := []struct {
		name      string
		ctrl_type string
		ctrl_model string
		want      DiskBus
		wantErr   bool
	}{
		{"virtio", "virtio", "", BUS_VIRTIO_BLK, false},
		{"sata", "sata", "", BUS_SATA, false},
		{"scsi_virtio_scsi", "scsi", "virtio-scsi", BUS_VIRTIO_SCSI, false},
		{"scsi_other", "scsi", "lsilogic", BUS_SCSI, false},
		{"unknown", "ide", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var bus DiskBus
			err := bus.Parse(tc.ctrl_type, tc.ctrl_model)
			if (tc.wantErr) {
				if (err == nil) {
					t.Error("expected error")
				}
			} else {
				if (err != nil) {
					t.Errorf("unexpected error: %v", err)
				} else if (bus != tc.want) {
					t.Errorf("got %d, want %d", bus, tc.want)
				}
			}
		})
	}
}

/* *** DiskManMode *** */

func Test_disk_man_mode_string(t *testing.T) {
	cases := []struct {
		mode DiskManMode
		want string
	}{
		{DISK_MAN_UNMANAGED, "U"},
		{DISK_MAN_MANAGED, "M"},
		{DiskManMode(99), ""},
	}
	for _, tc := range cases {
		got := tc.mode.String()
		if (got != tc.want) {
			t.Errorf("DiskManMode(%d).String() = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func Test_disk_man_mode_parse(t *testing.T) {
	cases := []struct {
		input   byte
		want    DiskManMode
		wantErr bool
	}{
		{'U', DISK_MAN_UNMANAGED, false},
		{'M', DISK_MAN_MANAGED, false},
		{'X', 0, true},
	}
	for _, tc := range cases {
		var mode DiskManMode
		err := mode.Parse(tc.input)
		if (tc.wantErr) {
			if (err == nil) {
				t.Errorf("DiskManMode.Parse(%c): expected error", tc.input)
			}
		} else {
			if (err != nil) {
				t.Errorf("DiskManMode.Parse(%c): %v", tc.input, err)
			} else if (mode != tc.want) {
				t.Errorf("DiskManMode.Parse(%c) = %d, want %d", tc.input, mode, tc.want)
			}
		}
	}
}

/* *** DiskProvMode *** */

func Test_disk_prov_mode_string(t *testing.T) {
	cases := []struct {
		mode DiskProvMode
		want string
	}{
		{DISK_PROV_NONE, "U"},
		{DISK_PROV_THIN, "t"},
		{DISK_PROV_THICK, "T"},
		{DiskProvMode(99), ""},
	}
	for _, tc := range cases {
		got := tc.mode.String()
		if (got != tc.want) {
			t.Errorf("DiskProvMode(%d).String() = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func Test_disk_prov_mode_parse(t *testing.T) {
	cases := []struct {
		input   byte
		want    DiskProvMode
		wantErr bool
	}{
		{'U', DISK_PROV_NONE, false},
		{'t', DISK_PROV_THIN, false},
		{'T', DISK_PROV_THICK, false},
		{'X', 0, true},
	}
	for _, tc := range cases {
		var mode DiskProvMode
		err := mode.Parse(tc.input)
		if (tc.wantErr) {
			if (err == nil) {
				t.Errorf("DiskProvMode.Parse(%c): expected error", tc.input)
			}
		} else {
			if (err != nil) {
				t.Errorf("DiskProvMode.Parse(%c): %v", tc.input, err)
			} else if (mode != tc.want) {
				t.Errorf("DiskProvMode.Parse(%c) = %d, want %d", tc.input, mode, tc.want)
			}
		}
	}
}

/* *** custom_isalnum / CustomField.IsAlnum *** */

func Test_custom_isalnum(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"ABC", true},
		{"abc", true},
		{"abc123", true},
		{"ABC_123", true},
		{"under_score", true},
		{"", true},
		{"has space", false},
		{"has-dash", false},
		{"has.dot", false},
		{"special!", false},
	}
	for _, tc := range cases {
		got := custom_isalnum(tc.input)
		if (got != tc.want) {
			t.Errorf("custom_isalnum(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func Test_custom_field_is_alnum(t *testing.T) {
	cases := []struct {
		name  string
		field CustomField
		want  bool
	}{
		{"both_valid", CustomField{Name: "CID", Value: "1217"}, true},
		{"name_invalid", CustomField{Name: "C-ID", Value: "1217"}, false},
		{"value_invalid", CustomField{Name: "CID", Value: "12.17"}, false},
		{"both_empty", CustomField{Name: "", Value: ""}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.field.IsAlnum()
			if (got != tc.want) {
				t.Errorf("CustomField{%q,%q}.IsAlnum() = %v, want %v", tc.field.Name, tc.field.Value, got, tc.want)
			}
		})
	}
}

/* *** Cstate *** */

func Test_cstate_string(t *testing.T) {
	cases := []struct {
		state Cstate
		want  string
	}{
		{CSTATE_INVALID, "invalid"},
		{CSTATE_ACTIVE, "active"},
		{CSTATE_LEFT, "left"},
		{CSTATE_FAILED, "failed"},
		{Cstate(99), ""},
	}
	for _, tc := range cases {
		got := tc.state.String()
		if (got != tc.want) {
			t.Errorf("Cstate(%d).String() = %q, want %q", tc.state, got, tc.want)
		}
	}
}

/* *** Vmrunstate *** */

func Test_vmrunstate_string(t *testing.T) {
	cases := []struct {
		state Vmrunstate
		want  string
	}{
		{RUNSTATE_NONE, "none"},
		{RUNSTATE_DELETED, "deleted"},
		{RUNSTATE_POWEROFF, "poweroff"},
		{RUNSTATE_STARTUP, "startup"},
		{RUNSTATE_RUNNING, "running"},
		{RUNSTATE_PAUSED, "paused"},
		{RUNSTATE_MIGRATING, "migrating"},
		{RUNSTATE_TERMINATING, "terminating"},
		{RUNSTATE_PMSUSPENDED, "pmsuspended"},
		{RUNSTATE_CRASHED, "crashed"},
		{Vmrunstate(99), ""},
	}
	for _, tc := range cases {
		got := tc.state.String()
		if (got != tc.want) {
			t.Errorf("Vmrunstate(%d).String() = %q, want %q", tc.state, got, tc.want)
		}
	}
}

/* *** MigrationState *** */

func Test_migration_state_string(t *testing.T) {
	cases := []struct {
		state MigrationState
		want  string
	}{
		{MIGRATION_NONE, "none"},
		{MIGRATION_SETUP, "setup"},
		{MIGRATION_CANCELLING, "cancelling"},
		{MIGRATION_CANCELLED, "cancelled"},
		{MIGRATION_ACTIVE, "active"},
		{MIGRATION_COMPLETED, "completed"},
		{MIGRATION_FAILED, "failed"},
		{MIGRATION_PRESWITCH, "pre-switchover"},
		{MIGRATION_DEVICE, "device"},
		{MIGRATION_WAIT_UNPLUG, "wait-unplug"},
		{MigrationState(99), ""},
	}
	for _, tc := range cases {
		got := tc.state.String()
		if (got != tc.want) {
			t.Errorf("MigrationState(%d).String() = %q, want %q", tc.state, got, tc.want)
		}
	}
}

func Test_migration_state_parse(t *testing.T) {
	cases := []struct {
		input   string
		want    MigrationState
		wantErr bool
	}{
		{"none", MIGRATION_NONE, false},
		{"setup", MIGRATION_SETUP, false},
		{"cancelling", MIGRATION_CANCELLING, false},
		{"cancelled", MIGRATION_CANCELLED, false},
		{"active", MIGRATION_ACTIVE, false},
		{"completed", MIGRATION_COMPLETED, false},
		{"failed", MIGRATION_FAILED, false},
		{"pre-switchover", MIGRATION_PRESWITCH, false},
		{"device", MIGRATION_DEVICE, false},
		{"wait-unplug", MIGRATION_WAIT_UNPLUG, false},
		{"bogus", 0, true},
	}
	for _, tc := range cases {
		var state MigrationState
		err := state.Parse(tc.input)
		if (tc.wantErr) {
			if (err == nil) {
				t.Errorf("MigrationState.Parse(%q): expected error", tc.input)
			}
		} else {
			if (err != nil) {
				t.Errorf("MigrationState.Parse(%q): %v", tc.input, err)
			} else if (state != tc.want) {
				t.Errorf("MigrationState.Parse(%q) = %d, want %d", tc.input, state, tc.want)
			}
		}
	}
}

/* *** OperationState *** */

func Test_operation_state_string(t *testing.T) {
	cases := []struct {
		state OperationState
		want  string
	}{
		{OPERATION_STARTED, "started"},
		{OPERATION_FAILED, "failed"},
		{OPERATION_COMPLETED, "completed"},
		{OperationState(99), ""},
	}
	for _, tc := range cases {
		got := tc.state.String()
		if (got != tc.want) {
			t.Errorf("OperationState(%d).String() = %q, want %q", tc.state, got, tc.want)
		}
	}
}

func Test_operation_state_parse(t *testing.T) {
	cases := []struct {
		input   string
		want    OperationState
		wantErr bool
	}{
		{"started", OPERATION_STARTED, false},
		{"failed", OPERATION_FAILED, false},
		{"completed", OPERATION_COMPLETED, false},
		{"bogus", 0, true},
	}
	for _, tc := range cases {
		var state OperationState
		err := state.Parse(tc.input)
		if (tc.wantErr) {
			if (err == nil) {
				t.Errorf("OperationState.Parse(%q): expected error", tc.input)
			}
		} else {
			if (err != nil) {
				t.Errorf("OperationState.Parse(%q): %v", tc.input, err)
			} else if (state != tc.want) {
				t.Errorf("OperationState.Parse(%q) = %d, want %d", tc.input, state, tc.want)
			}
		}
	}
}

/* *** Operation *** */

func Test_operation_string(t *testing.T) {
	cases := []struct {
		op   Operation
		want string
	}{
		{OpHostGet, "HostGet"},
		{OpVmBoot, "VmBoot"},
		{OpVmCreate, "VmCreate"},
		{OpVmMigrate, "VmMigrate"},
		{OpVmShutdown, "VmShutdown"},
		{Operation(99), ""},
	}
	for _, tc := range cases {
		got := tc.op.String()
		if (got != tc.want) {
			t.Errorf("Operation(%d).String() = %q, want %q", tc.op, got, tc.want)
		}
	}
}

func Test_operation_parse(t *testing.T) {
	cases := []struct {
		input   string
		want    Operation
		wantErr bool
	}{
		{"HostGet", OpHostGet, false},
		{"VmBoot", OpVmBoot, false},
		{"VmCreate", OpVmCreate, false},
		{"VmDelete", OpVmDelete, false},
		{"VmMigrate", OpVmMigrate, false},
		{"VmShutdown", OpVmShutdown, false},
		{"VmUpdate", OpVmUpdate, false},
		{"bogus", 0, true},
		{"", 0, true},
	}
	for _, tc := range cases {
		var op Operation
		err := op.Parse(tc.input)
		if (tc.wantErr) {
			if (err == nil) {
				t.Errorf("Operation.Parse(%q): expected error", tc.input)
			}
		} else {
			if (err != nil) {
				t.Errorf("Operation.Parse(%q): %v", tc.input, err)
			} else if (op != tc.want) {
				t.Errorf("Operation.Parse(%q) = %d, want %d", tc.input, op, tc.want)
			}
		}
	}
}

/* *** String/Parse round-trip consistency *** */

func Test_migration_state_roundtrip(t *testing.T) {
	states := []MigrationState{
		MIGRATION_NONE, MIGRATION_SETUP, MIGRATION_CANCELLING, MIGRATION_CANCELLED,
		MIGRATION_ACTIVE, MIGRATION_PRESWITCH, MIGRATION_DEVICE, MIGRATION_WAIT_UNPLUG,
		MIGRATION_COMPLETED, MIGRATION_FAILED,
	}
	for _, s := range states {
		str := s.String()
		if (str == "") {
			t.Errorf("MigrationState(%d).String() returned empty", s)
			continue
		}
		var parsed MigrationState
		err := parsed.Parse(str)
		if (err != nil) {
			t.Errorf("MigrationState round-trip failed for %d -> %q: %v", s, str, err)
		} else if (parsed != s) {
			t.Errorf("MigrationState round-trip: %d -> %q -> %d", s, str, parsed)
		}
	}
}

func Test_operation_roundtrip(t *testing.T) {
	for op, str := range OperationToString {
		var parsed Operation
		err := parsed.Parse(str)
		if (err != nil) {
			t.Errorf("Operation round-trip failed for %d -> %q: %v", op, str, err)
		} else if (parsed != op) {
			t.Errorf("Operation round-trip: %d -> %q -> %d", op, str, parsed)
		}
	}
}
