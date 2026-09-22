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
	"reflect"
	"strings"

	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
)

/*
 * diff_slice_key returns the value of the first field with json tag "name" or
 * "path" in the struct element, used as the key for set-based slice diff.
 * Returns "" if no such field is found.
 */
func diff_slice_key(elem reflect.Value) string {
	t := elem.Type()
	for _, want := range [...]string{"name", "path"} {
		for i := 0; i < t.NumField(); i++ {
			tag := t.Field(i).Tag.Get("json")
			idx := strings.Index(tag, ",")
			if (idx >= 0) {
				tag = tag[:idx]
			}
			if (tag == want) {
				return elem.Field(i).String()
			}
		}
	}
	return ""
}

/*
 * diff_leaf recursively walks two struct values, comparing primitive (leaf)
 * fields using reflection. Slice fields are diffed by element key ("name" or
 * "path"); elements present in one side but not the other are noted as +/-.
 */
func diff_leaf(old_val, new_val reflect.Value, prefix string, changes *[]string) {
	t := old_val.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		idx := strings.Index(tag, ",")
		if (idx >= 0) {
			tag = tag[:idx]
		}
		if (tag == "" || tag == "-") {
			continue
		}
		name := tag
		if (prefix != "") {
			name = prefix + "." + tag
		}
		old_field := old_val.Field(i)
		new_field := new_val.Field(i)
		switch old_field.Kind() {
		case reflect.Struct:
			diff_leaf(old_field, new_field, name, changes)
		case reflect.Slice:
			old_keys := make(map[string]bool)
			for j := 0; j < old_field.Len(); j++ {
				k := diff_slice_key(old_field.Index(j))
				if (k != "") {
					old_keys[k] = true
				}
			}
			new_keys := make(map[string]bool)
			for j := 0; j < new_field.Len(); j++ {
				k := diff_slice_key(new_field.Index(j))
				if (k != "") {
					new_keys[k] = true
				}
			}
			for j := 0; j < new_field.Len(); j++ {
				k := diff_slice_key(new_field.Index(j))
				if (k != "" && !old_keys[k]) {
					*changes = append(*changes, fmt.Sprintf("+%s:%s", tag, k))
				}
			}
			for j := 0; j < old_field.Len(); j++ {
				k := diff_slice_key(old_field.Index(j))
				if (k != "" && !new_keys[k]) {
					*changes = append(*changes, fmt.Sprintf("-%s:%s", tag, k))
				}
			}
		case reflect.String:
			if (old_field.String() != new_field.String()) {
				*changes = append(*changes, fmt.Sprintf("%s:%s->%s", name, old_field.String(), new_field.String()))
			}
		case reflect.Bool:
			if (old_field.Bool() != new_field.Bool()) {
				*changes = append(*changes, fmt.Sprintf("%s:%t->%t", name, old_field.Bool(), new_field.Bool()))
			}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			if (old_field.Int() != new_field.Int()) {
				*changes = append(*changes, fmt.Sprintf("%s:%d->%d", name, old_field.Int(), new_field.Int()))
			}
		default:
			logger.Log("diff_leaf: unhandled kind %s for field %s", old_field.Kind(), name)
		}
	}
}

/*
 * Diff returns a one-line space-separated summary of changes from old to
 * new_def, suitable for use as an oplog start message.
 */
func Diff(old, new_def openapi.Vmdef) string {
	var changes []string
	diff_leaf(reflect.ValueOf(old), reflect.ValueOf(new_def), "", &changes)
	if (len(changes) == 0) {
		return "no changes"
	}
	return strings.Join(changes, " ")
}
