// Package schema derives JSON Schemas for MCP tool inputs from the protocol's
// Go types, so the types stay the single source of truth.
package schema

import (
	"encoding/json"
	"reflect"
	"strings"
)

// Describer lets a type supply its own schema.
type Describer interface {
	JSONSchema() map[string]any
}

var (
	rawType       = reflect.TypeOf(json.RawMessage{})
	describerType = reflect.TypeOf((*Describer)(nil)).Elem()
)

// For returns the schema of v's type.
func For(v any) json.RawMessage {
	s := of(reflect.TypeOf(v), map[reflect.Type]bool{})
	data, _ := json.Marshal(s)
	return data
}

func of(t reflect.Type, seen map[reflect.Type]bool) map[string]any {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Implements(describerType) {
		return reflect.Zero(t).Interface().(Describer).JSONSchema()
	}
	if reflect.PointerTo(t).Implements(describerType) {
		return reflect.New(t).Interface().(Describer).JSONSchema()
	}
	if t == rawType {
		return map[string]any{}
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Slice, reflect.Array:
		s := map[string]any{"type": "array", "items": of(t.Elem(), seen)}
		if t.Kind() == reflect.Array {
			s["minItems"], s["maxItems"] = t.Len(), t.Len()
		}
		return s
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": of(t.Elem(), seen)}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Struct:
		if seen[t] {
			return map[string]any{"type": "object"} // recursion: stop here
		}
		seen[t] = true
		defer delete(seen, t)
		props := map[string]any{}
		var required []string
		addFields(t, props, &required, seen)
		s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	return map[string]any{}
}

func addFields(t reflect.Type, props map[string]any, required *[]string, seen map[reflect.Type]bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				addFields(ft, props, required, seen)
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		props[name] = of(f.Type, seen)
		if !strings.Contains(opts, "omitempty") && f.Type.Kind() != reflect.Pointer {
			*required = append(*required, name)
		}
	}
}
