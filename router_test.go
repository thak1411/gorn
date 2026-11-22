package gorn

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMiddlewareIncludesPreflightAnd404(t *testing.T) {
	r := NewRouter()
	calls := 0
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			calls++
			w.Header().Set("X-Request-ID", "12345678")
			next.ServeHTTP(w, req)
		})
	})
	r.SetOptions(&RouterOptions{AllowedOrigins: []string{"https://test.example"}, ExposedHeaders: []string{"X-Request-ID"}})
	r.Get("/test", func(c *Context) { c.SendSuccess() })
	for _, path := range []string{"/test", "/missing"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Origin", "https://test.example")
		r.ServeHTTP(w, req)
		if w.Header().Get("X-Request-ID") == "" {
			t.Fatal("missing middleware")
		}
		if path == "/test" && w.Header().Get("Access-Control-Expose-Headers") != "X-Request-ID" {
			t.Fatal("missing CORS exposure")
		}
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/test", nil)
	req.Header.Set("Origin", "https://test.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	r.ServeHTTP(w, req)
	if calls != 3 || w.Code != 204 {
		t.Fatalf("calls=%d code=%d", calls, w.Code)
	}
}

func TestGracefulShutdownDrainsRequest(t *testing.T) {
	r := NewRouter()
	entered, release := make(chan struct{}), make(chan struct{})
	r.Get("/slow", func(c *Context) { close(entered); <-release; c.SendPlainText(200, "finished") })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.ServeContext(ctx, listener) }()
	response := make(chan string, 1)
	go func() {
		res, err := http.Get("http://" + listener.Addr().String() + "/slow")
		if err != nil {
			response <- err.Error()
			return
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		response <- string(b)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler never entered")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("exited before request drained: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if got := <-response; got != "finished" {
		t.Fatal(got)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not finish")
	}
}
