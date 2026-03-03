package openapigo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"strings"
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
	handler    func(*http.Request) (*http.Response, error) // pre-built middleware chain
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
	// Pre-build the middleware chain once at construction time.
	c.handler = buildMiddlewareChain(c.httpClient, c.middleware)
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
	resp, err := executeWithMiddleware(client, httpReq)
	if err != nil {
		if httpReq.Body != nil {
			httpReq.Body.Close()
		}
		return nil, err
	}
	return resp, nil
}

// doInternal is the shared implementation for Do and DoWithResponse.
func doInternal[Req, Resp any](ctx context.Context, client *Client, endpoint Endpoint[Req, Resp], req Req) (*Resp, *http.Response, error) {
	httpReq, err := buildRequest(ctx, client, endpoint.method, endpoint.path, req, client.codec)
	if err != nil {
		return nil, nil, err
	}

	httpResp, err := executeWithMiddleware(client, httpReq)
	if err != nil {
		// Close the request body to unblock any goroutine writing to a pipe
		// (e.g., multipart/form-data streaming via io.Pipe).
		if httpReq.Body != nil {
			httpReq.Body.Close()
		}
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

	// 7. Set cookie parameters.
	setCookies(httpReq, req)

	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}

	return httpReq, nil
}

// encodeBody encodes the body field of the request struct.
// The media type is determined by the body tag: body:"application/json", body:"multipart/form-data", etc.
func encodeBody[Req any](bodyMeta *fieldMeta, req Req, codec JSONCodec) (io.Reader, string, error) {
	rv := reflectValue(req)
	fv := rv.Field(bodyMeta.index)

	// Only skip nil values (optional/pointer body). Non-nil zero values
	// (0, false, empty string, zero-value structs) are valid and must be encoded.
	if isNilValue(fv) {
		return nil, "", nil
	}

	switch bodyMeta.name {
	case "application/json", "":
		data, err := codec.Marshal(fv.Interface())
		if err != nil {
			return nil, "", err
		}
		return bytes.NewReader(data), "application/json", nil
	case "application/octet-stream":
		return encodeBinaryBody(fv)
	case "multipart/form-data":
		return encodeMultipartBody(fv)
	case "application/x-www-form-urlencoded":
		return encodeFormURLEncoded(fv)
	default:
		// Unknown media type — attempt JSON encoding.
		data, err := codec.Marshal(fv.Interface())
		if err != nil {
			return nil, "", err
		}
		return bytes.NewReader(data), bodyMeta.name, nil
	}
}

// encodeBinaryBody encodes a []byte or io.Reader body field.
// Handles both direct values and pointer types (e.g., *[]byte for optional bodies).
func encodeBinaryBody(fv reflect.Value) (io.Reader, string, error) {
	if fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			return nil, "", nil
		}
		// Check io.Reader before Elem() — pointer receiver methods (e.g., *bytes.Buffer) are lost on dereference.
		if r, ok := fv.Interface().(io.Reader); ok {
			return r, "application/octet-stream", nil
		}
		fv = fv.Elem()
	}
	iface := fv.Interface()
	if r, ok := iface.(io.Reader); ok {
		return r, "application/octet-stream", nil
	}
	if b, ok := iface.([]byte); ok {
		return bytes.NewReader(b), "application/octet-stream", nil
	}
	return nil, "", fmt.Errorf("binary body must be []byte or io.Reader, got %T", iface)
}

// encodeFormURLEncoded encodes a struct as application/x-www-form-urlencoded.
// Fields tagged with json:"name" become form values; json:"-" fields are skipped.
func encodeFormURLEncoded(fv reflect.Value) (io.Reader, string, error) {
	if fv.Kind() == reflect.Ptr {
		fv = fv.Elem()
	}
	if fv.Kind() != reflect.Struct {
		return nil, "", fmt.Errorf("form body must be a struct, got %s", fv.Kind())
	}
	vals := url.Values{}
	t := fv.Type()
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		fieldVal := fv.Field(i)
		if isNilValue(fieldVal) {
			continue
		}
		name := field.Tag.Get("json")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		if idx := strings.IndexByte(name, ','); idx != -1 {
			name = name[:idx]
		}
		if fieldVal.Kind() == reflect.Ptr {
			if fieldVal.IsNil() {
				continue
			}
			fieldVal = fieldVal.Elem()
		}
		vals.Set(name, fmt.Sprintf("%v", fieldVal.Interface()))
	}
	return strings.NewReader(vals.Encode()), "application/x-www-form-urlencoded", nil
}

