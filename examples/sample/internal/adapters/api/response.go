package api

import (
	"net/url"
	"strconv"
)

type Response struct {
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`

	NextCursor string           `json:"next_cursor,omitempty"`
	Error      ErrorMessageType `json:"error,omitempty"`
}

func PageParams(query url.Values) (limit int, cursor string) {
	if raw := query.Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}

	return limit, query.Get("cursor")
}
