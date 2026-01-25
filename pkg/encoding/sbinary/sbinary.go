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
package sbinary

import (
	"encoding/binary"
	"fmt"
	"errors"
	"reflect"
)

func Encode(buf []byte, order binary.ByteOrder, data any) (int, error) {
	var (
		err error
		size int
		v reflect.Value = reflect.ValueOf(data)
	)
	if (v.Kind() == reflect.Ptr && v.IsNil()) {
		return 0, errors.New("Encode: must pass non-Nil data to encode")
	}
	size, err = encode_value(buf, order, v)
	if (err != nil) {
		return size, err
	}
	return size, nil
}

func encode_struct(buf []byte, order binary.ByteOrder, val reflect.Value) (int, error) {
	var (
		i, n int
		field reflect.Value
		numfield int = val.NumField()
		offset int = 0
		err error
	)
	for i = 0; (i < numfield); i++ {
		field = val.Field(i)
		if (!field.CanInterface()) {
			/*
			 * if the field is not exported (capital letter), then attempting to introspect it
			 * will fail with panic at runtime. Ignore those fields instead.
			 */
			continue
		}
		n, err = encode_value(buf[offset:], order, field)
		if (err != nil) {
			return offset, err
		}
		offset += n
	}
	return offset, nil
}

func encode_bytes(buf []byte, order binary.ByteOrder, data []byte) (int, error) {
	var (
		n int = len(data)
		offset int = 0
	)
	if (len(buf) < 2 + n) {
		return offset, errors.New("Bytes: buffer too small for length indicator")
	}
	order.PutUint16(buf, uint16(n))
	offset += 2
	copy(buf[offset:], data)
	offset += n
	return offset, nil
}

func encode_array(buf []byte, order binary.ByteOrder, val reflect.Value) (int, error) {
	var (
		i, n int = 0, val.Len()
		off, offset int
		err error
	)
	if (len(buf) < 2) {
		return offset, errors.New("Array: buffer too small for length indicator")
	}
	order.PutUint16(buf, uint16(n))
	offset += 2
	for i = 0; i < n; i++ {
		off, err = encode_value(buf[offset:], order, val.Index(i))
		if (err != nil) {
			return offset, err
		}
		offset += off
	}
	return offset, nil
}

func encode_value(buf []byte, order binary.ByteOrder, val reflect.Value) (int, error) {
	if (val.Kind() == reflect.Ptr) {
		if (val.IsNil()) {
			return 0, errors.New("Pointer: nil pointers are not supported")
		}
		val = val.Elem()
	}
	switch (val.Kind()) {
	case reflect.Ptr:
		return 0, errors.New("Pointer: Ptr to Ptr is not supported")
	case reflect.Struct:
		return encode_struct(buf, order, val);
	case reflect.String:
		return encode_bytes(buf, order, []byte(val.String()));
	case reflect.Slice:
		fallthrough
	case reflect.Array:
		if (val.Type().Elem().Kind() == reflect.Uint8) { /* byte slice */
			return encode_bytes(buf, order, []byte(val.Bytes()))
		}
		return encode_array(buf, order, val);
	case reflect.Uint8:
		if (len(buf) < 1) {
			return 0, errors.New("Uint8: buffer too small")
		}
		buf[0] = byte(val.Uint())
		return 1, nil
	case reflect.Int8:
		if (len(buf) < 1) {
			return 0, errors.New("Int8: buffer too small")
		}
		buf[0] = byte(val.Int())
		return 1, nil
	case reflect.Bool:
		if (len(buf) < 1) {
			return 0, errors.New("Bool: buffer too small")
		}
		if (val.Bool()) {
			buf[0] = byte(1)
		} else {
			buf[0] = byte(0)
		}
		return 1, nil
	case reflect.Uint16:
		if (len(buf) < 2) {
			return 0, errors.New("Uint16: buffer too small")
		}
		order.PutUint16(buf, uint16(val.Uint()))
		return 2, nil
	case reflect.Int16:
		if (len(buf) < 2) {
			return 0, errors.New("Int16: buffer too small")
		}
		order.PutUint16(buf, uint16(val.Int()))
		return 2, nil
	case reflect.Uint32:
		if (len(buf) < 4) {
			return 0, errors.New("Uint32: buffer too small")
		}
		order.PutUint32(buf, uint32(val.Uint()))
		return 4, nil
	case reflect.Int32:
		if (len(buf) < 4) {
			return 0, errors.New("Int32: buffer too small")
		}
		order.PutUint32(buf, uint32(val.Int()))
		return 4, nil
	case reflect.Uint64:
		if (len(buf) < 8) {
			return 0, errors.New("Uint64: buffer too small")
		}
		order.PutUint64(buf, val.Uint())
		return 8, nil
	case reflect.Int64:
		if (len(buf) < 8) {
			return 0, errors.New("Int64: buffer too small")
		}
		order.PutUint64(buf, uint64(val.Int()))
		return 8, nil
	default:
		return 0, fmt.Errorf("unsupported type: %s", val.Kind().String())
	}
	panic("assertion failed: unreachable code isn't.\n")
}

