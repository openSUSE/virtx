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
	"encoding/xml"

	"suse.com/virtx/pkg/model"
)

type Vm struct {
	XMLName xml.Name `xml:""`

	Source string `xml:"source,omitempty"` /* osdisk golden image it was cloned from */
	Fields []Field `xml:"field"`
}

func (vm *Vm) To_xml(source string, fields []openapi.CustomField) (string, error) {
	var (
		err error
		xmlstr []byte
	)
	*vm = Vm{
		XMLName: xml.Name{ Space: "virtx-vm", Local: "data-vm" },
		Source: source,
		Fields: []Field{},
	}
	for _, custom := range fields {
		if (custom.Name == "") {
			continue
		}
		vm.Fields = append(vm.Fields, Field{ Name: custom.Name, Value: custom.Value })
	}
	xmlstr, err = xml.Marshal(vm)
	if (err != nil) {
		return "", err
	}
	return string(xmlstr), nil
}

func (vm *Vm) From_xml(xmlstr string, source *string, fields *[]openapi.CustomField) error {
	var err error
	err = xml.Unmarshal([]byte(xmlstr), vm)
	if (err != nil) {
		return err
	}
	*source = vm.Source
	for _, field := range vm.Fields {
		*fields = append(*fields, openapi.CustomField{
			Name: field.Name,
			Value: field.Value,
		})
	}
	return nil
}

type Field struct {
	Name string `xml:"name,attr"`
	Value string `xml:",chardata"`
}

