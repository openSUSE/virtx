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
package metadata

import (
	"encoding/xml"

	"suse.com/virtx/pkg/model"
)

type Vm struct {
	XMLName xml.Name `xml:"virtx-vm data-vm"`
	XMLNS string `xml:"xmlns:virtx-vm,attr"`

	Fields []Field `xml:"field"`
}

func (vm *Vm) To_xml(fields []openapi.CustomField) (string, error) {
	var (
		err error
		xmlstr []byte
	)
	*vm = Vm{
		XMLName: xml.Name{ Space: "virtx-vm", Local: "data-vm" },
		XMLNS: "virtx-vm",
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

func (vm *Vm) From_xml(xmlstr string, fields *[]openapi.CustomField) error {
	var err error
	err = xml.Unmarshal([]byte(xmlstr), vm)
	if (err != nil) {
		return err
	}
	if (err != nil) {
		return err
	}
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

/*
 * operation is read and written separately from the data above.
 * Due to a design choice of libvirt, we need to assign an entire separate namespace to it,
 * otherwise setting or getting operation data will delete everything else in the namespace.
 * This is why you see "virtx-vm" vs "virtx-op".
 *
 * Also you may notice that there is no default namespace used here in contrast to Vm,
 * and that is because GetMetadata loses the namespace along the way.
 * This is in my view another libvirt bug.
 */
type Operation struct {
	XMLName xml.Name `xml:""`
	Op string `xml:"op"`
	Ts int64 `xml:"ts"`
	Status string `xml:"status"`
	Msg string `xml:"msg"`
}

func (op *Operation) To_xml(o openapi.Operation, state openapi.OperationState, msg string, ts int64) (string, error) {
	var (
		err error
		xmlstr []byte
	)
	*op = Operation{
		XMLName: xml.Name{ Space: "virtx-op-" + o.String(), Local: "data-op" },
		Ts: ts,
		Op: o.String(),
		Status: state.String(),
		Msg: msg,
	}
	xmlstr, err = xml.Marshal(op)
	if (err != nil) {
		return "", err
	}
	return string(xmlstr), nil
}

func (op *Operation) From_xml(xmlstr string, o *openapi.Operation, state *openapi.OperationState, msg *string, ts *int64) error {
	var err error
	err = xml.Unmarshal([]byte(xmlstr), op)
	if (err != nil) {
		return err
	}
	err = o.Parse(op.Op)
	if (err != nil) {
		return err
	}
	err = state.Parse(op.Status)
	if (err != nil) {
		return err
	}
	*msg = op.Msg
	*ts = op.Ts
	return nil
}
