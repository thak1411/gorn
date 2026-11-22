package gorn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Router struct {
	mux           *http.ServeMux
	handler       map[string]bool
	getHandler    map[string][]func(c *Context)
	postHandler   map[string][]func(c *Context)
	putHandler    map[string][]func(c *Context)
	deleteHandler map[string][]func(c *Context)
	anyHandler    map[string][]func(c *Context)
	options       *RouterOptions
	prepareOnce   sync.Once
	middlewares   []func(http.Handler) http.Handler
	prepared      http.Handler
}

type RouterOptions struct {
	AllowedOrigins      []string
	AllowedMethods      []string
	AllowedHeaders      []string
	ExposedHeaders      []string
	MaxAge              int
	AllowCredentials    bool
	AllowPrivateNetwork bool
	// ResponseObserver can record application result codes without reading bodies.
	ResponseObserver func(*Context, int, interface{})
}

// copy handler
func copyHandler(
	prefix string,
	rootHandler map[string]bool,
	destHandler map[string][]func(c *Context),
	srcHandler map[string][]func(c *Context),
) {
	for p, handler := range srcHandler {
		newPath := path.Join(prefix, p)
		rootHandler[newPath] = true
		destHandler[newPath] = handler
	}
}

// Extends Router
func (r *Router) Extends(prefix string, router *Router) {
	prefix = "/" + prefix
	copyHandler(prefix, r.handler, r.getHandler, router.getHandler)
	copyHandler(prefix, r.handler, r.postHandler, router.postHandler)
	copyHandler(prefix, r.handler, r.putHandler, router.putHandler)
	copyHandler(prefix, r.handler, r.deleteHandler, router.deleteHandler)
	copyHandler(prefix, r.handler, r.anyHandler, router.anyHandler)
}

// Regist Get Function to Router
func (r *Router) Get(path string, handler ...func(c *Context)) {
	if len(handler) < 1 {
		return
	}
	r.handler[path] = true
	r.getHandler[path] = handler
}

// Regist Post Function to Router
func (r *Router) Post(path string, handler ...func(c *Context)) {
	if len(handler) < 1 {
		return
	}
	r.handler[path] = true
	r.postHandler[path] = handler
}

// Regist Put Function to Router
func (r *Router) Put(path string, handler ...func(c *Context)) {
	if len(handler) < 1 {
		return
	}
	r.handler[path] = true
	r.putHandler[path] = handler
}

// Regist Delete Function to Router
func (r *Router) Delete(path string, handler ...func(c *Context)) {
	if len(handler) < 1 {
		return
	}
	r.handler[path] = true
	r.deleteHandler[path] = handler
}

// Regist Any Function to Router
func (r *Router) Any(path string, handler ...func(c *Context)) {
	if len(handler) < 1 {
		return
	}
	r.handler[path] = true
	r.anyHandler[path] = handler
}

// preparing options
func prepareOptions(options *RouterOptions) *RouterOptions {
	if options == nil {
		options = &RouterOptions{}
	}
	if len(options.AllowedOrigins) == 0 {
		options.AllowedOrigins = []string{"*"}
	}
	if len(options.AllowedMethods) == 0 {
		options.AllowedMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions}
	}
	return options
}

// Set Router Options
func (r *Router) SetOptions(options *RouterOptions) {
	r.options = prepareOptions(options)
}

// check is allowed origin
func (r *Router) checkOrigin(origin string) bool {
	if len(r.options.AllowedOrigins) == 0 {
		return true
	}
	if r.options.AllowedOrigins[0] == "*" {
		return true
	}
	for _, o := range r.options.AllowedOrigins {
		if o == origin {
			return true
		}
	}
	return false
}

// check is allowed method
func (r *Router) checkMethod(method string) bool {
	if len(r.options.AllowedMethods) == 0 {
		return true
	}
	method = strings.ToUpper(method)
	if method == http.MethodOptions {
		return true
	}
	for _, m := range r.options.AllowedMethods {
		if m == method {
			return true
		}
	}
	return false
}

// check is allowed header
func (r *Router) checkHeader(headers []string) bool {
	if len(r.options.AllowedHeaders) == 0 {
		return true
	}
	if r.options.AllowedHeaders[0] == "*" {
		return true
	}
	for _, header := range headers {
		header = http.CanonicalHeaderKey(header)
		flag := false
		for _, h := range r.options.AllowedHeaders {
			if h == header {
				flag = true
				break
			}
		}
		if !flag {
			return false
		}
	}
	return true
}

// parsing header list
func parseHeaderList(headerList string) []string {
	n := len(headerList)
	h := make([]byte, 0, n)
	toLower := byte('a' - 'A')
	upper := true
	t := 0
	for i := 0; i < n; i++ {
		if headerList[i] == ',' {
			t++
		}
	}
	headers := make([]string, 0, t)
	for i := 0; i < n; i++ {
		b := headerList[i]
		switch {
		case b >= 'a' && b <= 'z':
			if upper {
				h = append(h, b-toLower)
			} else {
				h = append(h, b)
			}
		case b >= 'A' && b <= 'Z':
			if !upper {
				h = append(h, b+toLower)
			} else {
				h = append(h, b)
			}
		case b == '-' || b == '_' || b == '.' || (b >= '0' && b <= '9'):
			h = append(h, b)
		}

		if b == ' ' || b == ',' || i == n-1 {
			if len(h) > 0 {
				// Flush the found header
				headers = append(headers, string(h))
				h = h[:0]
				upper = true
			}
		} else {
			upper = b == '-' || b == '_'
		}
	}
	return headers
}

