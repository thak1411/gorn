package gorn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
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
// If Key Not Found, Return Empty String
func (c *Context) GetParam(key string) string {
	return c.request.URL.Query().Get(key)
}

// Get Params integer Value From key
// If Key Not Found, Return Empty String
func (c *Context) GetParamInt(key string, defaultValue int) int {
	str := c.request.URL.Query().Get(key)
	i, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return defaultValue
	}
	return int(i)
}

// Get Params 64bit integer Value From key
// If Key Not Found, Return Empty String
func (c *Context) GetParamInt64(key string, defaultValue int64) int64 {
	str := c.request.URL.Query().Get(key)
	i, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return defaultValue
	}
	return i
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
