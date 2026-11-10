package logging

// Config controls how New builds the shared *zap.Logger. The zero value is a
// fully valid, production-safe configuration: JSON encoding, info level, no
// service field.
type Config struct {
	// Level is the minimum log level: "debug", "info", "warn", or "error"
	// (case-insensitive, whitespace-trimmed). Empty or unrecognized input
	// defaults to "info" — New never fails because of this field, since a
	// bad log_level value in config should never stop a service from
	// starting. Typically sourced straight from config.BaseConfig.LogLevel.
	Level string

	// ServiceName, if set, is attached to every log line as a "service"
	// field, so logs stay attributable once aggregated across the fleet
	// (e.g. "dx-acl-go", "dx-gateway-go").
	ServiceName string

	// Development switches to zap's development preset: console encoding,
	// human-readable timestamps, caller info, and DPanic-on-error — for
	// local `go run`, not production. Defaults to false (JSON/production).
	Development bool
}
