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
package inventory

import (
	"testing"

	"suse.com/virtx/pkg/model"
)

/*
 * Each test calls reset() first, so tests are fully independent.
 * reset() reaches into the package-level singleton directly; this is safe
 * because we are in the same package and tests run sequentially by default.
 */
func reset() {
	inventory.m.Lock()
	defer inventory.m.Unlock()
	inventory.hosts = make(HostsInventory)
	inventory.vms = make(VmsInventory)
}

/* --- helpers --- */

func add_host(uuid, name string, cstate openapi.Cstate, mem int32, ts int64) {
	Update_host(&HostInfo{
		Uuid: uuid,
		HostListFields: openapi.HostListFields{
			Name:            name,
			Cstate:          cstate,
			Memoryavailable: mem,
			Ts:              ts,
		},
	})
}

func add_vm(t *testing.T, uuid, host, name string, state openapi.Vmrunstate, ts int64) {
	t.Helper()
	if err := Update_vm(&VmInfo{
		VmEvent: VmEvent{Uuid: uuid, Host: host, Runstate: state, Ts: ts},
		Name: name,
	}); err != nil {
		t.Fatalf("add_vm(%q): %v", uuid, err)
	}
}

func vm_event(uuid, host string, state openapi.Vmrunstate, ts int64) *VmEvent {
	return &VmEvent{Uuid: uuid, Host: host, Runstate: state, Ts: ts}
}

/* assert helpers — all call t.Helper() so failures point to the call site */

func assert_host_name(t *testing.T, uuid, want string) {
	t.Helper()
	info, err := Get_hostinfo(uuid)
	if err != nil {
		t.Fatalf("Get_hostinfo(%q): %v", uuid, err)
	}
	if info.Name != want {
		t.Errorf("host %q: Name = %q, want %q", uuid, info.Name, want)
	}
}

func assert_host_mem(t *testing.T, uuid string, want int32) {
	t.Helper()
	info, err := Get_hostinfo(uuid)
	if err != nil {
		t.Fatalf("Get_hostinfo(%q): %v", uuid, err)
	}
	if info.Memoryavailable != want {
		t.Errorf("host %q: Memoryavailable = %d, want %d", uuid, info.Memoryavailable, want)
	}
}

func assert_host_cstate(t *testing.T, uuid string, want openapi.Cstate) {
	t.Helper()
	info, err := Get_hostinfo(uuid)
	if err != nil {
		t.Fatalf("Get_hostinfo(%q): %v", uuid, err)
	}
	if info.Cstate != want {
		t.Errorf("host %q: Cstate = %d, want %d", uuid, info.Cstate, want)
	}
}

func assert_host_not_found(t *testing.T, uuid string) {
	t.Helper()
	if _, err := Get_hostinfo(uuid); err == nil {
		t.Errorf("Get_hostinfo(%q): expected error, got nil", uuid)
	}
}

func assert_vm_runstate(t *testing.T, uuid string, want openapi.Vmrunstate) {
	t.Helper()
	vm, err := Get_vminfo(uuid)
	if err != nil {
		t.Fatalf("Get_vminfo(%q): %v", uuid, err)
	}
	if vm.Runstate != want {
		t.Errorf("vm %q: Runstate = %d, want %d", uuid, vm.Runstate, want)
	}
}

func assert_vm_host(t *testing.T, uuid, want string) {
	t.Helper()
	vm, err := Get_vminfo(uuid)
	if err != nil {
		t.Fatalf("Get_vminfo(%q): %v", uuid, err)
	}
	if vm.Host != want {
		t.Errorf("vm %q: Host = %q, want %q", uuid, vm.Host, want)
	}
}

func assert_vm_not_found(t *testing.T, uuid string) {
	t.Helper()
	if _, err := Get_vminfo(uuid); err == nil {
		t.Errorf("Get_vminfo(%q): expected error, got nil", uuid)
	}
}

func assert_host_has_vm(t *testing.T, host_uuid, vm_uuid string) {
	t.Helper()
	data, err := Get_hostdata(host_uuid)
	if err != nil {
		t.Fatalf("Get_hostdata(%q): %v", host_uuid, err)
	}
	if _, ok := data.Vms[vm_uuid]; !ok {
		t.Errorf("host %q: VM %q not in Vms map", host_uuid, vm_uuid)
	}
}

func assert_host_lacks_vm(t *testing.T, host_uuid, vm_uuid string) {
	t.Helper()
	data, err := Get_hostdata(host_uuid)
	if err != nil {
		t.Fatalf("Get_hostdata(%q): %v", host_uuid, err)
	}
	if _, ok := data.Vms[vm_uuid]; ok {
		t.Errorf("host %q: VM %q should not be in Vms map", host_uuid, vm_uuid)
	}
}

