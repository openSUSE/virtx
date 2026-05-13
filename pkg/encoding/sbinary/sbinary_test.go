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
package sbinary

import (
	"encoding/binary"
	"testing"
)

func Test_encode_decode_uint8(t *testing.T) {
	var (
		buf [64]byte
		input uint8 = 0xAB
		output uint8
		n int
		err error
	)
	n, err = Encode(buf[:], binary.BigEndian, input)
	if (err != nil) {
		t.Fatalf("Encode uint8: %v", err)
	}
	if (n != 1) {
		t.Fatalf("Encode uint8: expected 1 byte, got %d", n)
	}
	n, err = Decode(buf[:], binary.BigEndian, &output)
	if (err != nil) {
		t.Fatalf("Decode uint8: %v", err)
	}
	if (n != 1) {
		t.Fatalf("Decode uint8: expected 1 byte, got %d", n)
	}
	if (output != input) {
		t.Fatalf("uint8 round-trip: expected %d, got %d", input, output)
	}
}

func Test_encode_decode_int8(t *testing.T) {
	var (
		buf [64]byte
		input int8 = -42
		output int8
		n int
		err error
	)
	n, err = Encode(buf[:], binary.BigEndian, input)
	if (err != nil) {
		t.Fatalf("Encode int8: %v", err)
	}
	if (n != 1) {
		t.Fatalf("Encode int8: expected 1 byte, got %d", n)
	}
	n, err = Decode(buf[:], binary.BigEndian, &output)
	if (err != nil) {
		t.Fatalf("Decode int8: %v", err)
	}
	if (n != 1) {
		t.Fatalf("Decode int8: expected 1 byte, got %d", n)
	}
	if (output != input) {
		t.Fatalf("int8 round-trip: expected %d, got %d", input, output)
	}
}

func Test_encode_decode_bool(t *testing.T) {
	cases := []struct {
		name string
		val  bool
	}{
		{"true", true},
		{"false", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				buf [64]byte
				output bool
			)
			n, err := Encode(buf[:], binary.BigEndian, tc.val)
			if (err != nil) {
				t.Fatalf("Encode bool: %v", err)
			}
			if (n != 1) {
				t.Fatalf("Encode bool: expected 1 byte, got %d", n)
			}
			n, err = Decode(buf[:], binary.BigEndian, &output)
			if (err != nil) {
				t.Fatalf("Decode bool: %v", err)
			}
			if (output != tc.val) {
				t.Fatalf("bool round-trip: expected %v, got %v", tc.val, output)
			}
		})
	}
}

func Test_encode_decode_uint16(t *testing.T) {
	var (
		buf [64]byte
		input uint16 = 0xBEEF
		output uint16
	)
	n, err := Encode(buf[:], binary.BigEndian, input)
	if (err != nil) {
		t.Fatalf("Encode uint16: %v", err)
	}
	if (n != 2) {
		t.Fatalf("Encode uint16: expected 2 bytes, got %d", n)
	}
	n, err = Decode(buf[:], binary.BigEndian, &output)
	if (err != nil) {
		t.Fatalf("Decode uint16: %v", err)
	}
	if (n != 2) {
		t.Fatalf("Decode uint16: expected 2 bytes, got %d", n)
	}
	if (output != input) {
		t.Fatalf("uint16 round-trip: expected %d, got %d", input, output)
	}
}

func Test_encode_decode_int16(t *testing.T) {
	var (
		buf [64]byte
		input int16 = -12345
		output int16
	)
	n, err := Encode(buf[:], binary.BigEndian, input)
	if (err != nil) {
		t.Fatalf("Encode int16: %v", err)
	}
	if (n != 2) {
		t.Fatalf("Encode int16: expected 2 bytes, got %d", n)
	}
	n, err = Decode(buf[:], binary.BigEndian, &output)
	if (err != nil) {
		t.Fatalf("Decode int16: %v", err)
	}
	if (output != input) {
		t.Fatalf("int16 round-trip: expected %d, got %d", input, output)
	}
}

