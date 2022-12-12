package gorn

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path"
)

type Router struct {
	mux           *http.ServeMux
	handler       map[string]bool
	getHandler    map[string][]func(c *Context)
	postHandler   map[string][]func(c *Context)
	putHandler    map[string][]func(c *Context)
	deleteHandler map[string][]func(c *Context)
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

// Preparing Router
func (r *Router) prepare() {
	for p := range r.handler {
		getHandler, hasGetHandler := r.getHandler[p]
		postHandler, hasPostHandler := r.postHandler[p]
		putHandler, hasPutHandler := r.putHandler[p]
		deleteHandler, hasDeleteHandler := r.deleteHandler[p]
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
		handler:       make(map[string]bool),
		getHandler:    make(map[string][]func(c *Context)),
		postHandler:   make(map[string][]func(c *Context)),
		putHandler:    make(map[string][]func(c *Context)),
		deleteHandler: make(map[string][]func(c *Context)),
	}
}
