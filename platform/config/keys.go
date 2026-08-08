package config

import (
	"reflect"
	"slices"
	"strings"
	"time"
)

// timeType is a struct we must NOT descend into: time.Time's fields are
// unexported implementation detail, and the value decodes from a single scalar.
var timeType = reflect.TypeOf(time.Time{})

// Keys returns every configuration key reachable in T, as viper dotted paths,
// sorted and deduplicated.
//
// This is the set Load binds to the environment. It is derived from the TYPE
// rather than from viper, because viper can only report keys it already knows —
// which by definition excludes the struct-only key that is the whole problem
// (ROADMAP P0-3 / review finding H-01).
//
// Only LEAF keys are returned. Binding an intermediate block key (`postgres`)
// as well as its children (`postgres.dsn`) would have the two fight over the
// same node in viper's settings map, and the block is not a value an operator
// can set through one environment variable anyway.
//
// It is exported because "which environment variables does this service read?"
// is otherwise unanswerable without reading a struct by hand, and a deployment
// review needs that list.
func Keys[T any]() []string {
	var zero T
	out := make([]string, 0, 32)
	appendKeys(reflect.TypeOf(zero), "", map[reflect.Type]bool{}, &out)
	slices.Sort(out)
	return slices.Compact(out)
}

// appendKeys walks t, appending the dotted path of every leaf field to out.
//
// path carries the types on the current recursion stack so a self-referential
// config type terminates instead of generating keys forever.
func appendKeys(t reflect.Type, prefix string, path map[reflect.Type]bool, out *[]string) {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	// Anything that is not a struct we descend into is a leaf: scalars, but
	// also maps, slices and interfaces. A slice IS bindable — viper splits a
	// comma-separated environment value — and that is how subject_asserters
	// arrives in Kubernetes.
	if t == nil || t.Kind() != reflect.Struct || t == timeType {
		if prefix != "" {
			*out = append(*out, prefix)
		}
		return
	}
	if path[t] {
		return
	}
	path[t] = true
	defer delete(path, t)

	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported — mapstructure cannot set it
			continue
		}
		name, opts := parseMapstructureTag(f)
		if name == "-" {
			continue
		}

		// A squashed field's own name never appears in the key path — that is
		// what squash means, and it is how config.Base's fields are addressed
		// as `server.port` rather than `base.server.port`. It applies only to
		// structs; on anything else mapstructure ignores it, so we must too,
		// or the field would be emitted under its parent's key.
		squashed := slices.Contains(opts, "squash") && isStruct(f.Type)
		key := prefix
		if !squashed {
			key = name
			if prefix != "" {
				key = prefix + "." + name
			}
		}
		appendKeys(f.Type, key, path, out)
	}
}

// parseMapstructureTag returns the field's key name and its tag options.
//
// An untagged field falls back to its lowercased Go name, which is what
// mapstructure matches on (it compares case-insensitively) and what viper
// stores, since viper lowercases every key it holds.
func parseMapstructureTag(f reflect.StructField) (name string, opts []string) {
	tag := f.Tag.Get("mapstructure")
	name, rest, _ := strings.Cut(tag, ",")
	if rest != "" {
		opts = strings.Split(rest, ",")
	}
	if name == "" {
		name = f.Name
	}
	return strings.ToLower(name), opts
}

func isStruct(t reflect.Type) bool {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t != nil && t.Kind() == reflect.Struct && t != timeType
}