func assert_search_hosts_contains(t *testing.T, list openapi.HostList, uuid string) {
	t.Helper()
	for _, item := range list.Items {
		if item.Uuid == uuid {
			return
		}
	}
	t.Errorf("host %q not found in search results", uuid)
}

func assert_search_hosts_lacks(t *testing.T, list openapi.HostList, uuid string) {
	t.Helper()
	for _, item := range list.Items {
		if item.Uuid == uuid {
			t.Errorf("host %q should not appear in search results", uuid)
			return
		}
	}
}

func assert_search_vms_contains(t *testing.T, list openapi.VmList, uuid string) {
	t.Helper()
	for _, item := range list.Items {
		if item.Uuid == uuid {
			return
		}
	}
	t.Errorf("vm %q not found in search results", uuid)
}

func assert_search_vms_lacks(t *testing.T, list openapi.VmList, uuid string) {
	t.Helper()
	for _, item := range list.Items {
		if item.Uuid == uuid {
			t.Errorf("vm %q should not appear in search results", uuid)
			return
		}
	}
}

/* =============================== HOST TESTS =============================== */

func Test_host_add_and_get(t *testing.T) {
	reset()
	add_host("h1", "node1", openapi.CSTATE_ACTIVE, 8192, 100)

	info, err := Get_hostinfo("h1")
	if err != nil {
		t.Fatalf("Get_hostinfo: %v", err)
	}
	if info.Uuid != "h1" { t.Errorf("Uuid: got %q", info.Uuid) }
	if info.Name != "node1" { t.Errorf("Name: got %q", info.Name) }
	if info.Cstate != openapi.CSTATE_ACTIVE { t.Errorf("Cstate: got %d", info.Cstate) }
	if info.Memoryavailable != 8192 { t.Errorf("Memoryavailable: got %d", info.Memoryavailable) }

	data, err := Get_hostdata("h1")
	if err != nil {
		t.Fatalf("Get_hostdata: %v", err)
	}
	if data.Vms == nil {
		t.Error("Hostdata.Vms map should be non-nil after first add")
	}
}

func Test_host_get_not_found(t *testing.T) {
	reset()
	assert_host_not_found(t, "no-such-host")
	if _, err := Get_hostdata("no-such-host"); err == nil {
		t.Error("Get_hostdata: expected error for unknown host")
	}
}

func Test_host_stale_update_rejected(t *testing.T) {
	reset()
	add_host("h1", "original", openapi.CSTATE_ACTIVE, 1024, 200)
	add_host("h1", "should-not-replace", openapi.CSTATE_ACTIVE, 2048, 100) /* older Ts */

	assert_host_name(t, "h1", "original")
	assert_host_mem(t, "h1", 1024)
}

func Test_host_newer_update_replaces(t *testing.T) {
	reset()
	add_host("h1", "old", openapi.CSTATE_ACTIVE, 1024, 100)
	add_host("h1", "new", openapi.CSTATE_ACTIVE, 4096, 200) /* newer Ts */

	assert_host_name(t, "h1", "new")
	assert_host_mem(t, "h1", 4096)
}

func Test_host_set_state(t *testing.T) {
	reset()
	add_host("h1", "node1", openapi.CSTATE_ACTIVE, 512, 100)

	if err := Set_host_state("h1", openapi.CSTATE_FAILED); err != nil {
		t.Fatalf("Set_host_state: %v", err)
	}
	assert_host_cstate(t, "h1", openapi.CSTATE_FAILED)
}

func Test_host_set_state_not_found(t *testing.T) {
	reset()
	if err := Set_host_state("no-such-host", openapi.CSTATE_ACTIVE); err == nil {
		t.Error("expected error for unknown host")
	}
}

/* ================================ VM TESTS ================================ */

func Test_vm_add_and_get(t *testing.T) {
	reset()
	add_host("h1", "node1", openapi.CSTATE_ACTIVE, 4096, 100)
	add_vm(t, "vm1", "h1", "testvm", openapi.RUNSTATE_RUNNING, 100)

	vm, err := Get_vminfo("vm1")
	if err != nil {
		t.Fatalf("Get_vminfo: %v", err)
	}
	if vm.Uuid != "vm1" { t.Errorf("Uuid: got %q", vm.Uuid) }
	if vm.Name != "testvm" { t.Errorf("Name: got %q", vm.Name) }
	if vm.Host != "h1" { t.Errorf("Host: got %q", vm.Host) }
	if vm.Runstate != openapi.RUNSTATE_RUNNING { t.Errorf("Runstate: got %d", vm.Runstate) }

	assert_host_has_vm(t, "h1", "vm1")
}

