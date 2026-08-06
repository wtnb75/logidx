package logging

import (
	"io"
	"log/slog"
)

// New builds a slog.Logger writing to w. format selects the handler
// ("json" for slog.NewJSONHandler, anything else defaults to text via
// slog.NewTextHandler). verbose lowers the level to Debug; otherwise Info.
func New(w io.Writer, format string, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}

	return slog.New(handler)
}
