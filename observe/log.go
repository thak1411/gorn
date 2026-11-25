// Package observe provides optional structured logging and HTTP instrumentation.
// It never sends logs to a remote service or starts background exporters.
package observe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
)

var secretText = regexp.MustCompile(`(?i)(authorization|cookie|password|passwd|secret|access_token|refresh_token|token|api_key)(["']?\s*[:=]\s*["']?)[^\s,;"']+`)
var bearerText = regexp.MustCompile(`(?i)Bearer\s+[^\s"']+`)
var urlText = regexp.MustCompile(`https?://[^\s<>"']+`)

// CleanText strips URL credentials, all query strings, and common credential
// forms from legacy messages. Applications must still avoid logging bodies and
// arbitrary user input: redaction cannot recognize every possible secret.
func CleanText(value string) string {
	value = bearerText.ReplaceAllString(value, "Bearer [REDACTED]")
	value = secretText.ReplaceAllString(value, "$1$2[REDACTED]")
	value = urlText.ReplaceAllStringFunc(value, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[URL]"
		}
		u.User, u.RawQuery, u.Fragment = nil, "", ""
		if strings.Contains(u.Path, "/webhooks/") {
			u.Path = "/webhooks/[REDACTED]"
		}
		return u.String()
	})
	if len(value) > 8192 {
		value = value[:8192] + " [truncated]"
	}
	return value
}

func sensitive(key string) bool {
	k := strings.ToLower(key)
	for _, s := range []string{"password", "passwd", "authorization", "cookie", "token", "secret", "api_key", "request_body", "response_body", "source_code"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

func NewLogger(w io.Writer, service, environment, version string) *slog.Logger {
	level := slog.LevelInfo
	_ = level.UnmarshalText([]byte(os.Getenv("LOG_LEVEL")))
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			a.Value = slog.TimeValue(a.Value.Time().UTC())
		}
		if sensitive(a.Key) {
			return slog.String(a.Key, "[REDACTED]")
		}
		if a.Value.Kind() == slog.KindString {
			a.Value = slog.StringValue(CleanText(a.Value.String()))
		}
		return a
	}})
	return slog.New(handler).With("service", service, "environment", environment, "version", version)
}

func Configure(service string) *slog.Logger {
	environment := os.Getenv("GORN_ENV")
	if environment == "" {
		environment = "local"
	}
	version := os.Getenv("GORN_VERSION")
	if version == "" {
		version = "development"
	}
	l := NewLogger(os.Stdout, service, environment, version)
	slog.SetDefault(l)
	return l
}

func ErrorType(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		for errors.Unwrap(err) != nil {
			err = errors.Unwrap(err)
		}
		return reflect.TypeOf(err).String()
	}
}

// ReportError distinguishes a cancelled request from an unrelated error that
// happens to race with cancellation. Deadlines and background cancellations are
// never silently suppressed. Call at the boundary that owns the failure.
func ReportError(ctx context.Context, logger *slog.Logger, operation string, err error) {
	if err == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	level, outcome := slog.LevelError, "failure"
	if errors.Is(err, context.Canceled) && errors.Is(ctx.Err(), context.Canceled) {
		level, outcome = slog.LevelInfo, "cancelled"
	}
	state := stateFrom(ctx)
	if state != nil {
		state.mu.Lock()
		state.outcome = outcome
		state.errorType = ErrorType(err)
		state.mu.Unlock()
	}
	logger.Log(ctx, level, CleanText(operation), "event", "application_error", "request_id", RequestID(ctx), "error_type", ErrorType(err), "error", CleanText(err.Error()), "outcome", outcome)
}

// Printf bridges legacy loggers while preserving an error's type as a field.
func Printf(level slog.Level, event, format string, args ...any) {
	attrs := []any{"event", event}
	for _, arg := range args {
		if err, ok := arg.(error); ok {
			attrs = append(attrs, "error_type", ErrorType(err))
			break
		}
	}
	slog.Log(context.Background(), level, CleanText(fmt.Sprintf(format, args...)), attrs...)
}
