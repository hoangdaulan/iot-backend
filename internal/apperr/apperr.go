// Package apperr carries an HTTP status and a client-facing message with service errors. Any
// other error is reported to clients as a 500.
package apperr

import (
	"errors"
	"net/http"
)

type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func New(status int, message string) *Error { return &Error{Status: status, Message: message} }

func BadRequest(message string) *Error   { return New(http.StatusBadRequest, message) }
func Unauthorized(message string) *Error { return New(http.StatusUnauthorized, message) }
func Forbidden(message string) *Error    { return New(http.StatusForbidden, message) }
func NotFound(message string) *Error     { return New(http.StatusNotFound, message) }
func Conflict(message string) *Error     { return New(http.StatusConflict, message) }

// As returns the *Error in err's chain, if any.
func As(err error) (*Error, bool) {
	var appErr *Error
	ok := errors.As(err, &appErr)
	return appErr, ok
}
