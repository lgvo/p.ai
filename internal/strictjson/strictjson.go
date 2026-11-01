// Package strictjson decodes JSON at P's authority boundaries, such as RPC
// parameters, trusted configuration and native proof responses. Duplicate
// keys, field names that differ from the exact JSON tag, unknown fields and
// trailing values are rejected.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
)

// Decode decodes data into dest under the closed-schema rules above.
func Decode(data []byte, dest any) error {
	if err := RejectDuplicateKeys(data); err != nil {
		return err
	}
	if err := rejectInexactStructFields(data, dest); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}

// encoding/json deliberately accepts case-insensitive struct field names.
// RPC schemas are closed and versioned, so require the exact JSON tag at
// every typed struct boundary. Maps, raw messages and custom decoders retain
// their own key semantics.
func rejectInexactStructFields(data []byte, dest any) error {
	t := reflect.TypeOf(dest)
	if t == nil || t.Kind() != reflect.Pointer || t.Elem().Kind() == reflect.Invalid {
		return errors.New("JSON destination must be a pointer")
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	return exactStructFields(value, t.Elem())
}

var jsonUnmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()

func exactStructFields(value any, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Implements(jsonUnmarshalerType) || reflect.PointerTo(t).Implements(jsonUnmarshalerType) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		} // the normal decoder reports type mismatches
		fields := make(map[string]reflect.Type)
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")[0]
			if tag == "-" {
				continue
			}
			if tag == "" && field.Anonymous {
				for name, nested := range exactFieldTypes(field.Type) {
					fields[name] = nested
				}
				continue
			}
			if tag == "" {
				tag = field.Name
			}
			fields[tag] = field.Type
		}
		for name, child := range object {
			fieldType, ok := fields[name]
			if !ok {
				return errors.New("unknown or inexact JSON field")
			}
			if err := exactStructFields(child, fieldType); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if values, ok := value.([]any); ok {
			for _, child := range values {
				if err := exactStructFields(child, t.Elem()); err != nil {
					return err
				}
			}
		}
	case reflect.Map:
		if values, ok := value.(map[string]any); ok {
			for _, child := range values {
				if err := exactStructFields(child, t.Elem()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func exactFieldTypes(t reflect.Type) map[string]reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	fields := make(map[string]reflect.Type)
	if t.Kind() != reflect.Struct {
		return fields
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == "-" {
			continue
		}
		if tag == "" && field.Anonymous {
			for name, nested := range exactFieldTypes(field.Type) {
				fields[name] = nested
			}
			continue
		}
		if tag == "" {
			tag = field.Name
		}
		fields[tag] = field.Type
	}
	return fields
}

// RejectDuplicateKeys rejects ambiguous JSON keys at every object depth,
// including method params, trusted policy and native proof responses. JSON
// decoder's normal last-value-wins behavior is unsafe at authority boundaries.
func RejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]bool)
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("duplicate or invalid JSON object key")
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}
