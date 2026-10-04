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
package serftags

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
)

/*
 * Struct <-> serf tags. One tag per string or signed integer field, keyed by the field name.
 * Nested structs are flattened, so field names must be unique across them.
 * All the tags of a serf member must fit in 512 bytes: keep field names short.
 */
func Encode(data any) (map[string]string, error) {
	var (
		v reflect.Value = reflect.Indirect(reflect.ValueOf(data))
		tags map[string]string = make(map[string]string)
		err error
	)
	if (v.Kind() != reflect.Struct) {
		return nil, errors.New("Encode: must pass a struct or a pointer to a struct")
	}
	err = encode_struct(v, tags)
	if (err != nil) {
		return nil, err
	}
	return tags, nil
}

/* Fields without a tag are left unchanged. Tags without a field are ignored. */
func Decode(tags map[string]string, data any) error {
	var v reflect.Value = reflect.ValueOf(data)
	if (v.Kind() != reflect.Ptr || v.IsNil() || v.Elem().Kind() != reflect.Struct) {
		return errors.New("Decode: must pass a non-nil pointer to a struct")
	}
	return decode_struct(tags, v.Elem())
}

func encode_struct(v reflect.Value, tags map[string]string) error {
	var (
		i int
		field reflect.StructField
		val reflect.Value
		present bool
		err error
	)
	for i = 0; i < v.NumField(); i++ {
		field = v.Type().Field(i)
		if (!field.IsExported()) {
			continue
		}
		val = v.Field(i)
		if (val.Kind() == reflect.Struct) {
			err = encode_struct(val, tags)
			if (err != nil) {
				return err
			}
			continue
		}
		_, present = tags[field.Name]
		if (present) {
			return fmt.Errorf("Encode: duplicate field name %s", field.Name)
		}
		switch (val.Kind()) {
		case reflect.String:
			tags[field.Name] = val.String()
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			tags[field.Name] = strconv.FormatInt(val.Int(), 10)
		default:
			return fmt.Errorf("Encode: unsupported type %s for field %s", val.Kind(), field.Name)
		}
	}
	return nil
}

func decode_struct(tags map[string]string, v reflect.Value) error {
	var (
		i int
		field reflect.StructField
		val reflect.Value
		tag string
		n int64
		present bool
		err error
	)
	for i = 0; i < v.NumField(); i++ {
		field = v.Type().Field(i)
		if (!field.IsExported()) {
			continue
		}
		val = v.Field(i)
		if (val.Kind() == reflect.Struct) {
			err = decode_struct(tags, val)
			if (err != nil) {
				return err
			}
			continue
		}
		tag, present = tags[field.Name]
		if (!present) {
			continue
		}
		switch (val.Kind()) {
		case reflect.String:
			val.SetString(tag)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			n, err = strconv.ParseInt(tag, 10, val.Type().Bits())
			if (err != nil) {
				return fmt.Errorf("Decode: field %s: %w", field.Name, err)
			}
			val.SetInt(n)
		default:
			return fmt.Errorf("Decode: unsupported type %s for field %s", val.Kind(), field.Name)
		}
	}
	return nil
}
