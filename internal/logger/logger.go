package logger

import (
	"log/slog"
	"os"
	"strings"
)

// Init configures the global slog logger based on the app environment.
// Development/dev/local: text format, debug level.
// All other values (including production): JSON format, info level.
func Init(env string) {
	var handler slog.Handler

	switch strings.ToLower(env) {
	case "development", "dev", "local":
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})
	default:
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
	}

	slog.SetDefault(slog.New(handler))
}
