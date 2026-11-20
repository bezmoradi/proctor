package api

import (
	"errors"
	"net/http"
)

type ErrorMessageType string

const (
	NotFoundError            ErrorMessageType = "Not found"
	ValidationFailure        ErrorMessageType = "Validation failed"
	FailedToReadRequestBody  ErrorMessageType = "Failed to read request body"
	RequestBodyTooLarge      ErrorMessageType = "Request body too large"
	DatabaseOperationFailure ErrorMessageType = "Database operation failure"
	InvalidCursor            ErrorMessageType = "Invalid pagination cursor"

	EventPublishFailure ErrorMessageType = "Event publish failure"
)

func DecodeFailure(err error) (int, ErrorMessageType) {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return http.StatusRequestEntityTooLarge, RequestBodyTooLarge
	}
	return http.StatusBadRequest, FailedToReadRequestBody
}
