package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func records(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var result []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatal(err)
		}
		result = append(result, v)
	}
	return result
}

func TestRequestOutcomeAndErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name           string
		err            error
		cancel         bool
		level, outcome string
	}{
		{"client_cancel", fmt.Errorf("query: %w", context.Canceled), true, "INFO", "cancelled"},
		{"deadline", context.DeadlineExceeded, false, "ERROR", "failure"},
		{"unexpected_cancel", context.Canceled, false, "ERROR", "failure"},
		{"real_error_racing_cancel", errors.New("database unavailable"), true, "ERROR", "failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			logger := NewLogger(&out, "test", "test", "v1")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			h := HTTP(HTTPOptions{Logger: logger})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ReportError(r.Context(), logger, "query", tc.err)
				w.WriteHeader(500)
			}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/resource?token=hidden", nil).WithContext(ctx))
			r := records(t, &out)
			if len(r) != 2 || r[0]["level"] != tc.level || r[1]["outcome"] != tc.outcome {
				t.Fatalf("unexpected records: %v", r)
			}
			if strings.Contains(out.String(), "hidden") {
				t.Fatal("query leaked")
			}
			if r[0]["request_id"] != w.Header().Get("X-Request-ID") {
				t.Fatal("request ID mismatch")
			}
		})
	}
}

func TestHTTP404PanicProbeAndBusinessResult(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		count  int
	}{{"missing", 404, 1}, {"panic", 500, 2}, {"healthz", 200, 0}, {"business", 200, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			mux := http.NewServeMux()
			mux.HandleFunc("/panic", func(http.ResponseWriter, *http.Request) { panic("boom") })
			mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
			mux.HandleFunc("/business", func(w http.ResponseWriter, r *http.Request) {
				SetResult(r.Context(), 8001)
				SetActor(r.Context(), "42")
				w.WriteHeader(200)
			})
			h := HTTP(HTTPOptions{Logger: NewLogger(&out, "test", "test", "v1"), HealthPaths: []string{"/healthz"}, SuccessCode: 8000})(mux)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/"+tc.name, nil))
			r := records(t, &out)
			if w.Code != tc.status || len(r) != tc.count {
				t.Fatalf("status=%d records=%v", w.Code, r)
			}
			if tc.name == "business" && (r[0]["outcome"] != "business_rejected" || r[0]["actor_id"] != "42") {
				t.Fatalf("%v", r)
			}
		})
	}
}

func TestHTTPPreservesStreamingAndAbort(t *testing.T) {
	var out bytes.Buffer
	h := HTTP(HTTPOptions{Logger: NewLogger(&out, "test", "test", "v1")})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Hijacker); ok {
			t.Fatal("added unsupported Hijacker")
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("event: hello\n\n"))
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Fatal("transport abort swallowed")
		}
		r := records(t, &out)
		if len(r) != 1 || r[0]["outcome"] != "aborted" {
			t.Fatalf("%v", r)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/events", nil))
}

func TestRedaction(t *testing.T) {
	var out bytes.Buffer
	l := NewLogger(&out, "test", "test", "v1")
	l.Info("failed https://user:pass@example.com/path?secret=hidden Bearer abcdef password=qwerty", slog.String("cookie", "session=abcdef"), slog.Group("nested", slog.String("access_token", "secretvalue")))
	for _, secret := range []string{"user:pass", "hidden", "abcdef", "qwerty", "secretvalue"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("secret %s leaked", secret)
		}
	}
}
