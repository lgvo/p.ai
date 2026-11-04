package api

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/methods.golden from the types")

// TestMethodShapesMatchGolden pins every method's JSON field names and types:
// a renamed JSON tag compiles everywhere and still breaks shell clients, so it
// must change this file, docs/archive/control-api.md and the VM scripts together.
// Run `go test ./internal/api -run Golden -update` to rewrite it.
func TestMethodShapesMatchGolden(t *testing.T) {
	var b strings.Builder
	for _, group := range []struct {
		name    string
		methods []MethodInfo
	}{{"control", Methods()}, {"session", SessionMethods()}} {
		for _, m := range group.methods {
			fmt.Fprintf(&b, "%s %s", group.name, m.Name)
			if m.Capability != "" {
				fmt.Fprintf(&b, " (%s)", m.Capability)
			}
			b.WriteString("\n  request\n")
			writeFields(&b, m.Request, "    ", map[reflect.Type]bool{})
			b.WriteString("  result\n")
			writeFields(&b, m.Result, "    ", map[reflect.Type]bool{})
		}
	}
	got := b.String()
	if *update {
		if err := os.WriteFile("testdata/methods.golden", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/methods.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("method shapes differ from testdata/methods.golden; update it, docs/archive/control-api.md and the VM scripts together (go test ./internal/api -run Golden -update)")
	}
}

var (
	rawType  = reflect.TypeFor[json.RawMessage]()
	timeType = reflect.TypeFor[time.Time]()
)

// writeFields writes one line per JSON field of struct t: name, type and
// whether it may be omitted or null, and the fields of nested objects below.
func writeFields(b *strings.Builder, t reflect.Type, indent string, open map[reflect.Type]bool) {
	if open[t] {
		fmt.Fprintf(b, "%s(recursive %s)\n", indent, t.Name())
		return
	}
	open[t] = true
	defer delete(open, t)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		parts := strings.Split(f.Tag.Get("json"), ",")
		name := parts[0]
		if !f.IsExported() || name == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			// encoding/json lifts an embedded struct's fields.
			delete(open, t)
			writeFields(b, f.Type, indent, open)
			open[t] = true
			continue
		}
		var marks []string
		if len(parts) > 1 && parts[1] == "omitempty" {
			marks = append(marks, "optional")
		}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
			if len(marks) == 0 {
				marks = append(marks, "nullable")
			}
		}
		kind, elem := jsonType(ft)
		line := indent + name + " " + kind
		if len(marks) > 0 {
			line += " (" + strings.Join(marks, ", ") + ")"
		}
		b.WriteString(line + "\n")
		if elem != nil {
			writeFields(b, elem, indent+"  ", open)
		}
	}
}

// jsonType names t's JSON type and returns the struct whose fields follow.
func jsonType(t reflect.Type) (string, reflect.Type) {
	switch {
	case t == rawType:
		return "raw", nil
	case t == timeType:
		return "time", nil
	}
	switch t.Kind() {
	case reflect.String:
		return "string", nil
	case reflect.Bool:
		return "boolean", nil
	case reflect.Int, reflect.Int64, reflect.Uint32, reflect.Uint64:
		return "integer", nil
	case reflect.Struct:
		return "object", t
	case reflect.Slice:
		kind, elem := jsonType(t.Elem())
		return "array of " + kind, elem
	}
	return "unsupported " + t.String(), nil
}

// No request or result is a loose shape: every one is a struct whose fields
// are typed, never a map or an interface.
func TestMethodTypesAreClosedStructs(t *testing.T) {
	for _, m := range append(Methods(), SessionMethods()...) {
		for _, typ := range []reflect.Type{m.Request, m.Result} {
			if typ.Kind() != reflect.Struct {
				t.Errorf("%s: %s is not a struct", m.Name, typ)
				continue
			}
			if loose := looseField(typ, map[reflect.Type]bool{}); loose != "" {
				t.Errorf("%s: %s has a loose field %s", m.Name, typ, loose)
			}
		}
	}
}

func looseField(t reflect.Type, seen map[reflect.Type]bool) string {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if seen[t] || t == rawType || t == timeType {
		return ""
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Interface, reflect.Map:
		return t.String()
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if loose := looseField(t.Field(i).Type, seen); loose != "" {
				return t.Field(i).Name + ": " + loose
			}
		}
	}
	return ""
}

func TestMethodNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range Methods() {
		if seen[m.Name] {
			t.Errorf("%s listed twice", m.Name)
		}
		seen[m.Name] = true
	}
	if seen["system.capabilities"] {
		t.Error("system.capabilities is still a method")
	}
}
