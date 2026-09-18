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
package metadata

import (
	"testing"

	"suse.com/virtx/pkg/model"
)

/* *** Vm.To_xml / Vm.From_xml *** */

func Test_vm_to_xml_from_xml_roundtrip(t *testing.T) {
	cases := []struct {
		name string
		fields []openapi.CustomField
		expect int
	}{
		{
			"no_fields",
			[]openapi.CustomField{},
			0,
		},
		{
			"one_field",
			[]openapi.CustomField{
				{Name: "CID", Value: "1217"},
			},
			1,
		},
		{
			"two_fields",
			[]openapi.CustomField{
				{Name: "CID", Value: "1217"},
				{Name: "ENV", Value: "prod"},
			},
			2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var vm Vm
			xmlstr, err := vm.To_xml(tc.fields)
			if (err != nil) {
				t.Fatalf("To_xml: %v", err)
			}
			if (xmlstr == "") {
				t.Fatal("To_xml: returned empty string")
			}

			var vm2 Vm
			var parsed []openapi.CustomField
			err = vm2.From_xml(xmlstr, &parsed)
			if (err != nil) {
				t.Fatalf("From_xml: %v", err)
			}
			if (len(parsed) != tc.expect) {
				t.Fatalf("From_xml: expected %d fields, got %d", tc.expect, len(parsed))
			}
			for i := 0; i < tc.expect; i++ {
				if (parsed[i].Name != tc.fields[i].Name || parsed[i].Value != tc.fields[i].Value) {
					t.Errorf("field %d: expected {%q,%q}, got {%q,%q}",
						i, tc.fields[i].Name, tc.fields[i].Value, parsed[i].Name, parsed[i].Value)
				}
			}
		})
	}
}

func Test_vm_to_xml_skips_empty_name(t *testing.T) {
	var vm Vm
	fields := []openapi.CustomField{
		{Name: "", Value: "ignored"},
		{Name: "CID", Value: "1217"},
	}
	xmlstr, err := vm.To_xml(fields)
	if (err != nil) {
		t.Fatalf("To_xml: %v", err)
	}

	var vm2 Vm
	var parsed []openapi.CustomField
	err = vm2.From_xml(xmlstr, &parsed)
	if (err != nil) {
		t.Fatalf("From_xml: %v", err)
	}
	if (len(parsed) != 1) {
		t.Fatalf("expected 1 field (empty name skipped), got %d", len(parsed))
	}
	if (parsed[0].Name != "CID" || parsed[0].Value != "1217") {
		t.Errorf("expected {CID,1217}, got {%q,%q}", parsed[0].Name, parsed[0].Value)
	}
}

/*
 * From_xml must parse both the namespaced element (as embedded in the full domain XML)
 * and the namespace-stripped element that libvirt GetMetadata returns. This is the whole
 * reason Vm uses XMLName xml:"" instead of requiring the "virtx-vm" namespace.
 */
func Test_vm_from_xml_namespaces(t *testing.T) {
	cases := []struct {
		name string
		xmlstr string
	}{
		{
			"namespaced", /* as found in the full domain XML metadata */
			`<data-vm xmlns="virtx-vm"><field name="CID">1217</field></data-vm>`,
		},
		{
			"stripped", /* as returned by libvirt GetMetadata (namespace lost) */
			`<data-vm><field name="CID">1217</field></data-vm>`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var vm Vm
			var parsed []openapi.CustomField
			err := vm.From_xml(tc.xmlstr, &parsed)
			if (err != nil) {
				t.Fatalf("From_xml: %v", err)
			}
			if (len(parsed) != 1) {
				t.Fatalf("expected 1 field, got %d", len(parsed))
			}
			if (parsed[0].Name != "CID" || parsed[0].Value != "1217") {
				t.Errorf("expected {CID,1217}, got {%q,%q}", parsed[0].Name, parsed[0].Value)
			}
		})
	}
}

