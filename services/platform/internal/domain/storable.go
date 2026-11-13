package domain

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"reflect"
	"strings"
)

// PostgreSQL's text and jsonb cannot store U+0000. A write holding one is refused, and the
// refusal aborts its transaction. Input from outside the server is kept clear of it where it is
// decoded, as docs/json-encoding.md#text-postgresql-cannot-store describes: NULField finds it
// in what a person or program chose to send, which is refused, and ReplaceNUL replaces it in
// what a host's own output produced, which must not stop that host's agent.

// UnstorableText is a write PostgreSQL refused because its text held a character it cannot
// store. Input is checked where it is decoded, so this answers only a path that missed that
// check, which store.Normalize turns into this rather than a failure.
var UnstorableText = &Error{Code: "invalid_input", Message: "The request holds a character that cannot be stored, such as NUL."}

var rawJSON = reflect.TypeFor[jsontext.Value]()

// NULField names the first string in v, rooted at path, that holds U+0000, including a string
// inside a raw JSON value, where it is written as an escape. The walk covers every exported field
// the JSON encoding reads or writes, so a field added later is checked without being listed here.
// A map key is checked as content of its map, which it is named after. With an empty root, a
// field is named from its top-level member, and a NUL in the root value itself as "request".
func NULField(v any, root string) (string, bool) {
	field, ok := nulField(reflect.ValueOf(v), root)
	if ok && field == "" {
		field = "request"
	}
	return field, ok
}

func nulField(v reflect.Value, path string) (string, bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return nulField(v.Elem(), path)
		}
	case reflect.String:
		return path, strings.ContainsRune(v.String(), 0)
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			// Other bytes are encoded as base64 or, for a UUID, as text, neither of which can
			// hold NUL.
			if v.Type() != rawJSON || !EscapedNUL(v.Bytes()) {
				return "", false
			}
			// A raw value is named down to the member that holds NUL, as its typed reader would
			// name it, so that a client can point at the field.
			var doc any
			if json.Unmarshal(v.Bytes(), &doc) != nil {
				return path, true
			}
			return rawNULField(doc, path)
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
			if !decoded(t.Field(i)) {
				continue
			}
			if field, ok := nulField(v.Field(i), member(path, t.Field(i))); ok {
				return field, true
			}
		}
	}
	return "", false
}

// rawNULField is nulField for a decoded JSON document, whose object members it names. A member
// whose own name holds NUL is named by its parent.
func rawNULField(doc any, path string) (string, bool) {
	switch doc := doc.(type) {
	case string:
		return path, strings.ContainsRune(doc, 0)
	case []any:
		for _, item := range doc {
			if field, ok := rawNULField(item, path); ok {
				return field, true
			}
		}
	case map[string]any:
		for name, item := range doc {
			if strings.ContainsRune(name, 0) {
				return path, true
			}
			next := name
			if path != "" {
				next = path + "." + name
			}
			if field, ok := rawNULField(item, next); ok {
				return field, true
			}
		}
	}
	return "", false
}

// ReplaceNUL replaces U+0000 with a space in every string v points to that the JSON encoding
// reads, including map keys and the escapes in raw JSON values, so that the length of what a
// host reported does not change. It reports the field of a map in which a replaced key would
// equal another, whose keys it leaves as they are, and then the caller must refuse v.
func ReplaceNUL(v any) (string, bool) {
	_, field, collides := replaceNUL(reflect.ValueOf(v), "")
	if collides && field == "" {
		field = "request"
	}
	return field, collides
}

// replaceNUL is ReplaceNUL for a settable v, and also reports whether it changed v.
func replaceNUL(v reflect.Value, path string) (changed bool, field string, collides bool) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			return replaceNUL(v.Elem(), path)
		}
	case reflect.Interface:
		if v.IsNil() {
			break
		}
		// What an interface holds cannot be changed in place, so a copy is changed and stored.
		c := reflect.New(v.Elem().Type()).Elem()
		c.Set(v.Elem())
		changed, field, collides := replaceNUL(c, path)
		if collides {
			return false, field, true
		}
		if changed {
			v.Set(c)
		}
		return changed, "", false
	case reflect.String:
		if strings.ContainsRune(v.String(), 0) {
			v.SetString(strings.ReplaceAll(v.String(), "\x00", " "))
			changed = true
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if v.Type() == rawJSON {
				changed = ReplaceEscapedNUL(v.Bytes())
			}
			break
		}
		for i := range v.Len() {
			c, field, collides := replaceNUL(v.Index(i), path)
			if collides {
				return false, field, true
			}
			changed = changed || c
		}
	case reflect.Map:
		type entry struct{ key, newKey, value reflect.Value }
		var replaced []entry
		entries := v.MapRange()
		for entries.Next() {
			e := entry{entries.Key(), reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()}
			e.newKey.Set(e.key)
			e.value.Set(entries.Value())
			keyChanged, _, _ := replaceNUL(e.newKey, path)
			valueChanged, field, collides := replaceNUL(e.value, path)
			if collides {
				return false, field, true
			}
			if keyChanged || valueChanged {
				replaced = append(replaced, e)
			}
		}
		renamed := map[any]bool{}
		for _, e := range replaced {
			if e.newKey.Equal(e.key) {
				continue
			}
			if v.MapIndex(e.newKey).IsValid() || renamed[e.newKey.Interface()] {
				return false, path, true
			}
			renamed[e.newKey.Interface()] = true
		}
		for _, e := range replaced {
			v.SetMapIndex(e.key, reflect.Value{})
		}
		for _, e := range replaced {
			v.SetMapIndex(e.newKey, e.value)
		}
		changed = len(replaced) > 0
	case reflect.Struct:
		t := v.Type()
		for i := range t.NumField() {
			if !decoded(t.Field(i)) {
				continue
			}
			c, field, collides := replaceNUL(v.Field(i), member(path, t.Field(i)))
			if collides {
				return false, field, true
			}
			changed = changed || c
		}
	}
	return changed, "", false
}

// member is the path of a struct field below path: its JSON member, or its parent's for an
// embedded struct the encoding inlines.
func member(path string, f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" {
		if f.Anonymous {
			return path
		}
		name = f.Name
	}
	if path == "" {
		return name
	}
	return path + "." + name
}

// decoded is whether the JSON encoding reads f: an exported field its tag does not omit. No type
// these walk embeds a struct of an unexported type, whose exported fields the encoding would read.
func decoded(f reflect.StructField) bool {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	return f.IsExported() && name != "-"
}

// EscapedNUL reports whether raw, a valid JSON value, writes U+0000, which it can only do as the
// escape \u0000: JSON allows no raw control character.
func EscapedNUL(raw []byte) bool {
	return escapedNUL(raw, false)
}

// ReplaceEscapedNUL rewrites each \u0000 in raw, a valid JSON value, as \u0020, a space, in place,
// and reports whether it found one.
func ReplaceEscapedNUL(raw []byte) bool {
	return escapedNUL(raw, true)
}

func escapedNUL(raw []byte, replace bool) bool {
	found := false
	for i := 0; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		// A backslash outside a string is not valid JSON, so every one here starts an escape,
		// and the character after it is never the start of another.
		if bytes.HasPrefix(raw[i+1:], []byte("u0000")) {
			if !replace {
				return true
			}
			copy(raw[i+1:], "u0020")
			found = true
		}
		i++
	}
	return found
}