func Decode(buf []byte, order binary.ByteOrder, data any) (int, error) {
	var (
		size int
		v reflect.Value = reflect.ValueOf(data)
		err error
	)
	if (v.Kind() != reflect.Ptr || v.IsNil()) {
		return 0, errors.New("Decode: must pass a non-nil pointer")
	}
	size, err = decode_value(buf, order, v)
	return size, err
}

func decode_value(buf []byte, order binary.ByteOrder, val reflect.Value) (int, error) {
	if (val.Kind() == reflect.Ptr) {
		if (val.IsNil()) {
			return 0, errors.New("Pointer: nil pointer encountered")
		}
		val = val.Elem()
	}
	switch (val.Kind()) {
	case reflect.Ptr:
		return 0, errors.New("Pointer: Ptr to Ptr is not supported")
	case reflect.Struct:
		return decode_struct(buf, order, val);
	case reflect.String:
		return decode_bytes(buf, order, val);
	case reflect.Slice:
		fallthrough
	case reflect.Array:
		if (val.Type().Elem().Kind() == reflect.Uint8) { /* byte slice */
			return decode_bytes(buf, order, val);
		}
		return decodeArray(buf, order, val);
	case reflect.Uint8:
		if (len(buf) < 1) {
			return 0, errors.New("Uint8: buffer too small")
		}
		val.SetUint(uint64(buf[0]))
		return 1, nil
	case reflect.Int8:
		if (len(buf) < 1) {
			return 0, errors.New("Int8: buffer too small")
		}
		val.SetInt(int64(buf[0]))
		return 1, nil
	case reflect.Bool:
		if (len(buf) < 1) {
			return 0, errors.New("Bool: buffer too small")
		}
		if (buf[0] == 1) {
			val.SetBool(true)
		} else if (buf[0] == 0) {
			val.SetBool(false)
		} else {
			return 0, errors.New("Bool: illegal byte encoding")
		}
		return 1, nil
	case reflect.Uint16:
		if (len(buf) < 2) {
			return 0, errors.New("Uint16: buffer too small")
		}
		val.SetUint(uint64(order.Uint16(buf)))
		return 2, nil
	case reflect.Int16:
		if (len(buf) < 2) {
			return 0, errors.New("Int16: buffer too small")
		}
		val.SetInt(int64(order.Uint16(buf)))
		return 2, nil
	case reflect.Uint32:
		if (len(buf) < 4) {
			return 0, errors.New("Uint32: buffer too small")
		}
		val.SetUint(uint64(order.Uint32(buf)))
		return 4, nil
	case reflect.Int32:
		if (len(buf) < 4) {
			return 0, errors.New("Int32: buffer too small")
		}
		val.SetInt(int64(order.Uint32(buf)))
		return 4, nil
	case reflect.Uint64:
		if (len(buf) < 8) {
			return 0, errors.New("Uint64: buffer too small")
		}
		val.SetUint(order.Uint64(buf))
		return 8, nil
	case reflect.Int64:
		if (len(buf) < 8) {
			return 0, errors.New("Int64: buffer too small")
		}
		val.SetInt(int64(order.Uint64(buf)))
		return 8, nil
	default:
		return 0, fmt.Errorf("unsupported type: %s", val.Kind().String())
	}
	panic("assertion failed: unreachable code isn't.\n")

}

func decode_struct(buf []byte, order binary.ByteOrder, val reflect.Value) (int, error) {
	var (
		i, n int
		field reflect.Value
		numfield int = val.NumField()
		offset int = 0
		err error
	)
	for i = 0; i < numfield; i++ {
		field = val.Field(i)
		if (!field.CanSet()) {
			/*
			 * if the field is not exported (capital letter), then attempting to set it
			 * will fail with panic at runtime. Ignore those fields instead.
			 */
			continue
		}
		n, err = decode_value(buf[offset:], order, field.Addr())
		if (err != nil) {
			return offset, err
		}
		offset += n
	}
	return offset, nil
}

func decode_bytes(buf []byte, order binary.ByteOrder, val reflect.Value) (int, error) {
	var (
		offset int = 0
		n int
	)
	if (len(buf) < 2) {
		return offset, errors.New("Bytes: buffer too small for length indicator")
	}
	n = int(order.Uint16(buf))
	offset += 2
	if (len(buf[offset:]) < n) {
		return offset, errors.New("Bytes: buffer too small for byte data")
	}
	switch (val.Kind()) {
	case reflect.String:
		val.SetString(string(buf[offset : offset + n]))
	case reflect.Slice:
		val.SetBytes(buf[offset : offset + n])
	}
	offset += n
	return offset, nil
}

func decodeArray(buf []byte, order binary.ByteOrder, val reflect.Value) (int, error) {
	var (
		offset int = 0
		off, i, n int
		err error
	)
	if (len(buf) < 2) {
		return offset, errors.New("Array: buffer too small for length indicator")
	}
	n = int(order.Uint16(buf))
	offset += 2
	switch (val.Kind()) {
	case reflect.Array:
		if (n > val.Len()) {
			return offset, errors.New("Array: array too small to hold data")
		}
	case reflect.Slice:
		slice := reflect.MakeSlice(val.Type(), n, n)
		val.Set(slice)
	}
	for i = 0; i < n; i++ {
		off, err = decode_value(buf[offset:], order, val.Index(i).Addr())
		if (err != nil) {
			return offset, err
		}
		offset += off
	}
	return offset, nil
}
