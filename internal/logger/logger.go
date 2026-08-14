package logger

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Logger wraps zerolog for structured logging.
type Logger struct {
	zl zerolog.Logger
}

// New creates a new Logger with the given configuration.
func New(level string, format string, w io.Writer) *Logger {
	if w == nil {
		w = os.Stderr
	}
	return &Logger{zl: build(level, format, w)}
}

// Info logs at info level.
func (l *Logger) Info() *zerolog.Event {
	return l.zl.Info()
}

// Debug logs at debug level.
func (l *Logger) Debug() *zerolog.Event {
	return l.zl.Debug()
}

// Warn logs at warn level.
func (l *Logger) Warn() *zerolog.Event {
	return l.zl.Warn()
}

// Error logs at error level.
func (l *Logger) Error() *zerolog.Event {
	return l.zl.Error()
}

// WithContext returns a logger with context values.
func (l *Logger) WithContext(ctx context.Context) *zerolog.Logger {
	logger := l.zl.With().Logger()
	return &logger
}

// Default returns a default logger (info level, JSON format).
func Default() *Logger {
	return New("info", "json", os.Stderr)
}

// Init initializes the global logger. Call once at startup.
func Init(level, format string) {
	log.Logger = build(level, format, os.Stderr)
}

// build assembles a logger for one writer, so New and Init cannot drift apart.
func build(level, format string, w io.Writer) zerolog.Logger {
	zl := zerolog.New(w).With().Timestamp().Logger().Level(levelOf(level))
	if format == "text" {
		zl = zl.Output(zerolog.ConsoleWriter{
			Out:        w,
			TimeFormat: time.RFC3339,
			NoColor:    true,
		})
	}
	return zl
}

// levelOf maps a configured name onto a zerolog level, defaulting to info so a
// typo does not silence the server.
func levelOf(level string) zerolog.Level {
	switch level {
	case "silent":
		return zerolog.Disabled
	case "error":
		return zerolog.ErrorLevel
	case "warn":
		return zerolog.WarnLevel
	case "info":
		return zerolog.InfoLevel
	case "debug":
		return zerolog.DebugLevel
	default:
		return zerolog.InfoLevel
	}
}
