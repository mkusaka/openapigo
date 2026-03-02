package openapigo

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
)

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
// Passing nil is a no-op (the default http.DefaultClient is retained).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
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
// Passing nil is a no-op (the default codec is retained).
func WithJSONCodec(codec JSONCodec) Option {
	return func(c *Client) {
		if codec != nil {
			c.codec = codec
		}
	}
}

// Do executes an API request described by the endpoint and request value,
// returning the parsed response. It handles parameter serialization, body
// encoding, middleware execution, and response parsing.
func Do[Req, Resp any](ctx context.Context, client *Client, endpoint Endpoint[Req, Resp], req Req) (*Resp, error) {
	resp, _, err := doInternal[Req, Resp](ctx, client, endpoint, req)
	return resp, err
}

// DoSimple executes an endpoint that takes no request parameters.
func DoSimple[Resp any](ctx context.Context, client *Client, endpoint Endpoint[NoRequest, Resp]) (*Resp, error) {
	return Do(ctx, client, endpoint, NoRequest{})
}

// DoRaw executes an endpoint and returns the raw HTTP response.
// The caller is responsible for closing the response body.
func DoRaw[Req any](ctx context.Context, client *Client, endpoint Endpoint[Req, any], req Req) (*http.Response, error) {
	httpReq, err := buildRequest(ctx, client, endpoint.method, endpoint.path, req, client.codec)
	if err != nil {
		return nil, err
	}
	return executeWithMiddleware(client, httpReq)
}

// doInternal is the shared implementation for Do and DoWithResponse.
func doInternal[Req, Resp any](ctx context.Context, client *Client, endpoint Endpoint[Req, Resp], req Req) (*Resp, *http.Response, error) {
	httpReq, err := buildRequest(ctx, client, endpoint.method, endpoint.path, req, client.codec)
	if err != nil {
		return nil, nil, err
	}

	httpResp, err := executeWithMiddleware(client, httpReq)
	if err != nil {
		return nil, nil, err
	}
	defer httpResp.Body.Close()

	// Limit response body to prevent OOM from malicious/large responses.
	// Default: 64 MiB. Can be increased via custom middleware if needed.
	const maxResponseBody = 64 << 20
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBody))
	if err != nil {
		return nil, httpResp, err
	}

	if endpoint.isSuccessStatus(httpResp.StatusCode) {
		return parseSuccess[Resp](client.codec, httpResp, body)
	}
	return nil, httpResp, parseError(endpoint.errors, httpResp, body)
}

// buildRequest creates an http.Request from the endpoint and typed request value.
func buildRequest[Req any](ctx context.Context, client *Client, method, pathTmpl string, req Req, codec JSONCodec) (*http.Request, error) {
	// 1. Build URL with path parameters.
	path := buildPath(pathTmpl, req)
	u, err := url.Parse(client.baseURL + path)
	if err != nil {
		return nil, err
	}

	// 2. Add query parameters.
	buildQuery(u, req)

	// 3. Encode body if present.
	var bodyReader io.Reader
	var contentType string
	meta := parseStructMeta(typeOf[Req]())
	if meta.body != nil {
		bodyReader, contentType, err = encodeBody(meta.body, req, codec)
		if err != nil {
			return nil, err
		}
	}

	// 4. Create HTTP request.
	httpReq, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return nil, err
	}

	// 5. Clone default headers into request (ADR-024: each request gets its own copy).
	for k, v := range client.headers {
		httpReq.Header[k] = append([]string(nil), v...)
	}

	// 6. Set headers from struct tags.
	setHeaders(httpReq.Header, req)

	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}

	return httpReq, nil
}

// encodeBody encodes the body field of the request struct.
func encodeBody[Req any](bodyMeta *fieldMeta, req Req, codec JSONCodec) (io.Reader, string, error) {
	rv := reflectValue(req)
	fv := rv.Field(bodyMeta.index)

	if isZeroValue(fv) {
		return nil, "", nil
	}

	// For now, only JSON bodies are supported.
	data, err := codec.Marshal(fv.Interface())
	if err != nil {
		return nil, "", err
	}
	return bytes.NewReader(data), "application/json", nil
}

// executeWithMiddleware runs the middleware chain and then the HTTP call.
func executeWithMiddleware(client *Client, req *http.Request) (*http.Response, error) {
	// Build the handler chain: middleware[0] wraps middleware[1] wraps ... wraps http.Client.Do
	handler := func(r *http.Request) (*http.Response, error) {
		return client.httpClient.Do(r)
	}
	// Apply middleware in reverse order so the first middleware is outermost.
	for i := len(client.middleware) - 1; i >= 0; i-- {
		mw := client.middleware[i]
		next := handler
		handler = func(r *http.Request) (*http.Response, error) {
			return mw.RoundTrip(r, next)
		}
	}
	return handler(req)
}

// parseSuccess parses a successful response body into the response type.
func parseSuccess[Resp any](codec JSONCodec, httpResp *http.Response, body []byte) (*Resp, *http.Response, error) {
	var resp Resp
	// Check for NoContent response type.
	if _, ok := any(&resp).(*NoContent); ok {
		return &resp, httpResp, nil
	}
	if len(body) == 0 {
		return &resp, httpResp, nil
	}
	if err := codec.Unmarshal(body, &resp); err != nil {
		return nil, httpResp, err
	}
	return &resp, httpResp, nil
}

// parseError matches an error handler and returns a typed error.
func parseError(handlers []ErrorHandler, httpResp *http.Response, body []byte) error {
	// Try exact status match first.
	for _, h := range handlers {
		if h.StatusCode == httpResp.StatusCode {
			return h.Parse(httpResp.StatusCode, httpResp.Header, body)
		}
	}
	// Try status range match (e.g., -4 for 4XX).
	rangeCode := -(httpResp.StatusCode / 100)
	for _, h := range handlers {
		if h.StatusCode == rangeCode {
			return h.Parse(httpResp.StatusCode, httpResp.Header, body)
		}
	}
	// Try default handler (StatusCode == 0).
	for _, h := range handlers {
		if h.StatusCode == 0 {
			return h.Parse(httpResp.StatusCode, httpResp.Header, body)
		}
	}
	// Fallback: generic APIError.
	return &APIError{
		StatusCode: httpResp.StatusCode,
		Status:     httpResp.Status,
		Header:     httpResp.Header,
		Body:       body,
	}
}
