# ADR-014: HTTP Client API Design

## Status

Accepted

## Date

2026-03-01

## Context

This is the most important design decision for the project. The HTTP client API determines the daily developer experience.

### What openapi-fetch achieves

```typescript
const { data, error } = await client.GET("/pets/{petId}", {
  params: { path: { petId: "123" } },
});
// data is automatically typed — no manual generic annotation
```

Key properties:

1. The **path string literal** determines all types (params, request body, response)
2. **No manual generic annotations** at call sites
3. **Minimal client** — the client is just a configured fetch wrapper
4. **IDE autocomplete** works for paths, params, and response fields

### Go's constraints

Go cannot infer a return type from a string argument's value. `client.GET("/pets/{petId}")` cannot return `*Pet` in Go — the compiler does not have template literal types or dependent types.

### Approaches considered

**A: Method per operation** (oapi-codegen style)
```go
pet, err := client.GetPet(ctx, "123")
```
- Pro: Clean call site
- Con: Client struct grows with every operation, not "thin wrapper"

**B: Path string + manual generic annotation**
```go
pet, err := openapigo.GET[Pet](client, "/pets/{petId}", ...)
```
- Pro: Path-centric
- Con: Manual type annotation — exactly what openapi-fetch avoids

**C: Method per operation, grouped by tag**
```go
pet, err := client.Pets.Get(ctx, "123")
```
- Pro: Organized, familiar
- Con: Generated client is large, grouping logic is opinionated

**D: Endpoint objects + generic Do()**
```go
pet, err := openapigo.Do(client, api.GetPet, api.GetPetRequest{...})
```
- Pro: Client is minimal, type inference works via endpoint object, composable
- Con: Slightly more verbose than A/C

## Decision

### Approach D: Endpoint Objects + Generic Do()

We adopt **Endpoint objects** as the core API pattern. This is the closest Go equivalent to openapi-fetch's path-literal type safety.

### Core Runtime Types

