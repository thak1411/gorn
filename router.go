package gorn

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path"
)

type HttpHandler func(w http.ResponseWriter, r *http.Request)

type Router struct {
	mux           *http.ServeMux
	Handler       map[string]bool
	GetHandler    map[string][]func(c *Context)
	PostHandler   map[string][]func(c *Context)
	PutHandler    map[string][]func(c *Context)
	DeleteHandler map[string][]func(c *Context)
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
	copyHandler(prefix, r.Handler, r.GetHandler, router.GetHandler)
	copyHandler(prefix, r.Handler, r.PostHandler, router.PostHandler)
	copyHandler(prefix, r.Handler, r.PutHandler, router.PutHandler)
	copyHandler(prefix, r.Handler, r.DeleteHandler, router.DeleteHandler)
}

// Regist Get Function to Router
func (r *Router) Get(path string, handler ...func(c *Context)) {
	if len(handler) < 1 {
		return
	}
	r.Handler[path] = true
	r.GetHandler[path] = handler
}

// Regist Post Function to Router
func (r *Router) Post(path string, handler ...func(c *Context)) {
	if len(handler) < 1 {
		return
	}
	r.Handler[path] = true
	r.PostHandler[path] = handler
}

// Regist Put Function to Router
func (r *Router) Put(path string, handler ...func(c *Context)) {
	if len(handler) < 1 {
		return
	}
	r.Handler[path] = true
	r.PutHandler[path] = handler
}

// Regist Delete Function to Router
func (r *Router) Delete(path string, handler ...func(c *Context)) {
	if len(handler) < 1 {
		return
	}
	r.Handler[path] = true
	r.DeleteHandler[path] = handler
}

// Preparing Router
func (r *Router) prepare() {
	for p := range r.Handler {
		getHandler, hasGetHandler := r.GetHandler[p]
		postHandler, hasPostHandler := r.PostHandler[p]
		putHandler, hasPutHandler := r.PutHandler[p]
		deleteHandler, hasDeleteHandler := r.DeleteHandler[p]
		r.mux.HandleFunc(p, func(w http.ResponseWriter, req *http.Request) {
			c := &Context{
				responseWriter: w,
				request:        req,
				ctx:            req.Context(),
			}
			var handler []func(c *Context)
			var ok bool
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
		})
	}
}

// Running Router
func (r *Router) Run(port int) error {
	r.prepare()

	ret := make(chan error, 1)
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	go func() {
		err := http.ListenAndServe(fmt.Sprintf(":%d", port), r.mux)
		ret <- err
	}()

	select {
	case err := <-ret:
		return err
	case <-interrupt:
		return nil
	}
}

// Generate a Gorn Router
func New() *Router {
	return &Router{
		mux:           http.NewServeMux(),
		Handler:       make(map[string]bool),
		GetHandler:    make(map[string][]func(c *Context)),
		PostHandler:   make(map[string][]func(c *Context)),
		PutHandler:    make(map[string][]func(c *Context)),
		DeleteHandler: make(map[string][]func(c *Context)),
	}
}
