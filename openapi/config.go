package openapi

// Config holds configuration for OpenAPI spec loading and validation.
type Config struct {
	SpecPath          string `mapstructure:"spec_path"`
	SwaggerUIEnabled  bool   `mapstructure:"swagger_ui_enabled"`
	SwaggerUIPath     string `mapstructure:"swagger_ui_path"`
	ValidateRequests  bool   `mapstructure:"validate_requests"`
	ValidateResponses bool   `mapstructure:"validate_responses"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		SwaggerUIEnabled: true,
		SwaggerUIPath:    "/docs",
		ValidateRequests: true,
	}
}