func Test_vm_get_not_found(t *testing.T) {
	reset()
	assert_vm_not_found(t, "no-such-vm")
}

func Test_vm_stale_update_rejected(t *testing.T) {
	reset()
	add_host("h1", "node1", openapi.CSTATE_ACTIVE, 512, 100)
	add_vm(t, "vm1", "h1", "original", openapi.RUNSTATE_RUNNING, 200)
	add_vm(t, "vm1", "h1", "should-not-replace", openapi.RUNSTATE_POWEROFF, 100) /* older Ts */

	vm, _ := Get_vminfo("vm1")
	if vm.Name != "original" {
		t.Errorf("stale update not rejected: Name = %q", vm.Name)
	}
	assert_vm_runstate(t, "vm1", openapi.RUNSTATE_RUNNING)
}

func Test_vm_state_update_runstate(t *testing.T) {
	reset()
	add_host("h1", "node1", openapi.CSTATE_ACTIVE, 512, 100)
	add_vm(t, "vm1", "h1", "testvm", openapi.RUNSTATE_RUNNING, 100)

	if err := Update_vm_state(vm_event("vm1", "h1", openapi.RUNSTATE_PAUSED, 200)); err != nil {
		t.Fatalf("Update_vm_state: %v", err)
	}
	assert_vm_runstate(t, "vm1", openapi.RUNSTATE_PAUSED)
}

func Test_vm_state_update_deleted(t *testing.T) {
	reset()
	add_host("h1", "node1", openapi.CSTATE_ACTIVE, 512, 100)
	add_vm(t, "vm1", "h1", "testvm", openapi.RUNSTATE_POWEROFF, 100)

	if err := Update_vm_state(vm_event("vm1", "h1", openapi.RUNSTATE_DELETED, 200)); err != nil {
		t.Fatalf("Update_vm_state DELETED: %v", err)
	}
	assert_vm_not_found(t, "vm1")
	assert_host_lacks_vm(t, "h1", "vm1")
}

func Test_vm_state_update_migration(t *testing.T) {
	reset()
	add_host("h1", "src", openapi.CSTATE_ACTIVE, 512, 100)
	add_host("h2", "dst", openapi.CSTATE_ACTIVE, 512, 100)
	add_vm(t, "vm1", "h1", "migvm", openapi.RUNSTATE_RUNNING, 100)

	if err := Update_vm_state(vm_event("vm1", "h2", openapi.RUNSTATE_RUNNING, 200)); err != nil {
		t.Fatalf("Update_vm_state (migration): %v", err)
	}
	assert_vm_host(t, "vm1", "h2")
	assert_host_lacks_vm(t, "h1", "vm1")
	assert_host_has_vm(t, "h2", "vm1")
}

func Test_vm_state_update_unknown_vm(t *testing.T) {
	reset()
	if err := Update_vm_state(vm_event("no-such-vm", "h1", openapi.RUNSTATE_RUNNING, 100)); err == nil {
		t.Error("expected error for unknown VM UUID")
	}
}

/* ============================== SEARCH TESTS ============================== */

func Test_search_hosts_no_filter(t *testing.T) {
	reset()
	add_host("h1", "alpha", openapi.CSTATE_ACTIVE, 512, 100)
	add_host("h2", "beta",  openapi.CSTATE_ACTIVE, 512, 100)

	list := Search_hosts(openapi.HostListFields{})
	assert_search_hosts_contains(t, list, "h1")
	assert_search_hosts_contains(t, list, "h2")
	if len(list.Items) != 2 {
		t.Errorf("no-filter: expected 2 results, got %d", len(list.Items))
	}
}

func Test_search_hosts_by_name_substring(t *testing.T) {
	reset()
	add_host("h1", "prod-node-01", openapi.CSTATE_ACTIVE, 512, 100)
	add_host("h2", "dev-node-01",  openapi.CSTATE_ACTIVE, 512, 100)

	list := Search_hosts(openapi.HostListFields{Name: "prod"})
	assert_search_hosts_contains(t, list, "h1")
	assert_search_hosts_lacks(t, list, "h2")
}

func Test_search_hosts_by_cstate(t *testing.T) {
	reset()
	add_host("h1", "active-node", openapi.CSTATE_ACTIVE, 512, 100)
	add_host("h2", "failed-node", openapi.CSTATE_FAILED, 512, 100)

	list := Search_hosts(openapi.HostListFields{Cstate: openapi.CSTATE_FAILED})
	assert_search_hosts_contains(t, list, "h2")
	assert_search_hosts_lacks(t, list, "h1")
}

