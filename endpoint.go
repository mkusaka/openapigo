package openapigo

// Endpoint describes an API operation with typed request and response.
// Endpoint is a value type — copies do not share state.
// Package-level Endpoint variables are safe for concurrent use.
type Endpoint[Req, Resp any] struct {
	method       string
	path         string
	successCodes []int
	errors       []ErrorHandler
}

// NewEndpoint creates an Endpoint for the given HTTP method and URL path template.
func NewEndpoint[Req, Resp any](method, path string) Endpoint[Req, Resp] {
	return Endpoint[Req, Resp]{
		method:       method,
		path:         path,
		successCodes: []int{200},
	}
}

// WithSuccessCodes returns a copy with the given status codes treated as success.
func (e Endpoint[Req, Resp]) WithSuccessCodes(codes ...int) Endpoint[Req, Resp] {
	e.successCodes = make([]int, len(codes))
	copy(e.successCodes, codes)
	return e
}

// WithErrors returns a copy with the given error handlers.
func (e Endpoint[Req, Resp]) WithErrors(handlers ...ErrorHandler) Endpoint[Req, Resp] {
	e.errors = make([]ErrorHandler, len(handlers))
	copy(e.errors, handlers)
	return e
}

// Method returns the HTTP method.
func (e Endpoint[Req, Resp]) Method() string { return e.method }

// Path returns the URL path template.
func (e Endpoint[Req, Resp]) Path() string { return e.path }

// isSuccessStatus reports whether the status code is a success code for this endpoint.
// Negative values represent range codes: -2 means 2XX (200-299).
func (e Endpoint[Req, Resp]) isSuccessStatus(code int) bool {
	for _, c := range e.successCodes {
		if c == code {
			return true
		}
		// Negative value = range code (e.g., -2 matches 200-299).
		if c < 0 && code/100 == -c {
			return true
		}
	}
	return false
}
