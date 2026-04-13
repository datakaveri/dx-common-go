package config

import (
	"github.com/datakaveri/dx-common-go/httpserver"
)

// BaseConfig is the common configuration block that all CDPG services embed.
// Service-specific config structs should embed this and add their own sections.
type BaseConfig struct {
	// LogLevel is the minimum log level: debug, info, warn, error.
	LogLevel string `mapstructure:"log_level"`
	// Server holds HTTP server tuning parameters.
	Server httpserver.Config `mapstructure:"server"`
	// AuthEnabled controls whether JWT validation middleware is active.
	// Set to false for local development / testing.
	AuthEnabled bool `mapstructure:"auth_enabled"`
}
