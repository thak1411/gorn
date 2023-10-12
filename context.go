package gorn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
)

type GornContext string

const (
	ContextFinish GornContext = "Finish"
)

type Context struct {
	// HTTP Response Writer
	responseWriter http.ResponseWriter

	// HTTP Request Handler
	request *http.Request

	// Custom Context Value
	ctx context.Context
}

//================================================================================
// HTTP RESPONSE
//================================================================================

// Get Response Writer
func (c *Context) GetResponseWriter() http.ResponseWriter {
	return c.responseWriter
}

// Flagging Context is Finished
func (c *Context) SetContextFinish() {
	c.ctx = context.WithValue(c.ctx, ContextFinish, true)
}

// Check Context is Finished
func (c *Context) IsContextFinish() bool {
	if c.ctx.Value(ContextFinish) == nil {
		return false
	}
	return c.ctx.Value(ContextFinish).(bool)
}

// Send Internal Server Error (500)
func (c *Context) SendInternalServerError() {
	c.SetContextFinish()
	http.Error(c.responseWriter, "internal server error", http.StatusInternalServerError)
}

// Send Bad Request (400)
func (c *Context) SendBadRequest() {
	c.SetContextFinish()
	http.Error(c.responseWriter, "bad request", http.StatusBadRequest)
}

// Send Not Authorized (401)
func (c *Context) SendNotAuthorized() {
	c.SetContextFinish()
	http.Error(c.responseWriter, "not authorized", http.StatusUnauthorized)
}

// Send Method Not Allowed (405)
func (c *Context) SendMethodNotAllowed() {
	c.SetContextFinish()
	http.Error(c.responseWriter, "method not allowed", http.StatusMethodNotAllowed)
}

// Send Success (200)
func (c *Context) SendSuccess() {
	c.SetContextFinish()
	c.responseWriter.WriteHeader(http.StatusOK)
}

// Send Plain Text
func (c *Context) SendPlainText(status int, text string) {
	c.SetContextFinish()
	c.responseWriter.Header().Set("Content-Type", "text/plain")
	c.responseWriter.WriteHeader(status)
	c.responseWriter.Write([]byte(text))
}

// Send HTML
func (c *Context) SendHTML(status int, html string) {
	c.SetContextFinish()
	c.responseWriter.Header().Set("Content-Type", "text/html")
	c.responseWriter.WriteHeader(status)
	c.responseWriter.Write([]byte(html))
}

// Send File
func (c *Context) SendFile(status int, filename string) {
	c.SetContextFinish()
	http.ServeFile(c.responseWriter, c.request, filename)
}

// Send Json Template
func (c *Context) SendJson(status int, v interface{}) {
	c.SetContextFinish()
	c.responseWriter.Header().Set("Content-Type", "application/json")
	c.responseWriter.WriteHeader(status)
	if err := json.NewEncoder(c.responseWriter).Encode(v); err != nil {
		c.SendInternalServerError()
	}
}

//================================================================================
// BODY & PARAMS BINDING
//================================================================================

// Get Request Object
func (c *Context) GetRequest() *http.Request {
	return c.request
}

// Binding Body to Json Object
// If Body Can't Decode to Json Object, Send Bad Request (400) & Return Error
func (c *Context) BindJsonBody(obj interface{}) error {
	decoder := json.NewDecoder(c.request.Body)
	if err := decoder.Decode(obj); err != nil {
		c.SendBadRequest()
		return err
	}
	return nil
}

// Get Params Value From key
// If Key Not Found, Return default Value
func (c *Context) GetParam(key, defaultValue string) string {
	if c.request.URL.Query().Has(key) {
		return c.request.URL.Query().Get(key)
	} else {
		return defaultValue
	}
}

// Get Params integer Value From key
// If Key Not Found, Return default Value
func (c *Context) GetParamInt(key string, defaultValue int) int {
	str := c.request.URL.Query().Get(key)
	i, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return defaultValue
	}
	return int(i)
}

// Get Params 64bit integer Value From key
// If Key Not Found, Return default Value
func (c *Context) GetParamInt64(key string, defaultValue int64) int64 {
	str := c.request.URL.Query().Get(key)
	i, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return defaultValue
	}
	return i
}

// Get Params Bool Value From key
// If Key Not Found, Return default Value
func (c *Context) GetParamBool(key string, defaultValue bool) bool {
	str := c.request.URL.Query().Get(key)
	b, err := strconv.ParseBool(str)
	if err != nil {
		return defaultValue
	}
	return b
}

//================================================================================
// COOKIES
//================================================================================

// Set Browser Cookie
func (c *Context) SetCookie(cookie *http.Cookie) {
	http.SetCookie(c.responseWriter, cookie)
}

// Get Browser Cookie
func (c *Context) GetCookie(sessionName string) (*http.Cookie, error) {
	return c.request.Cookie(sessionName)
}

//================================================================================
// HEADERS
//================================================================================

// Get Header
func (c *Context) GetHeader(key string) string {
	return c.request.Header.Get(key)
}

// Set Header
func (c *Context) SetHeader(key, value string) {
	c.responseWriter.Header().Set(key, value)
}

// Add Header
func (c *Context) AddHeader(key, value string) {
	c.responseWriter.Header().Add(key, value)
}

// Get Host
func (c *Context) GetHost() string {
	return c.request.Host
}

//================================================================================
// CONTEXT
//================================================================================

// Get Context
func (c *Context) GetContext() context.Context {
	return c.ctx
}

// Regist Value
func (c *Context) SetValue(key string, value interface{}) {
	c.ctx = context.WithValue(c.ctx, GornContext(key), value)
}

// Get Value
func (c *Context) GetValue(key string) interface{} {
	return c.ctx.Value(GornContext(key))
}

//================================================================================
// VALIDATION
//================================================================================

// Assertion
// If Assertion is Failed, Send Bad Request (400) & Return Error
func (c *Context) Assert(condition bool, message string) error {
	if condition {
		return nil
	}
	c.SendBadRequest()
	return errors.New(message)
}

// Assert From Integer Close Range
// If Assertion is Failed, Send Bad Request (400) & Return Error
func (c *Context) AssertIntRange(i int, min, max int) error {
	return c.Assert(i >= min && i <= max, "integer is not valid")
}

// Assert From 64Bit Integer Close Range
// If Assertion is Failed, Send Bad Request (400) & Return Error
func (c *Context) AssertInt64Range(i int64, min, max int64) error {
	return c.Assert(i >= min && i <= max, "64Bit integer is not valid")
}

// Assert From String Length Closed Range
// If Assertion is Failed, Send Bad Request (400) & Return Error
func (c *Context) AssertStrLen(str string, min, max int) error {
	return c.Assert(len(str) >= min && len(str) <= max, "string length is not valid")
}

// Assert From String Regex
// If Assertion is Failed, Send Bad Request (400) & Return Error
func (c *Context) AssertStrRegex(str string, regex string) error {
	ok, err := regexp.MatchString(regex, str)
	return c.Assert(err == nil && ok, "string is not valid")
}
