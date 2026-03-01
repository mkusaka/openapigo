# ADR-017: Response Handling and Error Types

## Status

Accepted

## Date

2026-03-01

## Context

OpenAPI defines responses per HTTP status code, each potentially with different schemas:

```yaml
responses:
  "200":
    description: Successful response
    content:
      application/json:
        schema: { $ref: '#/components/schemas/Pet' }
  "400":
    description: Validation error
    content:
      application/json:
        schema: { $ref: '#/components/schemas/ValidationError' }
  "404":
    description: Resource not found
    content:
      application/json:
        schema: { $ref: '#/components/schemas/NotFoundError' }
  default:
    description: Unexpected error
    content:
      application/json:
        schema: { $ref: '#/components/schemas/GenericError' }
```

### Design questions

1. How to return the success response type while making error responses accessible?
2. How to type error responses per status code?
3. How to handle `default` responses?
4. How to handle responses with no body (204 No Content)?
5. How to expose response headers?
6. How to handle streaming responses (SSE)?

### Go conventions

Go APIs use `(T, error)` returns. The `error` interface is the standard mechanism for error values. `errors.As` / `errors.AsType[E]` (Go 1.26) provide typed error extraction.

## Decision

### Success Response → Return Value, Error Response → error

`openapigo.Do()` returns `(*Resp, error)` where:

- **2xx responses** → parsed into `*Resp` (the success type from the Endpoint definition)
- **3xx responses** → normally followed automatically by Go's `http.Client` (default redirect policy). If the client's `CheckRedirect` returns `http.ErrUseLastResponse` (stop following but not an error), the 3xx response arrives at `Do()` with `err == nil` and is processed through `parseError` like 4xx/5xx responses. If 3xx error handlers are registered on the endpoint (via `WithErrors` with `StatusRange: "3XX"` or exact status codes like `301`, `302`), the response body is parsed into the typed error. If no 3xx handler is registered, the response falls through to the `default` handler (if registered). If no `default` handler is registered either, a generic `APIError` with the raw body is returned. If `CheckRedirect` returns any other error, it is returned as a transport-level error (wrapped). **Note**: 3XX range handling is an extension of the base error handling defined in ADR-014 (which covers 4XX/5XX). The `ErrorHandler` type supports any status code range; ADR-014 focuses on the most common cases while this ADR documents the full range support including 3XX.
- **4xx/5xx responses** → parsed into a typed error and returned as `error`
- **Network/transport errors** → returned as `error` (wrapped)

```go
pet, err := openapigo.Do(ctx, client, api.GetPet, req)
if err != nil {
    // Could be an API error (4xx/5xx) or a transport error
    return err
}
// pet is *api.Pet
```

### API Error Type Hierarchy

We generate a structured error hierarchy:

```go
// ===== Runtime library =====

// APIError is the base error for all non-2xx HTTP responses.
type APIError struct {
    StatusCode int
    Status     string
    Header     http.Header
    Body       []byte // raw response body
}

func (e *APIError) Error() string {
    return fmt.Sprintf("API error: %s", e.Status)
}

// ===== Generated per operation =====

// GetPet404Error is returned when GetPet responds with 404.
type GetPet404Error struct {
    openapigo.APIError
    Payload NotFoundError // typed body
}

// Unwrap returns the embedded APIError, enabling errors.As(err, &apiErr)
// to match the base type through the error chain.
// NOTE: Go's errors.As does NOT inspect embedded fields — it only traverses
// the chain via Unwrap(). Without this method, a call like:
//   var apiErr *openapigo.APIError
//   errors.As(err, &apiErr)
// would return false even though APIError is embedded.
// SAFETY: Unwrap is defined on *GetPet404Error (pointer receiver).
// A typed nil (*GetPet404Error)(nil) would panic on `&e.APIError` (nil dereference).
// Guard against this: while parseError always returns non-nil pointers, a typed-nil
// error value could be introduced through other code paths (e.g., error wrapping,
// user code assigning (*GetPet404Error)(nil) to an error interface).
func (e *GetPet404Error) Unwrap() error {
    if e == nil {
        return nil
    }
    return &e.APIError
}

// GetPet400Error is returned when GetPet responds with 400.
type GetPet400Error struct {
    openapigo.APIError
    Payload ValidationError // typed body
}

func (e *GetPet400Error) Unwrap() error {
    if e == nil {
        return nil
    }
    return &e.APIError
}

// GetPetDefaultError is returned for unhandled status codes.
type GetPetDefaultError struct {
    openapigo.APIError
    Payload GenericError // from 'default' response
}

func (e *GetPetDefaultError) Unwrap() error {
    if e == nil {
        return nil
    }
    return &e.APIError
}
```

### Error Extraction