// encodeMultipartBody encodes a struct as multipart/form-data.
// Fields tagged with json:"name" become form fields; json:"-" fields are skipped.
// Fields of type []byte or io.Reader become file parts.
// Uses io.Pipe for streaming to avoid buffering entire payloads in memory.
func encodeMultipartBody(fv reflect.Value) (io.Reader, string, error) {
	if fv.Kind() == reflect.Ptr {
		fv = fv.Elem()
	}
	if fv.Kind() != reflect.Struct {
		return nil, "", fmt.Errorf("multipart body must be a struct, got %s", fv.Kind())
	}

	pr, pw := io.Pipe()
	w := multipart.NewWriter(pw)
	contentType := w.FormDataContentType()

	go func() {
		err := writeMultipartFields(w, fv)
		if closeErr := w.Close(); err == nil {
			err = closeErr
		}
		pw.CloseWithError(err)
	}()

	return pr, contentType, nil
}

// writeMultipartFields writes struct fields to a multipart writer.
func writeMultipartFields(w *multipart.Writer, fv reflect.Value) error {
	t := fv.Type()
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		fieldVal := fv.Field(i)
		if isNilValue(fieldVal) {
			continue
		}

		name := field.Tag.Get("json")
		if name == "-" {
			continue // skip fields explicitly excluded via json:"-"
		}
		if name == "" {
			name = field.Name
		}
		if idx := strings.IndexByte(name, ','); idx != -1 {
			name = name[:idx]
		}

		// Check io.Reader on pointer BEFORE dereferencing — pointer receiver methods
		// (e.g., *bytes.Buffer) are lost when calling Elem().
		if fieldVal.Kind() == reflect.Ptr {
			if fieldVal.IsNil() {
				continue
			}
			if r, ok := fieldVal.Interface().(io.Reader); ok {
				part, err := w.CreateFormFile(name, name)
				if err != nil {
					return err
				}
				if _, err := io.Copy(part, r); err != nil {
					return err
				}
				continue
			}
			fieldVal = fieldVal.Elem()
		}

		// Check if the field is a file ([]byte or io.Reader).
		iface := fieldVal.Interface()
		if b, ok := iface.([]byte); ok {
			part, err := w.CreateFormFile(name, name)
			if err != nil {
				return err
			}
			if _, err := part.Write(b); err != nil {
				return err
			}
		} else if r, ok := iface.(io.Reader); ok {
			part, err := w.CreateFormFile(name, name)
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, r); err != nil {
				return err
			}
		} else if fieldVal.Kind() == reflect.Slice {
			for j := range fieldVal.Len() {
				val := fmt.Sprintf("%v", fieldVal.Index(j).Interface())
				if err := w.WriteField(name, val); err != nil {
					return err
				}
			}
		} else {
			if err := w.WriteField(name, fmt.Sprintf("%v", iface)); err != nil {
				return err
			}
		}
	}
	return nil
}

// executeWithMiddleware runs the pre-built middleware chain.
func executeWithMiddleware(client *Client, req *http.Request) (*http.Response, error) {
	return client.handler(req)
}

// buildMiddlewareChain creates a handler function with the middleware chain applied.
// Called once at Client construction time.
func buildMiddlewareChain(httpClient *http.Client, middleware []Middleware) func(*http.Request) (*http.Response, error) {
	handler := func(r *http.Request) (*http.Response, error) {
		return httpClient.Do(r)
	}
	for i := len(middleware) - 1; i >= 0; i-- {
		mw := middleware[i]
		next := handler
		handler = func(r *http.Request) (*http.Response, error) {
			return mw.RoundTrip(r, next)
		}
	}
	return handler
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
		if h.StatusCode == httpResp.StatusCode && h.Parse != nil {
			return h.Parse(httpResp.StatusCode, httpResp.Header, body)
		}
	}
	// Try status range match (e.g., -4 for 4XX).
	rangeCode := -(httpResp.StatusCode / 100)
	for _, h := range handlers {
		if h.StatusCode == rangeCode && h.Parse != nil {
			return h.Parse(httpResp.StatusCode, httpResp.Header, body)
		}
	}
	// Try default handler (StatusCode == 0).
	for _, h := range handlers {
		if h.StatusCode == 0 && h.Parse != nil {
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
