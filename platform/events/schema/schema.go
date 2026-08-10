// Package schema pins event payload shapes so an incompatible change cannot
// ship silently.
//
// # What this catches (ROADMAP P0-6)
//
// A topic carries a version, Publish stamps it, and Subscribe quarantines
// anything else. That machinery is correct and it protects nothing on its own,
// because NOTHING TIES THE VERSION TO THE PAYLOAD STRUCT. Rename a JSON field,
// change a type, drop a field — the version stays 1, producers start emitting
// the new shape, and every existing consumer decodes it into a zero value or
// quarantines the lot. The version says "same contract" while the contract has
// moved.
//
// This package makes the shape an artefact: a fingerprint per topic+version,
// checked into git, compared in CI. Changing a payload without bumping its
// version is then a failing test with a diff, not an incident with a full DLQ.
//
// # Why a fingerprint and not a JSON Schema
//
// A JSON Schema would describe the payload more richly and would need a
// generator, a validator and a decision about which dialect. The question here
// is narrower: DID THIS SHAPE CHANGE. A canonical rendering of the fields Go
// will actually marshal answers exactly that, in a form a reviewer can read in
// a diff and understand without tooling.
//
// # What it deliberately does NOT decide
//
// It does not judge whether a change is backward-compatible. Adding an optional
// field usually is; renaming one never is; widening an int is somewhere in
// between and depends on the consumer. That judgement belongs to the person
// reading the diff, and encoding a guess would license the changes it guessed
// wrong about.
package schema

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Fingerprint renders the JSON shape of T as canonical text.
//
// Deterministic across runs and machines: fields are emitted in declaration
// order (which is the order the struct defines, not map order), and every
// nested struct is expanded inline so a change two levels down still moves the
// fingerprint.
func Fingerprint[T any]() string {
	var zero T
	var b strings.Builder
	writeType(&b, reflect.TypeOf(&zero).Elem(), 0, map[reflect.Type]bool{})
	return b.String()
}

// writeType renders t at the given indent.
//
// seen breaks recursive types (a payload with a parent pointer, a tree node).
// Without it this recurses until the stack dies, and the first person to add a
// self-referential payload would get a crash rather than a fingerprint.
func writeType(b *strings.Builder, t reflect.Type, depth int, seen map[reflect.Type]bool) {
	indent := strings.Repeat("  ", depth)

	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.Struct:
		if seen[t] {
			fmt.Fprintf(b, "%s<recursive %s>\n", indent, t.Name())
			return
		}
		seen[t] = true
		defer delete(seen, t)

		for i := range t.NumField() {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue // unexported: encoding/json ignores it, so the wire shape does not include it
			}
			name, omitted := jsonName(f)
			if omitted {
				continue
			}
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			switch ft.Kind() {
			case reflect.Struct, reflect.Slice, reflect.Array, reflect.Map:
				fmt.Fprintf(b, "%s%s:\n", indent, name)
				writeType(b, ft, depth+1, seen)
			default:
				fmt.Fprintf(b, "%s%s: %s\n", indent, name, ft.Kind())
			}
		}

	case reflect.Slice, reflect.Array:
		fmt.Fprintf(b, "%s[]\n", indent)
		writeType(b, t.Elem(), depth+1, seen)

	case reflect.Map:
		fmt.Fprintf(b, "%smap[%s]\n", indent, t.Key().Kind())
		writeType(b, t.Elem(), depth+1, seen)

	default:
		fmt.Fprintf(b, "%s%s\n", indent, t.Kind())
	}
}

// jsonName resolves the wire name of a field, and whether json skips it.
//
// The TAG is what matters, not the Go name: renaming a Go field while keeping
// its tag changes nothing on the wire and must NOT move the fingerprint, or
// every refactor becomes a false contract break and the check gets ignored.
func jsonName(f reflect.StructField) (name string, omitted bool) {
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		return f.Name, false
	}
	parts := strings.Split(tag, ",")
	if parts[0] == "-" && len(parts) == 1 {
		return "", true
	}
	if parts[0] == "" {
		return f.Name, false
	}
	return parts[0], false
}

// Entry is one pinned topic shape.
type Entry struct {
	// Topic is the routing name.
	Topic string
	// Version is the payload version the fingerprint belongs to.
	Version int
	// Fingerprint is the canonical shape.
	Fingerprint string
}

// Key identifies an entry. Topic AND version, so bumping a version ADDS a
// pinned shape rather than replacing one — which is what makes the N/N-1
// compatibility window visible in the file instead of implied.
func (e Entry) Key() string { return fmt.Sprintf("%s@v%d", e.Topic, e.Version) }

// Render produces the golden-file text for a set of entries.
//
// Sorted by key so the file is stable regardless of declaration order, and a
// diff shows only what actually changed.
func Render(entries []Entry) string {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key() < sorted[j].Key() })

	var b strings.Builder
	b.WriteString("# Event payload shapes, pinned so an incompatible change cannot ship silently.\n")
	b.WriteString("# Generated — regenerate with: go test ./... -run TestEventSchemas -update\n")
	b.WriteString("#\n")
	b.WriteString("# A diff here is a CONTRACT CHANGE. Adding a field is usually safe; renaming or\n")
	b.WriteString("# removing one is not; changing a type depends on the consumer. If the change is\n")
	b.WriteString("# not backward compatible, bump the topic's version — which ADDS an entry here\n")
	b.WriteString("# rather than modifying one, leaving both shapes visible for the N/N-1 window.\n")
	for _, e := range sorted {
		fmt.Fprintf(&b, "\n=== %s\n%s", e.Key(), e.Fingerprint)
	}
	return b.String()
}