```go
// Endpoint defines a typed API operation.
// Req is the request type (params + body), Resp is the success response type.
// All fields are unexported to prevent mutation of package-level Endpoint variables
// (which would cause data races in concurrent use and unexpected behavior).
type Endpoint[Req, Resp any] struct {
    method         string
    path           string           // OpenAPI path template, e.g. "/pets/{petId}"
    successCodes   []int            // status codes treated as success (default: 200-299)
    successParser  func(resp *http.Response) (*Resp, error) // parses success response body
    errorParsers   []ErrorHandler   // registered via WithErrors(); unexported to prevent direct mutation
}

// Method returns the HTTP method.
func (e Endpoint[Req, Resp]) Method() string { return e.method }

// Path returns the OpenAPI path template.
func (e Endpoint[Req, Resp]) Path() string { return e.path }

// isSuccessStatus returns true if the status code is a success status for this endpoint.
// When successCodes is explicitly set (via WithSuccessCodes or generated code), ONLY those
// codes are treated as success — this enables ADR-017's "undefined 2xx → UnexpectedStatusError"
// behavior. When successCodes is empty (default), all 2xx codes are treated as success.
// Generated endpoints always call WithSuccessCodes with the specific codes from the OpenAPI
// spec (e.g., WithSuccessCodes(200) or WithSuccessCodes(200, 201)), so undefined 2xx status
// codes fall through to error handling. When the spec defines a 2XX range response, the
// generator leaves successCodes empty (all 2xx are success).
func (e Endpoint[Req, Resp]) isSuccessStatus(code int) bool {
    if len(e.successCodes) > 0 {
        for _, c := range e.successCodes {
            if c == code {
                return true
            }
        }
        return false
    }
    return code >= 200 && code < 300
}

// ErrorHandler maps an HTTP status code (or range) to a response parser.
// Matching priority: exact Status → StatusRange (3XX/4XX/5XX) → Default.
// See ADR-017 for full response handling details.
//
// 3xx caveat: Go's http.Client automatically follows redirects
// (301/302/303/307/308) by default, so most 3xx codes never reach
// this handler unless Do() overrides CheckRedirect. When the OpenAPI
// spec defines a 3xx response, Do() sets a per-request CheckRedirect
// that returns http.ErrUseLastResponse for those specific codes,
// allowing them to reach the handler (see ADR-017). StatusRange "3XX"
// is supported and matches any 3xx code that reaches the handler.
type ErrorHandler struct {
    Status      int                            // exact status code (e.g., 404)
    StatusRange string                         // status range: "3XX", "4XX", "5XX"
    Default     bool                           // catch-all for unmatched status codes
    Parse       func(resp *http.Response) error // parses response body into typed error
}

// NewEndpoint creates an Endpoint with the given method and path.
// This is the only way to construct an Endpoint — struct literal initialization
// is not possible because all fields are unexported.
func NewEndpoint[Req, Resp any](method, path string) Endpoint[Req, Resp] {
    return Endpoint[Req, Resp]{method: method, path: path}
}

// WithSuccessCodes returns a copy of the Endpoint with the given success status codes.
// When set, ONLY these codes are treated as success (overriding the default "all 2xx" behavior).
// Generated code always calls this with the specific success codes from the OpenAPI spec.
// Additional non-2xx success codes (e.g., 304) can also be included.
func (e Endpoint[Req, Resp]) WithSuccessCodes(codes ...int) Endpoint[Req, Resp] {
    combined := make([]int, len(e.successCodes)+len(codes))
    copy(combined, e.successCodes)
    copy(combined[len(e.successCodes):], codes)
    e.successCodes = combined
    return e
}

// WithSuccessParser returns a copy of the Endpoint with a custom success response parser.
// This enables status-code-specific response parsing (e.g., different schemas for 200 vs 201).
func (e Endpoint[Req, Resp]) WithSuccessParser(parser func(resp *http.Response) (*Resp, error)) Endpoint[Req, Resp] {
    e.successParser = parser
    return e
}

// WithErrors returns a copy of the Endpoint with error handlers appended.
// Handlers are matched by priority: exact status code → status range (3XX/4XX/5XX) → default.
// Within the same priority level, **later-registered handlers shadow earlier ones**
// (last-wins semantics). This allows callers to override default error handling:
//   ep.WithErrors(default404Handler).WithErrors(custom404Handler)
//   // → custom404Handler matches for 404, default404Handler is shadowed.
// Multiple calls to WithErrors accumulate handlers (append, not replace).
// The runtime checks handlers in reverse registration order within each priority level,
// returning the first match. See ADR-017 for full response handling details including 3XX.
func (e Endpoint[Req, Resp]) WithErrors(handlers ...ErrorHandler) Endpoint[Req, Resp] {
    combined := make([]ErrorHandler, len(e.errorParsers)+len(handlers))
    copy(combined, e.errorParsers)
    copy(combined[len(e.errorParsers):], handlers)
    e.errorParsers = combined
    return e
}

// Do executes an API call. The response type is inferred from the endpoint.
func Do[Req, Resp any](
    ctx context.Context,
    client *Client,
    endpoint Endpoint[Req, Resp],
    req Req,
) (*Resp, error) {
    // 1. Serialize request (path params, query params, headers, body)
    // 2. Build HTTP request
    // 3. Run middleware
    // 4. Execute HTTP call
    // 5. Parse response by status code
    // 6. Return (*Resp, nil) or (nil, typed error)
    // ...
    return nil, nil
}
```

### Generated Code

For each operation in the spec, we generate:

1. An **Endpoint variable** with the request/response types baked in
2. A **request struct** with all parameters and body
3. **Response/error types** per status code

```yaml
# OpenAPI spec
paths:
  /pets/{petId}:
    get:
      operationId: getPet
      parameters:
        - name: petId
          in: path
          required: true
          schema: { type: string }
        - name: include
          in: query
          schema: { type: string, enum: [owner, vaccinations] }
      responses:
        200:
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Pet' }
        404:
          content:
            application/json:
              schema: { $ref: '#/components/schemas/NotFoundError' }
```

