package observe

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/felixge/httpsnoop"
)

type requestKey struct{}
type requestState struct {
	mu                            sync.Mutex
	id, actor, outcome, errorType string
	appCode                       int
}

func stateFrom(ctx context.Context) *requestState {
	s, _ := ctx.Value(requestKey{}).(*requestState)
	return s
}

func RequestID(ctx context.Context) string {
	if s := stateFrom(ctx); s != nil {
		return s.id
	}
	return ""
}

func SetActor(ctx context.Context, actor string) {
	if s := stateFrom(ctx); s != nil {
		s.mu.Lock()
		s.actor = actor
		s.mu.Unlock()
	}
}

func SetResult(ctx context.Context, code int) {
	if s := stateFrom(ctx); s != nil {
		s.mu.Lock()
		s.appCode = code
		s.mu.Unlock()
	}
}

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
var validTraceparent = regexp.MustCompile(`^00-([0-9a-f]{32})-[0-9a-f]{16}-[0-9a-f]{2}$`)

type HTTPOptions struct {
	Logger *slog.Logger
	// Skip successful probes; failures are always recorded.
	HealthPaths []string
	// Audit selects state-changing operations. No request/response bodies are read.
	Audit func(*http.Request) bool
	// SuccessCode enables application-result classification when SetResult is used.
	SuccessCode int
}

// HTTP records one completion event per request, even for 404 and preflight.
// Optional ResponseWriter interfaces (including Hijacker and Flusher) are
// preserved by httpsnoop. Transport aborts retain net/http's original behavior.
func HTTP(options HTTPOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			logger := options.Logger
			if logger == nil {
				logger = slog.Default()
			}
			id := req.Header.Get("X-Request-ID")
			if !validRequestID.MatchString(id) {
				var bytes [16]byte
				if _, err := rand.Read(bytes[:]); err != nil {
					panic(err)
				}
				id = hex.EncodeToString(bytes[:])
			}
			state := &requestState{id: id}
			req = req.WithContext(context.WithValue(req.Context(), requestKey{}, state))
			w.Header().Set("X-Request-ID", id)
			started := time.Now()
			status, size, committed := 0, int64(0), false
			wrapped := httpsnoop.Wrap(w, httpsnoop.Hooks{
				WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
					return func(code int) {
						if code >= 200 && !committed {
							status, committed = code, true
						}
						next(code)
					}
				},
				Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
					return func(p []byte) (int, error) {
						if !committed {
							status, committed = 200, true
						}
						n, err := next(p)
						size += int64(n)
						return n, err
					}
				},
				Flush: func(next httpsnoop.FlushFunc) httpsnoop.FlushFunc {
					return func() {
						if !committed {
							status, committed = 200, true
						}
						next()
					}
				},
				Hijack: func(next httpsnoop.HijackFunc) httpsnoop.HijackFunc {
					return func() (net.Conn, *bufio.ReadWriter, error) {
						c, r, err := next()
						if err == nil {
							status, committed = http.StatusSwitchingProtocols, true
						}
						return c, r, err
					}
				},
			})
			defer func() {
				panicValue := recover()
				abort := panicValue == http.ErrAbortHandler
				if panicValue != nil {
					state.mu.Lock()
					if abort {
						state.outcome = "aborted"
					} else {
						state.outcome, state.errorType = "failure", "panic"
					}
					state.mu.Unlock()
					if !abort {
						logger.ErrorContext(req.Context(), "HTTP handler panicked", "event", "application_error", "request_id", id, "error_type", "panic", "error", CleanText(fmt.Sprint(panicValue)), "stack", string(debug.Stack()))
						if !committed {
							http.Error(wrapped, "Internal Server Error", http.StatusInternalServerError)
						} else {
							abort = true
						}
					}
				}
				state.mu.Lock()
				outcome, errorType, actor, appCode := state.outcome, state.errorType, state.actor, state.appCode
				state.mu.Unlock()
				if outcome == "" {
					switch {
					case req.Context().Err() != nil:
						outcome = "cancelled"
					case status >= 500:
						outcome = "failure"
					case status >= 400:
						outcome = "rejected"
					case options.SuccessCode != 0 && appCode != 0 && appCode != options.SuccessCode:
						outcome = "business_rejected"
					default:
						outcome = "success"
					}
				}
				if status == 0 && outcome != "cancelled" && outcome != "aborted" {
					status = 200
				}
				skip := false
				for _, p := range options.HealthPaths {
					if req.URL.Path == p && outcome == "success" {
						skip = true
					}
				}
				if !skip {
					route := req.Pattern
					if route == "" {
						route = "unmatched"
					}
					attrs := []any{"event", "http_access", "request_id", id, "method", req.Method, "route", route, "path", CleanText(req.URL.EscapedPath()), "status", status, "duration_ms", float64(time.Since(started).Microseconds()) / 1000, "response_bytes", size, "outcome", outcome}
					if actor != "" {
						attrs = append(attrs, "actor_id", actor)
					}
					if appCode != 0 {
						attrs = append(attrs, "app_code", appCode)
					}
					if errorType != "" {
						attrs = append(attrs, "error_type", errorType)
					}
					if match := validTraceparent.FindStringSubmatch(req.Header.Get("traceparent")); match != nil && match[1] != strings.Repeat("0", 32) {
						attrs = append(attrs, "trace_id", match[1])
					}
					logger.InfoContext(req.Context(), "HTTP request completed", attrs...)
					if options.Audit != nil && options.Audit(req) {
						attrs[1] = "audit"
						logger.InfoContext(req.Context(), "State-changing request completed", attrs...)
					}
				}
				if abort {
					panic(http.ErrAbortHandler)
				}
			}()
			next.ServeHTTP(wrapped, req)
		})
	}
}
