package domain

import (
	"reflect"
	"strings"
)

// nulField names the first string in v, a decoded JSON document rooted at path, that holds U+0000.
// PostgreSQL's text and jsonb cannot store that character, and a refused cast aborts the
// transaction. The walk covers every exported field the JSON encoding writes, so a field added
// later is checked without being listed here. A map key is checked as content of its map, which
// it is named after.
func nulField(v reflect.Value, path string) (string, bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return nulField(v.Elem(), path)
		}
	case reflect.String:
		return path, strings.ContainsRune(v.String(), 0)
	case reflect.Slice, reflect.Array:
		// Bytes are encoded as base64 or, for a UUID, as text, neither of which can hold NUL.
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return "", false
		}
		for i := range v.Len() {
			if field, ok := nulField(v.Index(i), path); ok {
				return field, true
			}
		}
	case reflect.Map:
		entries := v.MapRange()
		for entries.Next() {
			if field, ok := nulField(entries.Key(), path); ok {
				return field, true
			}
			if field, ok := nulField(entries.Value(), path); ok {
				return field, true
			}
		}
	case reflect.Struct:
		t := v.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if !f.IsExported() || name == "-" {
				continue
			}
			next := path
			if name != "" {
				next = path + "." + name
			} else if !f.Anonymous {
				next = path + "." + f.Name
			}
			if field, ok := nulField(v.Field(i), next); ok {
				return field, true
			}
		}
	}
	return "", false
}
