package api

import (
	"errors"
	"net/http"
)

type ErrorMessageType string

// The stable, coarse label a client branches on. The specific reason — when it
// is safe to disclose — travels in Response.Message instead. Nothing derived
// from a driver, query, or connection string appears in either field: a failure
// the client cannot act on is always the one opaque DatabaseOperationFailure,
// so an outage can't leak credentials or topology to whoever is holding the
// connection open.
const (
	NotFoundError            ErrorMessageType = "Not found"
	ValidationFailure        ErrorMessageType = "Validation failed"
	FailedToReadRequestBody  ErrorMessageType = "Failed to read request body"
	RequestBodyTooLarge      ErrorMessageType = "Request body too large"
	DatabaseOperationFailure ErrorMessageType = "Database operation failure"
	InvalidCursor            ErrorMessageType = "Invalid pagination cursor"
)

// DecodeFailure maps a request-body decode error to its status and label: 413
// when the body ran past the server's size cap (middleware.MaxBytes), else 400.
func DecodeFailure(err error) (int, ErrorMessageType) {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return http.StatusRequestEntityTooLarge, RequestBodyTooLarge
	}
	return http.StatusBadRequest, FailedToReadRequestBody
}