func Test_encode_decode_uint32(t *testing.T) {
	var (
		buf [64]byte
		input uint32 = 0xDEADBEEF
		output uint32
	)
	n, err := Encode(buf[:], binary.BigEndian, input)
	if (err != nil) {
		t.Fatalf("Encode uint32: %v", err)
	}
	if (n != 4) {
		t.Fatalf("Encode uint32: expected 4 bytes, got %d", n)
	}
	n, err = Decode(buf[:], binary.BigEndian, &output)
	if (err != nil) {
		t.Fatalf("Decode uint32: %v", err)
	}
	if (output != input) {
		t.Fatalf("uint32 round-trip: expected %d, got %d", input, output)
	}
}

func Test_encode_decode_int32(t *testing.T) {
	var (
		buf [64]byte
		input int32 = -100000
		output int32
	)
	n, err := Encode(buf[:], binary.BigEndian, input)
	if (err != nil) {
		t.Fatalf("Encode int32: %v", err)
	}
	if (n != 4) {
		t.Fatalf("Encode int32: expected 4 bytes, got %d", n)
	}
	n, err = Decode(buf[:], binary.BigEndian, &output)
	if (err != nil) {
		t.Fatalf("Decode int32: %v", err)
	}
	if (output != input) {
		t.Fatalf("int32 round-trip: expected %d, got %d", input, output)
	}
}

func Test_encode_decode_uint64(t *testing.T) {
	var (
		buf [64]byte
		input uint64 = 0xDEADBEEFCAFEBABE
		output uint64
	)
	n, err := Encode(buf[:], binary.BigEndian, input)
	if (err != nil) {
		t.Fatalf("Encode uint64: %v", err)
	}
	if (n != 8) {
		t.Fatalf("Encode uint64: expected 8 bytes, got %d", n)
	}
	n, err = Decode(buf[:], binary.BigEndian, &output)
	if (err != nil) {
		t.Fatalf("Decode uint64: %v", err)
	}
	if (output != input) {
		t.Fatalf("uint64 round-trip: expected %d, got %d", input, output)
	}
}

func Test_encode_decode_int64(t *testing.T) {
	var (
		buf [64]byte
		input int64 = -9876543210
		output int64
	)
	n, err := Encode(buf[:], binary.BigEndian, input)
	if (err != nil) {
		t.Fatalf("Encode int64: %v", err)
	}
	if (n != 8) {
		t.Fatalf("Encode int64: expected 8 bytes, got %d", n)
	}
	n, err = Decode(buf[:], binary.BigEndian, &output)
	if (err != nil) {
		t.Fatalf("Decode int64: %v", err)
	}
	if (output != input) {
		t.Fatalf("int64 round-trip: expected %d, got %d", input, output)
	}
}

func Test_encode_decode_string(t *testing.T) {
	cases := []struct {
		name string
		val  string
	}{
		{"empty", ""},
		{"short", "hello"},
		{"with_spaces", "hello world 123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				buf [256]byte
				output string
			)
			n, err := Encode(buf[:], binary.BigEndian, tc.val)
			if (err != nil) {
				t.Fatalf("Encode string: %v", err)
			}
			if (n != 2 + len(tc.val)) {
				t.Fatalf("Encode string: expected %d bytes, got %d", 2 + len(tc.val), n)
			}
			n, err = Decode(buf[:], binary.BigEndian, &output)
			if (err != nil) {
				t.Fatalf("Decode string: %v", err)
			}
			if (output != tc.val) {
				t.Fatalf("string round-trip: expected %q, got %q", tc.val, output)
			}
		})
	}
}

func Test_encode_decode_byte_slice(t *testing.T) {
	var (
		buf [256]byte
		input []byte = []byte{0x01, 0x02, 0x03, 0xFF}
		output []byte
	)
	n, err := Encode(buf[:], binary.BigEndian, input)
	if (err != nil) {
		t.Fatalf("Encode []byte: %v", err)
	}
	if (n != 2 + len(input)) {
		t.Fatalf("Encode []byte: expected %d bytes, got %d", 2 + len(input), n)
	}
	n, err = Decode(buf[:], binary.BigEndian, &output)
	if (err != nil) {
		t.Fatalf("Decode []byte: %v", err)
	}
	if (len(output) != len(input)) {
		t.Fatalf("[]byte round-trip: expected len %d, got %d", len(input), len(output))
	}
	for i := range input {
		if (output[i] != input[i]) {
			t.Fatalf("[]byte round-trip: mismatch at index %d: expected %d, got %d", i, input[i], output[i])
		}
	}
}

