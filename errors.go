package gola

import (
	"errors"
	"net/http"
)

var ErrResponseCommitted = errors.New("gola: response already committed")
var ErrInvalidStatus = errors.New("gola: invalid final response status")
var ErrInvalidRedirect = errors.New("gola: invalid redirect status or location")

func (c *Context) Error(err error) {
	if err != nil {
		c.errors = append(c.errors, err)
	}
}
func (c *Context) Errors() []error { return append([]error(nil), c.errors...) }
func (c *Context) Fail(err error) {
	if err == nil {
		return
	}
	c.Error(err)
	c.Abort()
	if !c.Writer.Written() {
		c.engine.errorHandler(c, err)
	}
}
func defaultErrorHandler(c *Context, _ error) {
	if err := c.String(http.StatusInternalServerError, "Internal Server Error\n"); err != nil {
		c.Error(err)
	}
}