Users extract typed errors using standard `errors.As` (all Go versions) or the ergonomic `errors.AsType[E]` (Go 1.26+):

```go
// Go 1.26+: errors.AsType for ergonomic typed extraction
pet, err := openapigo.Do(ctx, client, api.GetPet, req)
if err != nil {
    if e, ok := errors.AsType[*api.GetPet404Error](err); ok {
        fmt.Println("Not found:", e.Payload.Message)
        return
    }
    var apiErr *openapigo.APIError
    if errors.As(err, &apiErr) {
        fmt.Printf("HTTP %d: %s\n", apiErr.StatusCode, apiErr.Status)
        return
    }
    return err
}
```

```go
// Go 1.24-1.25: standard errors.As
pet, err := openapigo.Do(ctx, client, api.GetPet, req)
if err != nil {
    var notFound *api.GetPet404Error
    if errors.As(err, &notFound) {
        fmt.Println("Not found:", notFound.Payload.Message)
        return
    }

    // Check for any API error (base type) — works via Unwrap() chain
    var apiErr *openapigo.APIError
    if errors.As(err, &apiErr) {
        fmt.Printf("HTTP %d: %s\n", apiErr.StatusCode, apiErr.Status)
        return
    }

    // Transport error (network, DNS, timeout, etc.)
    return err
}
```

**Important**: Each generated error type implements `Unwrap() error` returning `&e.APIError`. This enables `errors.As(err, &apiErr)` to match the base `*APIError` type through the error chain. Without `Unwrap()`, Go's `errors.As` would **not** find the embedded `APIError` — struct embedding does not create an error chain.

### Response Parsing Logic in Do()

The runtime `Do()` function handles response parsing:

```go
func Do[Req, Resp any](ctx context.Context, c *Client, ep Endpoint[Req, Resp], req Req) (*Resp, error) {
    // ... build and execute HTTP request ...

    // Response handling
    // Note: 3xx redirects are typically followed by http.Client before reaching here.
    // If CheckRedirect returns http.ErrUseLastResponse, the 3xx response arrives
    // here with err == nil and is handled as a non-success response below.
    switch {
    case resp.StatusCode >= 200 && resp.StatusCode < 300:
        return parseSuccess[Resp](resp)
    default:
        // 3xx (unredirected), 4xx, 5xx — all go through parseError.
        // parseError matches handlers in order: exact status → range (3XX/4XX/5XX) → default.
        return nil, parseError(resp, ep.errorParsers)
    }
}
```

Error parsers are registered per endpoint via the generated Endpoint variable. Handlers support exact status codes, status code ranges (3XX, 4XX, 5XX), and a default fallback:

```go
var GetPet = openapigo.Endpoint[GetPetRequest, Pet]{
    Method: "GET",
    Path:   "/pets/{petId}",
}.WithErrors(
    openapigo.ErrorHandler{Status: 400, Parse: parseGetPet400Error},
    openapigo.ErrorHandler{Status: 404, Parse: parseGetPet404Error},
    openapigo.ErrorHandler{StatusRange: "3XX", Parse: parseGetPetRedirectError}, // catch-all for unredirected 3xx
    openapigo.ErrorHandler{StatusRange: "4XX", Parse: parseGetPetClientError},   // catch-all for 4xx
    openapigo.ErrorHandler{StatusRange: "5XX", Parse: parseGetPetServerError},   // catch-all for 5xx
    openapigo.ErrorHandler{Default: true, Parse: parseGetPetDefaultError},
)
```

### 204 No Content

When the success response has no body (204), the response type is `openapigo.NoContent`:

```yaml
responses:
  204:
    description: Successfully deleted
```

```go
var DeletePet = openapigo.Endpoint[DeletePetRequest, openapigo.NoContent]{
    Method: "DELETE",
    Path:   "/pets/{petId}",
}

// Usage
_, err := openapigo.Do(ctx, client, api.DeletePet, req)
// return value is *openapigo.NoContent (can be ignored)
```

```go
// Runtime type
type NoContent struct{}
```

### Multiple Success Status Codes

When an operation defines multiple 2xx responses with different schemas:

```yaml
responses:
  200:
    content:
      application/json:
        schema: { $ref: '#/components/schemas/Pet' }
  201:
    content:
      application/json:
        schema: { $ref: '#/components/schemas/PetCreated' }
```

We generate a response union:

```go
type CreatePetResponse struct {
    Pet200    *Pet
    Created201 *PetCreated
    StatusCode int
}

var CreatePet = openapigo.Endpoint[CreatePetRequest, CreatePetResponse]{
    Method: "POST",
    Path:   "/pets",
}
```

The user checks which response was received:

```go
resp, err := openapigo.Do(ctx, client, api.CreatePet, req)
if resp.Pet200 != nil {
    // 200
}
if resp.Created201 != nil {
    // 201
}
```

