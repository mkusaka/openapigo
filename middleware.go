package openapigo

import "net/http"

// Middleware intercepts HTTP requests before they are sent.
// Implementations MUST be safe for concurrent use.
type Middleware interface {
	RoundTrip(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error)
}

// MiddlewareFunc is an adapter to allow the use of ordinary functions as Middleware.
type MiddlewareFunc func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error)

// RoundTrip implements Middleware.
func (f MiddlewareFunc) RoundTrip(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	return f(req, next)
}
