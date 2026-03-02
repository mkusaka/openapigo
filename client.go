package openapigo

import "net/http"

// Client holds configuration for making API requests.
// A Client is safe for concurrent use after construction (all fields are read-only).
// There are no setter methods — all configuration is via NewClient options.
type Client struct {
	baseURL    string
	httpClient *http.Client
	middleware []Middleware
	headers    http.Header
	codec      JSONCodec
}

// Option configures a Client.
type Option func(*Client)

// NewClient creates a Client with the given options.
func NewClient(opts ...Option) *Client {
	c := &Client{
		httpClient: http.DefaultClient,
		headers:    make(http.Header),
		codec:      defaultCodec{},
	}
	for _, opt := range opts {
		opt(c)
	}
	// Defensive clone of headers so the caller cannot mutate them after construction.
	h := make(http.Header, len(c.headers))
	for k, v := range c.headers {
		h[k] = append([]string(nil), v...)
	}
	c.headers = h
	// Defensive clone of middleware slice.
	if len(c.middleware) > 0 {
		mw := make([]Middleware, len(c.middleware))
		copy(mw, c.middleware)
		c.middleware = mw
	}
	return c
}

// WithBaseURL sets the base URL for all requests.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = u }
}

// WithHTTPClient sets the underlying http.Client.
// The caller must not modify the http.Client after passing it here.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithDefaultHeader adds a default header sent on every request.
func WithDefaultHeader(key, value string) Option {
	return func(c *Client) { c.headers.Add(key, value) }
}

// WithMiddleware appends middleware to the request pipeline.
// Middleware implementations MUST be safe for concurrent use.
func WithMiddleware(mw ...Middleware) Option {
	return func(c *Client) { c.middleware = append(c.middleware, mw...) }
}

// WithJSONCodec sets a custom JSON encoder/decoder.
func WithJSONCodec(codec JSONCodec) Option {
	return func(c *Client) { c.codec = codec }
}
