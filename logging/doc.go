// Package logging is the single sanctioned construction path for a service's
// *zap.Logger. It exists to close a fleet-wide gap: every CDPG Go service
// already depends on go.uber.org/zap, but each one calls zap.NewProduction()
// inline, discarding the constructor's error and ignoring
// config.BaseConfig.LogLevel entirely — so a service's configured log level
// has no effect anywhere, and encoding/fields silently drift service by
// service.
//
// Services should call New once at startup with a Config built from their
// already-loaded BaseConfig, then pass the resulting *zap.Logger down through
// their dependency graph (and to middleware.Logger for request logging) as
// they do today — this package only changes how the logger is constructed,
// not how it's used.
package logging
