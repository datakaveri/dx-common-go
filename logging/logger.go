package logging

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New builds a *zap.Logger from cfg. It is the one sanctioned construction
// path for CDPG Go services, replacing the fleet-wide pattern of each
// service calling zap.NewProduction() directly and discarding both the
// constructor's error and its own configured log level.
//
// Typical use, right after a service loads its BaseConfig:
//
//	logger, err := logging.New(logging.Config{
//		Level:       cfg.LogLevel,
//		ServiceName: "dx-acl-go",
//	})
//	if err != nil {
//		fmt.Fprintln(os.Stderr, err)
//		os.Exit(1)
//	}
//	defer logger.Sync()
func New(cfg Config) (*zap.Logger, error) {
	var zcfg zap.Config
	if cfg.Development {
		zcfg = zap.NewDevelopmentConfig()
	} else {
		zcfg = zap.NewProductionConfig()
	}
	zcfg.Level = zap.NewAtomicLevelAt(parseLevel(cfg.Level))
	// Matches the ISO8601 "time" convention already hand-built in the two
	// services (dx-community-layer-go, dx-files-connect-api-go) that construct
	// a level-aware logger themselves today, so adopting this package changes
	// their log shape as little as possible.
	zcfg.EncoderConfig.TimeKey = "time"
	zcfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	// S13.2 backstop: strip/mask known secret and credential fields before
	// they reach the encoder, regardless of what a call site logs. Wrapped
	// here rather than left to each handler, so it applies to every line this
	// logger (and everything derived from it via .With/.Named) ever writes.
	logger, err := zcfg.Build(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		return NewRedactingCore(core)
	}))
	if err != nil {
		return nil, fmt.Errorf("logging.New: build zap logger: %w", err)
	}

	if cfg.ServiceName != "" {
		logger = logger.With(zap.String("service", cfg.ServiceName))
	}
	return logger, nil
}

// parseLevel maps a case-insensitive level name to a zapcore.Level,
// defaulting to Info on empty or unrecognized input.
func parseLevel(s string) zapcore.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return zapcore.DebugLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