```go
// ===== Generated: endpoints.go =====

// GetPet retrieves a pet by ID.
var GetPet = openapigo.NewEndpoint[GetPetRequest, Pet]("GET", "/pets/{petId}").
    WithSuccessCodes(200).
    WithErrors(
        // Error parsers registered per status code (see ADR-017 for error type details)
        openapigo.ErrorHandler{Status: 404, Parse: parseGetPet404Error},
    )

// ===== Generated: operations.go =====

type GetPetRequest struct {
    PetID   string          `path:"petId"`
    Include *GetPetInclude  `query:"include,omitzero"`
}

type GetPetInclude string

const (
    GetPetIncludeOwner        GetPetInclude = "owner"
    GetPetIncludeVaccinations GetPetInclude = "vaccinations"
)
```

### User Code

```go
package main

import (
    "context"
    "fmt"

    "github.com/mkusaka/openapigo"
    "myproject/api" // generated package
)

func main() {
    client := openapigo.NewClient(
        openapigo.WithBaseURL("https://api.example.com"),
    )

    ctx := context.Background()

    // Type inference: pet is *api.Pet, inferred from api.GetPet
    pet, err := openapigo.Do(ctx, client, api.GetPet, api.GetPetRequest{
        PetID: "123",
    })
    if err != nil {
        // Typed error handling (see ADR-017)
        fmt.Println(err)
        return
    }
    fmt.Println(pet.Name)
}
```

### Why This Works Like openapi-fetch

| openapi-fetch | openapigo | How it's similar |
|---------------|-----------|-----------------|
| `client.GET("/pets/{petId}", ...)` | `openapigo.Do(ctx, client, api.GetPet, ...)` | **Path determines types** — `api.GetPet` carries the type info |
| Return type inferred from path | Return type inferred from endpoint | **No manual generic annotation** |
| `{ data, error }` return | `(*T, error)` return | **Success/error separation** |
| Middleware via `client.use()` | Middleware via `WithMiddleware()` | **Composable extensibility** |
| Client is ~700 lines | Client is target < 1000 lines | **Minimal runtime** |

### Flat Request Structs with Struct Tags

Request structs use struct tags to indicate where each field goes:

| Tag | Meaning | Example |
|-----|---------|---------|
| `path:"name"` | Path parameter | `PetID string \`path:"petId"\`` |
| `query:"name"` | Query parameter | `Limit *int \`query:"limit"\`` |
| `header:"name"` | Header parameter | `APIVersion string \`header:"X-API-Version"\`` |
| `cookie:"name"` | Cookie parameter | `Session string \`cookie:"session_id"\`` |
| `body:"mediaType"` | Request body | `Body CreatePetBody \`body:"application/json"\`` |

**Example with all parameter locations:**

```go
type UpdatePetRequest struct {
    // Path parameters
    PetID string `path:"petId"`

    // Query parameters
    DryRun *bool `query:"dry_run,omitzero"`

    // Header parameters
    IfMatch *string `header:"If-Match,omitzero"`

    // Request body
    Body UpdatePetBody `body:"application/json"`
}

type UpdatePetBody struct {
    Name string  `json:"name"`
    Tag  *string `json:"tag,omitzero"`
}
```

### Operations Without Parameters

When an operation has no parameters and no body, the request type is `openapigo.NoRequest`:

```go
var ListPets = openapigo.NewEndpoint[openapigo.NoRequest, ListPetsResponse]("GET", "/pets").
    WithSuccessCodes(200)

// Usage: no request argument needed
pets, err := openapigo.Do(ctx, client, api.ListPets, openapigo.NoRequest{})
```

We also provide a convenience function for parameterless calls:

```go
pets, err := openapigo.DoSimple(ctx, client, api.ListPets)
```

### Client Configuration

```go
client := openapigo.NewClient(
    // Required
    openapigo.WithBaseURL("https://api.example.com"),

    // Optional: custom HTTP client
    openapigo.WithHTTPClient(&http.Client{Timeout: 30 * time.Second}),

    // Optional: default headers
    openapigo.WithDefaultHeader("User-Agent", "myapp/1.0"),

    // Optional: middleware (see ADR-018 for auth)
    openapigo.WithMiddleware(loggingMiddleware, retryMiddleware),

    // Optional: custom JSON encoder/decoder (for json/v2 migration)
    openapigo.WithJSONCodec(customCodec),
)
```

The `Client` struct is small:

