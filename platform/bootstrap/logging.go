package bootstrap

import (
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/datakaveri/dx-common-go/logging"
)

// Log formats. The value comes from the log_format config key.
const (
	// logFormatAuto picks console when stderr is a terminal and JSON when it
	// is not. A developer running the binary gets a readable, coloured stream;
	// a container writing to a log collector gets the machine-parseable one,
	// with nobody having to remember to set anything in either place.
	logFormatAuto = "auto"
	// logFormatConsole is the human format: level, time, caller, message, then
	// the fields. Coloured when the destination is a terminal.
	logFormatConsole = "console"
	// logFormatJSON is the aggregated format — one object per line.
	logFormatJSON = "json"
)

// newLogger builds the service logger from the configured level and format.
//
// This function exists so that the logger CANNOT be built before config is
// loaded — the ordering is owned by bootstrap rather than by each service's
// main(). Three services (dx-audit-go, dx-subscription-go, dx-gateway-go) call
// zap.NewProduction() before config.Load and therefore ignore log_level
// entirely; that is not a mistake anyone can avoid by being careful, it is a
// consequence of the boot sequence being hand-written 18 times.
//
// In JSON every log line carries service and version, so a line in an
// aggregated stream is attributable without correlating it against a
// deployment. The console format drops them: it is read by the person who
// started the process, who already knows which service they are looking at,
// and repeating it on every line is what makes a local stream unreadable.
func newLogger(level, format, service, version string) (*zap.Logger, error) {
	lvl, err := zapcore.ParseLevel(level)
	if err != nil {
		// An unparseable level is a config error worth surfacing, not a reason
		// to silently run at info — the operator asked for something specific.
		return nil, err
	}

	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(lvl)
	// ISO8601 rather than epoch floats: a human reads these during an incident.
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	cfg.EncoderConfig.TimeKey = "ts"
	cfg.EncoderConfig.MessageKey = "msg"

	if !useConsole(format) {
		fields := []zap.Field{zap.String("service", service)}
		if version != "" {
			fields = append(fields, zap.String("version", version))
		}
		return cfg.Build(zap.Fields(fields...), zap.WrapCore(redactingCore))
	}

	cfg.Encoding = logFormatConsole
	// Sampling drops repeated messages after the first hundred a second. That
	// is right for a hot production path and wrong at a terminal, where the
	// line that vanished is the one being debugged.
	cfg.Sampling = nil
	cfg.EncoderConfig.ConsoleSeparator = "  "
	// Wall-clock time only. The date is the day the developer is having.
	cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("15:04:05.000")
	cfg.EncoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	if colour() {
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}
	cfg.EncoderConfig.EncodeCaller = zapcore.ShortCallerEncoder
	return cfg.Build(zap.WrapCore(redactingCore))
}

// redactingCore is the S13.2 backstop: every bootstrap.Run service gets the
// same field-key denylist + secret-value masking as logging.New, applied at the
// single point every one of them builds its logger — in both formats, since a
// console stream is just as likely to be pasted into a ticket.
func redactingCore(core zapcore.Core) zapcore.Core {
	return logging.NewRedactingCore(core)
}

// useConsole resolves the configured format against the destination. An
// unrecognised value behaves as auto rather than failing the boot: the log
// format is how the operator would SEE a boot failure, so refusing to start
// over it hides the very message that explains why.
func useConsole(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case logFormatJSON:
		return false
	case logFormatConsole:
		return true
	default: // auto, empty, anything unrecognised
		return isTerminal(os.Stderr)
	}
}

// colour reports whether ANSI level colouring is wanted. NO_COLOR is honoured
// because it is the convention every other CLI on the developer's machine
// already follows (no-color.org); a value is not required, presence is enough.
func colour() bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(os.Stderr)
}

// isTerminal reports whether f is a character device — a terminal, as opposed
// to the pipe or file a container's stdout is. Stat rather than a x/term
// dependency: this is the only place the platform needs to know.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