When all 2xx responses share the same schema, a single type is used (no union needed).

### Response Headers

When response headers are defined in the spec, they are included in the response type:

```yaml
responses:
  200:
    headers:
      X-Request-Id:
        schema: { type: string }
      X-Rate-Limit-Remaining:
        schema: { type: integer }
    content:
      application/json:
        schema: { $ref: '#/components/schemas/Pet' }
```

```go
type GetPetResponse struct {
    Body               Pet
    XRequestID         string `header:"X-Request-Id"`
    XRateLimitRemaining int   `header:"X-Rate-Limit-Remaining"`
}
```

For operations with response headers, the Endpoint's response type is the wrapper struct (not the body schema directly). The runtime parses both body and headers.

### Raw Response Access

Users who need access to the raw `*http.Response` can use `DoRaw`:

```go
resp, httpResp, err := openapigo.DoRaw(ctx, client, api.GetPet, req)
// httpResp is *http.Response — body already consumed
// resp is *api.Pet (or nil on error)
// err is typed API error or transport error
defer httpResp.Body.Close() // already drained, but good practice
```

### Streaming Responses

For `text/event-stream` (SSE) or `application/x-ndjson` responses:

```yaml
responses:
  200:
    content:
      text/event-stream:
        schema:
          type: object
          properties:
            event: { type: string }
            data: { type: string }
```

```go
var StreamEvents = openapigo.StreamEndpoint[StreamEventsRequest, Event]{
    Method: "GET",
    Path:   "/events",
}

// Usage: returns iter.Seq2[Event, error]
for event, err := range openapigo.DoStream(ctx, client, api.StreamEvents, req) {
    if err != nil {
        return err
    }
    fmt.Println(event.Data)
}
```

`DoStream` returns an iterator that reads the response body incrementally. The connection is held open until the iterator is exhausted or the context is canceled.

**Error handling**: `DoStream` checks the initial HTTP response status code **before** entering the streaming loop. On non-2xx responses, the iterator yields a single `(zero, error)` pair where the error follows the same `parseError` logic as `Do()` (exact status → range → default handler, per ADR-014). The connection is then closed. This ensures consistent error handling between `Do()` and `DoStream()` — callers can use the same `errors.As` patterns for both. Mid-stream errors (connection drops, malformed events) are yielded as `(zero, error)` pairs during iteration, wrapped in a `*StreamError` type (distinct from `*APIError`) to allow callers to distinguish initial HTTP errors from mid-stream failures.

### Status Code Ranges

OpenAPI supports status code ranges like `2XX`, `4XX`, `5XX`:

```yaml
responses:
  2XX:
    content:
      application/json:
        schema: { $ref: '#/components/schemas/Pet' }
  4XX:
    content:
      application/json:
        schema: { $ref: '#/components/schemas/ClientError' }
```

Specific status codes take precedence over ranges. Ranges take precedence over `default`.

```go
// Resolution order for status 404:
// 1. Check for explicit 404 handler
// 2. Check for 4XX handler
// 3. Check for default handler
// 4. Return generic APIError
```

### Wildcard Media Type in Responses

When the response uses `*/*` or a wildcard:

```yaml
responses:
  200:
    content:
      application/octet-stream:
        schema:
          type: string
          format: binary
```

```go
// Binary response
var DownloadFile = openapigo.Endpoint[DownloadFileRequest, openapigo.BinaryResponse]{
    Method: "GET",
    Path:   "/files/{fileId}",
}

// openapigo.BinaryResponse wraps io.ReadCloser
type BinaryResponse struct {
    Body        io.ReadCloser
    ContentType string
}
```

## Consequences

### Positive

- **Idiomatic Go**: `(T, error)` return, `errors.As` / `errors.AsType` (1.26+) for typed error handling
- **Per-status-code typed errors**: users can handle 404 differently from 400 at compile time
- **Raw response accessible**: `DoRaw` for advanced use cases
- **Streaming built-in**: `DoStream` + iterators for SSE/NDJSON
- **Response headers typed**: extracted into struct fields, not lost in `http.Response`
- **204 No Content is clean**: `NoContent` type, no awkward `*interface{}`

### Negative

- **Error type proliferation**: each operation × each error status = many error types. Mitigated by sharing common error schemas across operations.
- **Generated error names are verbose**: `GetPet404Error` is long but clear
- **Multiple 2xx responses are uncommon but complex**: the response union adds cognitive overhead for a rare case

### Risks

- APIs that don't define error response schemas produce untyped `APIError` (raw body as `[]byte`). This is common — many specs only define success responses. We document this and provide `APIError.Body` for manual parsing.
- Streaming responses hold HTTP connections open. If the user forgets to drain the iterator, connections may leak. We document the requirement and close the body on context cancellation.