func Test_search_hosts_by_memory(t *testing.T) {
	reset()
	add_host("h1", "big",   openapi.CSTATE_ACTIVE, 8192, 100)
	add_host("h2", "small", openapi.CSTATE_ACTIVE, 256,  100)

	list := Search_hosts(openapi.HostListFields{Memoryavailable: 4096})
	assert_search_hosts_contains(t, list, "h1")
	assert_search_hosts_lacks(t, list, "h2")
}

func Test_search_vms_no_filter(t *testing.T) {
	reset()
	add_host("h1", "node1", openapi.CSTATE_ACTIVE, 512, 100)
	add_vm(t, "vm1", "h1", "alpha", openapi.RUNSTATE_RUNNING,  100)
	add_vm(t, "vm2", "h1", "beta",  openapi.RUNSTATE_POWEROFF, 100)

	list := Search_vms(openapi.VmListFields{})
	assert_search_vms_contains(t, list, "vm1")
	assert_search_vms_contains(t, list, "vm2")
	if len(list.Items) != 2 {
		t.Errorf("no-filter: expected 2 results, got %d", len(list.Items))
	}
}

func Test_search_vms_by_name(t *testing.T) {
	reset()
	add_host("h1", "node1", openapi.CSTATE_ACTIVE, 512, 100)
	add_vm(t, "vm1", "h1", "prod-db",  openapi.RUNSTATE_RUNNING, 100)
	add_vm(t, "vm2", "h1", "dev-web",  openapi.RUNSTATE_RUNNING, 100)

	list := Search_vms(openapi.VmListFields{Name: "prod"})
	assert_search_vms_contains(t, list, "vm1")
	assert_search_vms_lacks(t, list, "vm2")
}

func Test_search_vms_by_host(t *testing.T) {
	reset()
	add_host("h1", "src", openapi.CSTATE_ACTIVE, 512, 100)
	add_host("h2", "dst", openapi.CSTATE_ACTIVE, 512, 100)
	add_vm(t, "vm1", "h1", "vm-on-h1", openapi.RUNSTATE_RUNNING, 100)
	add_vm(t, "vm2", "h2", "vm-on-h2", openapi.RUNSTATE_RUNNING, 100)

	list := Search_vms(openapi.VmListFields{Host: "h1"})
	assert_search_vms_contains(t, list, "vm1")
	assert_search_vms_lacks(t, list, "vm2")
}

func Test_search_vms_by_runstate(t *testing.T) {
	reset()
	add_host("h1", "node1", openapi.CSTATE_ACTIVE, 512, 100)
	add_vm(t, "vm1", "h1", "running-vm",  openapi.RUNSTATE_RUNNING,  100)
	add_vm(t, "vm2", "h1", "poweroff-vm", openapi.RUNSTATE_POWEROFF, 100)

	list := Search_vms(openapi.VmListFields{Runstate: openapi.RUNSTATE_RUNNING})
	assert_search_vms_contains(t, list, "vm1")
	assert_search_vms_lacks(t, list, "vm2")
}

func Test_search_vms_by_custom_field(t *testing.T) {
	reset()
	add_host("h1", "node1", openapi.CSTATE_ACTIVE, 512, 100)

	vm_match := &VmInfo{
		VmEvent: VmEvent{Uuid: "vm1", Host: "h1", Runstate: openapi.RUNSTATE_RUNNING, Ts: 100},
		Name:   "vm-with-custom",
		Custom: []openapi.CustomField{{Name: "ENV", Value: "prod"}},
	}
	vm_nomatch := &VmInfo{
		VmEvent: VmEvent{Uuid: "vm2", Host: "h1", Runstate: openapi.RUNSTATE_RUNNING, Ts: 100},
		Name:   "vm-no-custom",
	}
	if err := Update_vm(vm_match);   err != nil { t.Fatalf("Update_vm vm1: %v", err) }
	if err := Update_vm(vm_nomatch); err != nil { t.Fatalf("Update_vm vm2: %v", err) }

	list := Search_vms(openapi.VmListFields{Custom: openapi.CustomField{Name: "ENV", Value: "prod"}})
	assert_search_vms_contains(t, list, "vm1")
	assert_search_vms_lacks(t, list, "vm2")
}

func Test_search_vms_ts_filter(t *testing.T) {
	reset()
	add_host("h1", "node1", openapi.CSTATE_ACTIVE, 512, 100)
	add_vm(t, "vm1", "h1", "old-vm", openapi.RUNSTATE_RUNNING, 100)
	add_vm(t, "vm2", "h1", "new-vm", openapi.RUNSTATE_RUNNING, 500)

	/* Ts filter returns only VMs with Ts <= filter.Ts */
	list := Search_vms(openapi.VmListFields{Ts: 200})
	assert_search_vms_contains(t, list, "vm1") /* Ts=100 <= 200 */
	assert_search_vms_lacks(t, list, "vm2")    /* Ts=500 > 200 */
}