```go
type Client struct {
    baseURL    string
    httpClient *http.Client
    middleware []Middleware
    headers    http.Header
    codec      JSONCodec
}
```

### Middleware Interface

```go
type Middleware interface {
    // RoundTrip wraps the HTTP call. Call next to proceed.
    RoundTrip(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error)
}

// MiddlewareFunc is a convenience adapter.
type MiddlewareFunc func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error)

func (f MiddlewareFunc) RoundTrip(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
    return f(req, next)
}
```

This is deliberately simpler than openapi-fetch's `onRequest`/`onResponse`/`onError` split. Go developers are familiar with the `http.RoundTripper` pattern, and a wrapping middleware is more natural.

### Pagination via Iterators

For paginated endpoints, we generate an iterator function (per ADR-005, using `iter.Seq2`):

```go
// Generated for endpoints that match pagination patterns
// (detected by response having items array + cursor/next field)
func ListPetsIter(
    ctx context.Context,
    client *openapigo.Client,
    req ListPetsRequest,
) iter.Seq2[Pet, error] {
    return func(yield func(Pet, error) bool) {
        for {
            resp, err := openapigo.Do(ctx, client, ListPets, req)
            if err != nil {
                yield(Pet{}, err)
                return
            }
            for _, item := range resp.Items {
                if !yield(item, nil) {
                    return
                }
            }
            if resp.NextCursor == nil {
                return
            }
            req.Cursor = resp.NextCursor
        }
    }
}

// Usage
for pet, err := range api.ListPetsIter(ctx, client, api.ListPetsRequest{}) {
    if err != nil {
        return err
    }
    fmt.Println(pet.Name)
}
```

### Why Not Method-Per-Operation

We do not generate methods on the Client for each operation because:

1. **Client stays thin**: the runtime library doesn't grow with the API surface
2. **No circular dependency**: generated code imports the runtime, not vice versa
3. **Composable**: `Do()` is a free function, making it easy to wrap, mock, and test
4. **Consistent**: every operation follows the exact same pattern

For users who prefer method-based access, a convenience wrapper can be generated via CLI flag `--method-client`:

```go
// Generated with --method-client
type PetStoreClient struct {
    client *openapigo.Client
}

func (c *PetStoreClient) GetPet(ctx context.Context, req GetPetRequest) (*Pet, error) {
    return openapigo.Do(ctx, c.client, GetPet, req)
}
```

But this is opt-in, not the default.

## Consequences

### Positive

- **Type inference works**: `openapigo.Do(ctx, client, api.GetPet, req)` — `*Pet` return type is inferred from `api.GetPet`
- **Client is truly minimal**: ~500 lines runtime, no per-operation code
- **Every operation follows the same pattern**: easy to learn, easy to review
- **Testable**: mock by replacing the `*http.Client` or middleware
- **IDE autocomplete**: `api.` shows all endpoint variables; field autocomplete works on request structs
- **Flat request structs**: all params in one struct, no nested objects for common cases

### Negative

- **`openapigo.Do(ctx, client, endpoint, req)` is more verbose** than `client.GetPet(ctx, "123")` — four arguments instead of two
- **Endpoint variables are a new pattern**: Go developers may not immediately recognize `api.GetPet` as an operation descriptor
- **Free function vs method**: `openapigo.Do()` is a package-level function, which some teams may find less discoverable than client methods

### Risks

- Go's type inference for generic functions has edge cases. If the compiler cannot infer types (e.g., when `NoRequest` is used with an interface), explicit type arguments may be needed. We test against all Go 1.24+ compilers.
- The struct tag approach for parameter serialization adds runtime reflection overhead. This is amortized by caching struct metadata per type (computed once via `sync.Once`).
- **DoStream and error responses**: `DoStream` (ADR-017) checks the initial HTTP response status code before entering the streaming loop using the same `isSuccessStatus` logic as `Do()` (checks `successCodes` if set, otherwise all 2xx). On non-success responses, the iterator yields a single `(zero, error)` pair where the error follows the same `parseError` logic as `Do()` (exact status → range → default handler). The connection is then closed. This ensures consistent error handling between `Do()` and `DoStream()`.
