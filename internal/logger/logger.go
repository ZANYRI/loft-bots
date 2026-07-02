// Package logger provides one structured (JSON) logger for the whole
// project. It exposes the same Printf/Println/Fatal* signatures as the
// standard "log" package so existing call sites work unchanged after
// swapping the import.
package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

var base *slog.Logger

func init() {
	Init()
}

// Init (re)configures the global logger from LOG_LEVEL / LOG_FORMAT env vars.
// LOG_FORMAT=text switches to human-readable output; default is JSON.
func Init() {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{Level: level, AddSource: true}

	var handler slog.Handler
	if strings.EqualFold(strings.TrimSpace(os.Getenv("LOG_FORMAT")), "text") {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	service := strings.TrimSpace(os.Getenv("SERVICE_NAME"))
	if service == "" {
		service = "loft-bots"
	}

	base = slog.New(handler).With(slog.String("service", service))
	slog.SetDefault(base)
}

// L returns the underlying structured logger for callers that want to
// attach fields, e.g. logger.L().With("order_id", id).Info("created").
func L() *slog.Logger {
	return base
}

// With returns a child logger carrying the given key/value pairs.
func With(args ...any) *slog.Logger {
	return base.With(args...)
}

func Debug(msg string, args ...any) { base.Debug(msg, args...) }
func Info(msg string, args ...any)  { base.Info(msg, args...) }
func Warn(msg string, args ...any)  { base.Warn(msg, args...) }
func Error(msg string, args ...any) { base.Error(msg, args...) }

func DebugContext(ctx context.Context, msg string, args ...any) { base.DebugContext(ctx, msg, args...) }
func InfoContext(ctx context.Context, msg string, args ...any)  { base.InfoContext(ctx, msg, args...) }
func WarnContext(ctx context.Context, msg string, args ...any)  { base.WarnContext(ctx, msg, args...) }
func ErrorContext(ctx context.Context, msg string, args ...any) { base.ErrorContext(ctx, msg, args...) }

// Printf/Println/Fatal* mirror the standard "log" package API so files can
// switch their import from "log" to "loft-bots/internal/logger" without
// rewriting every call site.

func Printf(format string, v ...any) {
	base.Info(fmt.Sprintf(format, v...))
}

func Println(v ...any) {
	base.Info(strings.TrimSuffix(fmt.Sprintln(v...), "\n"))
}

func Print(v ...any) {
	base.Info(fmt.Sprint(v...))
}

func Fatalf(format string, v ...any) {
	base.Error(fmt.Sprintf(format, v...))
	os.Exit(1)
}

func Fatalln(v ...any) {
	base.Error(strings.TrimSuffix(fmt.Sprintln(v...), "\n"))
	os.Exit(1)
}

func Fatal(v ...any) {
	base.Error(fmt.Sprint(v...))
	os.Exit(1)
}
