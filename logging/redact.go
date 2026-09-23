package logging

import (
	"regexp"
	"strings"

	"go.uber.org/zap/zapcore"
)

// prohibitedFieldKeys are stripped outright, regardless of value — the key
// alone identifies content that must never reach a diagnostic log line
// (OBSERVABILITY.md §10.1 / GO-SERVICE-STANDARDS S13.2: "no personal data,
// secrets or credentials in diagnostic logs"). Matched case-insensitively so
// "Authorization", "authorization" and "X-DX-Workload" are all caught.
//
// This is deliberately a small, high-confidence denylist of fields that are
// NEVER legitimate in a diagnostic log line, not a general PII filter — a
// broad heuristic filter would either miss real secrets (false negatives, the
// dangerous direction) or strip legitimate operational fields no one asked to
// remove (false positives, which just gets the filter disabled). Anything
// wider (user/org/delegation identity) is a per-service field-selection
// decision at the call site, not a global drop here (§10.1's "not by default"
// column, not "forbidden").
var prohibitedFieldKeys = map[string]struct{}{
	"authorization":   {},
	"x-dx-workload":   {},
	"cookie":          {},
	"set-cookie":      {},
	"password":        {},
	"secret":          {},
	"client_secret":   {},
	"workload_secret": {},
	"token":           {},
	"access_token":    {},
	"refresh_token":   {},
	"id_token":        {},
	"api_key":         {},
	"apikey":          {},
}

// bearerPattern catches a bearer credential embedded in a field's VALUE even
// when the key gives no hint (a raw header dump, an error that echoes a
// request) — the same pattern the Collector's transform/redaction processor
// applies as its backstop (observability/otel-collector.yaml). The
// application-layer copy here is PRIMARY per OBSERVABILITY.md §10.3; the
// Collector's is the secondary net for whatever slips past this one.
var bearerPattern = regexp.MustCompile(`(?i)bearer\s+[a-z0-9._\-]+`)

const redactedBearer = "bearer ***"

// redactField returns the field to keep and whether to keep it at all: false
// when the key alone is prohibited (the field is dropped, not replaced —
// there is no safe value to log under a key like "password"); true (with the
// value possibly masked) otherwise.
func redactField(f zapcore.Field) (zapcore.Field, bool) {
	if _, forbidden := prohibitedFieldKeys[strings.ToLower(f.Key)]; forbidden {
		return zapcore.Field{}, false
	}
	if f.Type == zapcore.StringType && bearerPattern.MatchString(f.String) {
		f.String = bearerPattern.ReplaceAllString(f.String, redactedBearer)
	}
	return f, true
}

// redactFields filters and masks a field slice in place semantics (returns a
// new slice; the input is never mutated since zapcore.Field is a value type
// copied by redactField).
func redactFields(fields []zapcore.Field) []zapcore.Field {
	if len(fields) == 0 {
		return fields
	}
	out := make([]zapcore.Field, 0, len(fields))
	for _, f := range fields {
		if rf, keep := redactField(f); keep {
			out = append(out, rf)
		}
	}
	return out
}

// redactingCore wraps a zapcore.Core and redacts every field before it
// reaches the wrapped core — whether attached persistently via
// logger.With(...) or passed at the call site (logger.Info(msg, fields...)).
type redactingCore struct {
	zapcore.Core
}

// NewRedactingCore wraps core so every field written through it passes the
// platform's field-key denylist and value-pattern backstop before it reaches
// the encoder — the technical control behind S13.2. Wire it once, at logger
// construction (logging.New, platform/bootstrap's newLogger); nothing else in
// a service needs to change, and nothing a handler logs can bypass it.
func NewRedactingCore(core zapcore.Core) zapcore.Core {
	return &redactingCore{Core: core}
}

// With must redact eagerly: fields attached here are NOT re-passed through
// Write, so an unredacted With call would leak on every subsequent line from
// the derived logger, not just the line that attached it.
func (c *redactingCore) With(fields []zapcore.Field) zapcore.Core {
	return &redactingCore{Core: c.Core.With(redactFields(fields))}
}

// Check re-points the CheckedEntry at THIS core, not the wrapped one — using
// c.Core.Check here would hand the caller a shortcut straight to the
// unwrapped core's Write, bypassing redaction entirely. Same pattern zap's
// own level-filtering and hook wrappers use.
func (c *redactingCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return ce.AddCore(entry, c)
	}
	return ce
}

func (c *redactingCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	return c.Core.Write(entry, redactFields(fields))
}
