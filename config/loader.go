package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// LoadInto reads a configuration file at filePath, merges any matching
// DX_-prefixed environment variables, and unmarshals the result into a value
// of type T. The file format is inferred from the extension (yaml, json, toml).
//
// Environment variable mapping:
//
//	DX_SERVER_PORT  →  server.port
//	DX_AUTH_ENABLED →  auth_enabled
func LoadInto[T any](filePath string) (*T, error) {
	v := viper.New()
	v.SetEnvPrefix("DX")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if filePath != "" {
		v.SetConfigFile(filePath)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("config.LoadInto: reading %q: %w", filePath, err)
		}
	}

	var cfg T
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("config.LoadInto: unmarshal: %w", err)
	}
	return &cfg, nil
}
