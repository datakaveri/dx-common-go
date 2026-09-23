package bootstrap

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/datakaveri/dx-common-go/logging"
)

// newLogger builds the service logger from the configured level.
//
// This function exists so that the logger CANNOT be built before config is
// loaded — the ordering is owned by bootstrap rather than by each service's
// main(). Three services (dx-audit-go, dx-subscription-go, dx-gateway-go) call
// zap.NewProduction() before config.Load and therefore ignore log_level
// entirely; that is not a mistake anyone can avoid by being careful, it is a
// consequence of the boot sequence being hand-written 18 times.
//
// Every log line carries service and version, so a line in an aggregated stream
// is attributable without correlating it against a deployment.
func newLogger(level, service, version string) (*zap.Logger, error) {
	cfg := zap.NewProductionConfig()

	lvl, err := zapcore.ParseLevel(level)
	if err != nil {
		// An unparseable level is a config error worth surfacing, not a reason
		// to silently run at info — the operator asked for something specific.
		return nil, err
	}
	cfg.Level = zap.NewAtomicLevelAt(lvl)

	// ISO8601 rather than epoch floats: a human reads these during an incident.
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	cfg.EncoderConfig.TimeKey = "ts"
	cfg.EncoderConfig.MessageKey = "msg"

	fields := []zap.Field{zap.String("service", service)}
	if version != "" {
		fields = append(fields, zap.String("version", version))
	}
	// S13.2 backstop: every bootstrap.Run service gets the same field-key
	// denylist + secret-value masking as logging.New, applied at the single
	// point every one of them builds its logger — see logging.NewRedactingCore.
	return cfg.Build(zap.Fields(fields...), zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		return logging.NewRedactingCore(core)
	}))
}