func Test_encode_decode_struct(t *testing.T) {
	type simple_struct struct {
		A uint16
		B int32
		C string
	}
	var (
		buf [256]byte
		input = simple_struct{A: 42, B: -1000, C: "test"}
		output simple_struct
	)
	n, err := Encode(buf[:], binary.BigEndian, input)
	if (err != nil) {
		t.Fatalf("Encode struct: %v", err)
	}
	_, err = Decode(buf[:n], binary.BigEndian, &output)
	if (err != nil) {
		t.Fatalf("Decode struct: %v", err)
	}
	if (output.A != input.A || output.B != input.B || output.C != input.C) {
		t.Fatalf("struct round-trip: expected %+v, got %+v", input, output)
	}
}

func Test_encode_decode_struct_unexported_fields(t *testing.T) {
	type mixed_struct struct {
		Exported uint32
		hidden   uint32
	}
	var (
		buf [256]byte
		input = mixed_struct{Exported: 99, hidden: 77}
		output mixed_struct
	)
	n, err := Encode(buf[:], binary.BigEndian, input)
	if (err != nil) {
		t.Fatalf("Encode struct with unexported: %v", err)
	}
	if (n != 4) {
		t.Fatalf("Encode struct with unexported: expected 4 bytes (only exported), got %d", n)
	}
	_, err = Decode(buf[:n], binary.BigEndian, &output)
	if (err != nil) {
		t.Fatalf("Decode struct with unexported: %v", err)
	}
	if (output.Exported != input.Exported) {
		t.Fatalf("exported field: expected %d, got %d", input.Exported, output.Exported)
	}
	if (output.hidden != 0) {
		t.Fatalf("unexported field should be zero, got %d", output.hidden)
	}
}

func Test_encode_decode_nested_struct(t *testing.T) {
	type inner struct {
		X int16
		Y int16
	}
	type outer struct {
		Name string
		Inner inner
		Val  uint8
	}
	var (
		buf [256]byte
		input = outer{Name: "abc", Inner: inner{X: 10, Y: -20}, Val: 0xFF}
		output outer
	)
	n, err := Encode(buf[:], binary.BigEndian, input)
	if (err != nil) {
		t.Fatalf("Encode nested struct: %v", err)
	}
	_, err = Decode(buf[:n], binary.BigEndian, &output)
	if (err != nil) {
		t.Fatalf("Decode nested struct: %v", err)
	}
	if (output.Name != input.Name || output.Inner.X != input.Inner.X ||
		output.Inner.Y != input.Inner.Y || output.Val != input.Val) {
		t.Fatalf("nested struct round-trip: expected %+v, got %+v", input, output)
	}
}

func Test_encode_decode_slice_of_structs(t *testing.T) {
	type item struct {
		Id   uint16
		Name string
	}
	var (
		buf [512]byte
		input = []item{
			{Id: 1, Name: "first"},
			{Id: 2, Name: "second"},
			{Id: 3, Name: "third"},
		}
		output []item
	)
	n, err := Encode(buf[:], binary.BigEndian, input)
	if (err != nil) {
		t.Fatalf("Encode []struct: %v", err)
	}
	_, err = Decode(buf[:n], binary.BigEndian, &output)
	if (err != nil) {
		t.Fatalf("Decode []struct: %v", err)
	}
	if (len(output) != len(input)) {
		t.Fatalf("[]struct round-trip: expected len %d, got %d", len(input), len(output))
	}
	for i := range input {
		if (output[i].Id != input[i].Id || output[i].Name != input[i].Name) {
			t.Fatalf("[]struct round-trip: mismatch at %d: expected %+v, got %+v", i, input[i], output[i])
		}
	}
}

