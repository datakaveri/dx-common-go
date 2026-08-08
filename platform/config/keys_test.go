package config_test

import (
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/platform/config"
)

type inner struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

type keysConfig struct {
	config.Base `mapstructure:",squash"`

	Postgres inner  `mapstructure:"postgres"`
	Redis    *inner `mapstructure:"redis"`

	// Untagged: mapstructure matches on the lowercased field name.
	Untagged string

	Ignored   string            `mapstructure:"-"`
	Deadline  time.Duration     `mapstructure:"deadline"`
	StartedAt time.Time         `mapstructure:"started_at"`
	Asserters []string          `mapstructure:"asserters"`
	Labels    map[string]string `mapstructure:"labels"`

	unexported string //nolint:unused // the point of the field is that Keys skips it
}

// recursive is a config type that contains itself. Nothing in the fleet looks
// like this, but a walker that does not guard against it hangs instead of
// failing, which is the worst way to find out.
type recursive struct {
	Name string     `mapstructure:"name"`
	Next *recursive `mapstructure:"next"`
}

// twice reaches the same type down two different branches. It is NOT a cycle,
// and a walker that guards cycles with a plain "seen this type" set would
// wrongly drop the second branch — so this pins that the guard tracks the
// current recursion PATH, not every type ever visited.
type twice struct {
	Left  inner `mapstructure:"left"`
	Right inner `mapstructure:"right"`
}

func TestKeys(t *testing.T) {
	got := config.Keys[keysConfig]()

	// Present: leaves only, with the embedded Base squashed to the top level.
	for _, want := range []string{
		"log_level",   // from the squashed config.Base
		"server.port", // nested inside the squashed Base
		"internal_auth.shared_secret",
		"postgres.host",
		"postgres.port",
		"redis.host", // through a POINTER to a struct
		"untagged",   // lowercased Go field name
		"deadline",   // time.Duration is a leaf, not a struct to descend
		"started_at", // time.Time likewise — its fields are unexported detail
		"asserters",  // a slice IS bindable; viper splits comma-separated env
		"labels",
	} {
		if !contains2(got, want) {
			t.Errorf("Keys() is missing %q\ngot: %v", want, got)
		}
	}

	// Absent: intermediate blocks, ignored fields, unexported fields, and any
	// key implying we descended into a type we must treat as a scalar.
	for _, unwanted := range []string{
		"postgres", // an intermediate block is never bound
		"server",   //
		"base",     // squash means the field's own name does not appear
		"base.server.port",
		"ignored",
		"unexported",
		"started_at.wall", // descending into time.Time
		"deadline.nanos",
	} {
		if contains2(got, unwanted) {
			t.Errorf("Keys() must not contain %q\ngot: %v", unwanted, got)
		}
	}
}

// TestKeys_RecursiveTypeTerminates pins termination, and pins that the
// recursive BRANCH is dropped rather than unrolled to some arbitrary depth.
//
// Dropping it is the honest answer: arbitrary nesting cannot be addressed in a
// flat environment namespace at all, so there is no depth at which the branch
// becomes bindable — only one at which the truncation is less obvious.
func TestKeys_RecursiveTypeTerminates(t *testing.T) {
	done := make(chan []string, 1)
	go func() { done <- config.Keys[recursive]() }()

	select {
	case got := <-done:
		if !contains2(got, "name") {
			t.Errorf("Keys() = %v, want the non-recursive leaf", got)
		}
		if contains2(got, "next.next.name") {
			t.Errorf("Keys() = %v, want the recursive branch dropped, not partly unrolled", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Keys() did not terminate on a self-referential config type")
	}
}

func TestKeys_SameTypeOnTwoBranches(t *testing.T) {
	got := config.Keys[twice]()
	for _, want := range []string{"left.host", "left.port", "right.host", "right.port"} {
		if !contains2(got, want) {
			t.Errorf("Keys() is missing %q — the cycle guard dropped a repeated type that is not a cycle\ngot: %v", want, got)
		}
	}
}

func TestKeys_IsSortedAndDeduplicated(t *testing.T) {
	got := config.Keys[keysConfig]()
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("Keys() must be sorted and deduplicated: %q then %q", got[i-1], got[i])
		}
	}
}

func contains2(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