// Preparing Router
func (r *Router) prepare() {
	for p := range r.handler {
		getHandler, hasGetHandler := r.getHandler[p]
		postHandler, hasPostHandler := r.postHandler[p]
		putHandler, hasPutHandler := r.putHandler[p]
		deleteHandler, hasDeleteHandler := r.deleteHandler[p]
		anyHandler, hasAnyHandler := r.anyHandler[p]
		r.mux.HandleFunc(p, func(w http.ResponseWriter, req *http.Request) {
			c := &Context{
				responseWriter:   w,
				request:          req,
				ctx:              req.Context(),
				responseObserver: r.options.ResponseObserver,
			}
			if req.Method == http.MethodOptions && c.GetHeader("Access-Control-Request-Method") != "" {
				r.preFlight(c)
			} else {
				r.actualRequest(c)
				var handler []func(c *Context)
				var ok bool
				handler, ok = anyHandler, hasAnyHandler
				if !ok {
					switch req.Method {
					case http.MethodGet:
						handler, ok = getHandler, hasGetHandler
					case http.MethodPost:
						handler, ok = postHandler, hasPostHandler
					case http.MethodPut:
						handler, ok = putHandler, hasPutHandler
					case http.MethodDelete:
						handler, ok = deleteHandler, hasDeleteHandler
					default:
						ok = false
					}
				}
				if !ok {
					c.SendMethodNotAllowed()
					return
				}
				for _, h := range handler {
					if c.IsContextFinish() {
						break
					}
					h(c)
				}
			}
		})
	}
}

// pre-flight CORS requests
func (r *Router) preFlight(c *Context) {
	origin := c.GetHeader("Origin")

	// CORS OPTION METHODS
	c.AddHeader("Vary", "Origin")
	c.AddHeader("Vary", "Access-Control-Request-Method")
	c.AddHeader("Vary", "Access-Control-Request-Headers")
	if r.options.AllowPrivateNetwork {
		c.AddHeader("Vary", "Access-Control-Request-Private-Network")
	}
	if !r.checkOrigin(c.GetHeader("Origin")) {
		c.SetContextFinish()
		return
	}
	if !r.checkMethod(c.GetHeader("Access-Control-Request-Method")) {
		c.SetContextFinish()
		return
	}
	headers := parseHeaderList(c.GetHeader("Access-Control-Request-Headers"))
	if !r.checkHeader(headers) {
		c.SetContextFinish()
		return
	}
	c.SetHeader("Access-Control-Allow-Methods", c.GetHeader("Access-Control-Request-Method"))
	if len(headers) > 0 {
		c.SetHeader("Access-Control-Allow-Headers", strings.Join(headers, ","))
	}
	c.SetHeader("Access-Control-Allow-Origin", origin)
	if r.options.AllowCredentials {
		c.SetHeader("Access-Control-Allow-Credentials", "true")
	}
	if r.options.AllowPrivateNetwork && c.GetHeader("Access-Control-Request-Private-Network") == "true" {
		c.SetHeader("Access-Control-Allow-Private-Network", "true")
	}
	if r.options.MaxAge > 0 {
		c.SetHeader("Access-Control-Max-Age", strconv.Itoa(r.options.MaxAge))
	}
	c.responseWriter.WriteHeader(http.StatusNoContent)
}

// handle cors rquests
func (r *Router) actualRequest(c *Context) {
	origin := c.GetHeader("Origin")

	c.AddHeader("Vary", "Origin")
	if !r.checkOrigin(origin) {
		return
	}
	if !r.checkMethod(c.request.Method) {
		return
	}
	c.SetHeader("Access-Control-Allow-Origin", origin)
	if len(r.options.ExposedHeaders) > 0 {
		c.SetHeader("Access-Control-Expose-Headers", strings.Join(r.options.ExposedHeaders, ", "))
	}
	if r.options.AllowCredentials {
		c.SetHeader("Access-Control-Allow-Credentials", "true")
	}
}

// Use installs standard net/http middleware, in outermost-first order.
// Register routes and middleware before first calling Handler or ServeHTTP.
func (r *Router) Use(middleware ...func(http.Handler) http.Handler) {
	r.middlewares = append(r.middlewares, middleware...)
}

// Handler exposes the router for httptest, custom servers, and instrumentation.
// Middleware surrounds the mux, including its 404, redirect and OPTIONS responses.
func (r *Router) Handler() http.Handler {
	r.prepareOnce.Do(func() {
		r.prepare()
		r.prepared = r.mux
		for i := len(r.middlewares) - 1; i >= 0; i-- {
			r.prepared = r.middlewares[i](r.prepared)
		}
	})
	return r.prepared
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.Handler().ServeHTTP(w, req)
}

// Run serves until SIGINT or SIGTERM, allowing active requests to finish.
func (r *Router) Run(port int) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return r.RunContext(ctx, port)
}

func (r *Router) RunContext(ctx context.Context, port int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	return r.ServeContext(ctx, listener)
}

// ServeContext takes ownership of listener. Cancellation stops accepting requests
// and drains active requests for up to 25 seconds. No write timeout is imposed on
// streaming responses. Hijacked connections remain the application's responsibility.
func (r *Router) ServeContext(ctx context.Context, listener net.Listener) error {
	server := &http.Server{Handler: r.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		drain, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if err := server.Shutdown(drain); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}

// Generate a Gorn Router
func NewRouter() *Router {
	options := &RouterOptions{}
	return &Router{
		mux:           http.NewServeMux(),
		handler:       make(map[string]bool),
		getHandler:    make(map[string][]func(c *Context)),
		postHandler:   make(map[string][]func(c *Context)),
		putHandler:    make(map[string][]func(c *Context)),
		deleteHandler: make(map[string][]func(c *Context)),
		anyHandler:    make(map[string][]func(c *Context)),
		options:       prepareOptions(options),
	}
}