func Test_encode_decode_byte_order(t *testing.T) {
	var (
		buf_be [8]byte
		buf_le [8]byte
		input uint32 = 0x01020304
		out_be uint32
		out_le uint32
	)
	Encode(buf_be[:], binary.BigEndian, input)
	Encode(buf_le[:], binary.LittleEndian, input)

	if (buf_be[0] != 0x01 || buf_be[1] != 0x02 || buf_be[2] != 0x03 || buf_be[3] != 0x04) {
		t.Fatalf("BigEndian encoding wrong: %x", buf_be[:4])
	}
	if (buf_le[0] != 0x04 || buf_le[1] != 0x03 || buf_le[2] != 0x02 || buf_le[3] != 0x01) {
		t.Fatalf("LittleEndian encoding wrong: %x", buf_le[:4])
	}

	Decode(buf_be[:], binary.BigEndian, &out_be)
	Decode(buf_le[:], binary.LittleEndian, &out_le)
	if (out_be != input || out_le != input) {
		t.Fatalf("byte order round-trip failed: be=%d le=%d expected=%d", out_be, out_le, input)
	}
}

func Test_encode_nil_pointer(t *testing.T) {
	var buf [64]byte
	_, err := Encode(buf[:], binary.BigEndian, (*uint32)(nil))
	if (err == nil) {
		t.Fatal("Encode nil pointer: expected error, got nil")
	}
}

func Test_decode_non_pointer(t *testing.T) {
	var buf [64]byte
	var val uint32
	_, err := Decode(buf[:], binary.BigEndian, val)
	if (err == nil) {
		t.Fatal("Decode non-pointer: expected error, got nil")
	}
}

func Test_decode_nil_pointer(t *testing.T) {
	var buf [64]byte
	_, err := Decode(buf[:], binary.BigEndian, (*uint32)(nil))
	if (err == nil) {
		t.Fatal("Decode nil pointer: expected error, got nil")
	}
}

func Test_encode_buffer_too_small(t *testing.T) {
	cases := []struct {
		name    string
		bufsize int
		data    any
	}{
		{"uint16_in_1byte", 1, uint16(1)},
		{"uint32_in_2bytes", 2, uint32(1)},
		{"uint64_in_4bytes", 4, uint64(1)},
		{"int16_in_1byte", 1, int16(1)},
		{"int32_in_2bytes", 2, int32(1)},
		{"int64_in_4bytes", 4, int64(1)},
		{"bool_in_0bytes", 0, true},
		{"uint8_in_0bytes", 0, uint8(1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := make([]byte, tc.bufsize)
			_, err := Encode(buf, binary.BigEndian, tc.data)
			if (err == nil) {
				t.Fatal("expected buffer too small error, got nil")
			}
		})
	}
}

func Test_encode_unsupported_type(t *testing.T) {
	var buf [64]byte
	_, err := Encode(buf[:], binary.BigEndian, float64(1.0))
	if (err == nil) {
		t.Fatal("Encode float64: expected error, got nil")
	}
}

func Test_decode_bool_invalid_byte(t *testing.T) {
	buf := []byte{0x02}
	var output bool
	_, err := Decode(buf, binary.BigEndian, &output)
	if (err == nil) {
		t.Fatal("Decode bool with byte 0x02: expected error, got nil")
	}
}

func Test_encode_decode_empty_slice(t *testing.T) {
	type item struct {
		Id uint16
	}
	var (
		buf [64]byte
		input = []item{}
		output []item
	)
	n, err := Encode(buf[:], binary.BigEndian, input)
	if (err != nil) {
		t.Fatalf("Encode empty slice: %v", err)
	}
	if (n != 2) {
		t.Fatalf("Encode empty slice: expected 2 bytes (length prefix), got %d", n)
	}
	_, err = Decode(buf[:n], binary.BigEndian, &output)
	if (err != nil) {
		t.Fatalf("Decode empty slice: %v", err)
	}
	if (len(output) != 0) {
		t.Fatalf("empty slice round-trip: expected len 0, got %d", len(output))
	}
}
