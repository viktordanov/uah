package config

import (
	"reflect"
	"slices"
	"strings"
)

// Keys lists every key of the configuration as a dotted path, sorted: the
// tables and their keys, nested ones included, with <name> for a key of a
// map, such as mcp_servers.<name>.command. The reference test checks that
// docs/configuration.md names each, and the new settings notice compares
// them with the ones the user has seen (newsettings.go).
func Keys() []string {
	var keys []string
	walkKeys(reflect.TypeFor[Config](), "", &keys)
	slices.Sort(keys)

	return keys
}

// walkKeys adds the keys of a struct type under prefix, walking structs
// behind pointers, slices, and maps.
func walkKeys(typ reflect.Type, prefix string, keys *[]string) {
	for field := range typ.Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("toml"), ",")
		if name == "" || name == "-" {
			continue
		}
		path := prefix + name
		*keys = append(*keys, path)
		inner, maps := field.Type, 0
		for inner.Kind() == reflect.Pointer || inner.Kind() == reflect.Slice || inner.Kind() == reflect.Map {
			if inner.Kind() == reflect.Map {
				maps++
			}
			inner = inner.Elem()
		}
		if inner.Kind() == reflect.Struct {
			walkKeys(inner, path+strings.Repeat(".<name>", maps)+".", keys)
		}
	}
}
