package openapigo

import (
	"fmt"
	"net/http"
)

// APIError represents an HTTP error response from an API.
type APIError struct {
	// StatusCode is the HTTP status code.
	StatusCode int
	// Status is the HTTP status text (e.g., "404 Not Found").
	Status string
	// Header contains the response headers.
	Header http.Header
	// Body is the raw response body (may be truncated).
	Body []byte
}

func (e *APIError) Error() string {
	// Do not include the response body in the error string. The body may contain
	// sensitive data (tokens, PII) that would leak into logs. Callers can access
	// the raw body via e.Body when needed.
	return fmt.Sprintf("API error %s", e.Status)
}

// ErrorHandler parses an error response body into a typed error.
// It is registered on an Endpoint via WithErrors.
type ErrorHandler struct {
	// StatusCode is the HTTP status code this handler matches.
	// Use 0 for the default handler.
	// Negative values represent status code ranges: -4 for 4XX, -5 for 5XX.
	StatusCode int
	// Parse creates a typed error from the response.
	Parse func(statusCode int, header http.Header, body []byte) error
}

// NoRequest is used as the Req type parameter for endpoints that take no request body or parameters.
type NoRequest struct{}

// NoContent is used as the Resp type parameter for endpoints that return no response body (e.g., 204).
type NoContent struct{}
