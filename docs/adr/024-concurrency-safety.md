# ADR-024: Concurrency Safety Contract

## Status

Accepted

## Date

2026-03-02

## Context

Go programs routinely use goroutines for concurrent HTTP requests. A typical pattern:

```go
client := openapigo.NewClient(openapigo.WithBaseURL("https://api.example.com"))

var wg sync.WaitGroup
for _, id := range petIDs {
    wg.Add(1)
    go func() {
        defer wg.Done()
        pet, err := openapigo.Do(ctx, client, api.GetPet, api.GetPetRequest{PetID: id})
        // ...
    }()
}
wg.Wait()
```

If `Client` is not goroutine-safe, this common pattern causes data races. The existing ADRs mention concurrency in passing:

- ADR-014: Endpoint fields are unexported "to prevent mutation of package-level Endpoint variables (which would cause data races in concurrent use)"
- ADR-018: "Token refresh races: if `tokenFunc` is slow and multiple requests fire concurrently, each may call `tokenFunc` independently. This is the user's responsibility."

But there is no comprehensive contract defining what is safe to use concurrently and what is not.

### Go standard library precedent

- `http.Client`: "Clients are safe for concurrent use by multiple goroutines." ([net/http docs](https://pkg.go.dev/net/http#Client))
- `http.Transport`: "Transports should be reused instead of created as needed. Transports are safe for concurrent use by multiple goroutines."
- `sync.Mutex`: Not safe for concurrent use (by design)

## Decision

### Concurrency Safety Guarantees

The following contract applies to all runtime types and generated types:

#### Safe for Concurrent Use (Goroutine-Safe)

| Type/Function | Guarantee | Notes |
|---|---|---|
| `*Client` | Safe after construction | All fields are read-only after `NewClient()`. `Do()` reads but never writes Client fields. |
| `Do()`, `DoSimple()`, `DoRaw()` | Safe | Stateless free functions. Each call builds an independent `*http.Request`. |
| `DoStream()` | Safe to call concurrently | Each call returns an independent iterator. However, a single iterator (`iter.Seq2`) is NOT safe for concurrent consumption — use one goroutine per iterator. |
| `Endpoint[Req, Resp]` variables | Safe | Package-level `var` declarations are read-only after `init()`. Value type (not pointer) ensures copies don't share state. |
| `Nullable[T]` | Safe for **read-only sharing** | `Nullable[T]` itself is a value type (copied on assignment). However, if `T` is a reference type (`map`, `slice`, pointer), the inner value shares underlying storage after copy. Read-only sharing is safe; concurrent mutation of the inner `T` is NOT safe — same rules as generated types below. |
| `MiddlewareFunc` | Safe if the function itself is safe | The runtime does not add synchronization around middleware calls. |
| Generated types (structs, enums) | Safe for **read-only sharing** | Shallow copying a struct and reading fields concurrently is safe. However, generated types with `map` fields (from `additionalProperties` — ADR-006) or `slice` fields (from `items`) share underlying storage on copy. Concurrent **mutation** (e.g., calling `SetAdditionalProperty()` on the same instance, or appending to a shared slice) is NOT safe — use one instance per goroutine or add external synchronization. |
| `Validate()` methods | Safe (read-only receiver) | Pure functions on value receiver. Exception: `Validate()` with **pointer receiver** for unevaluatedProperties (ADR-012) may populate `AdditionalProperties` — see NOT-safe table below. |

#### NOT Safe for Concurrent Use

| Type/Operation | Why | Mitigation |
|---|---|---|
| `Client` construction (`NewClient()`) | Not a concern — construction is a one-time operation | Construct once, share the `*Client` |
| Mutating an `Endpoint` after `init()` | Endpoint values are immutable by convention. `WithErrors()`, `WithSuccessCodes()` return copies, not mutate in place. But if a user stores the result in a shared variable without synchronization, that's a race. | Endpoint variables should be package-level `var` (initialized at package load time) or local variables. |
| Mutating generated types with `map`/`slice` fields | `SetAdditionalProperty()` (ADR-006) writes to an internal `map`. Appending to slice fields shares underlying array. Both race under concurrent mutation. | Use one instance per goroutine, or protect mutation with `sync.Mutex`. Read-only sharing (e.g., concurrent `json.Marshal` on the same struct) is safe as long as no goroutine mutates. |
| `Validate()` with pointer receiver (ADR-012) | Populates `AdditionalProperties` on the struct — concurrent calls on the **same struct value** would race | Call `Validate()` once per value, or copy the struct before concurrent validation |
| User-provided `tokenFunc` in auth middleware | Called on every request without synchronization | Users must make `tokenFunc` goroutine-safe (e.g., `sync.Mutex`, `singleflight.Group`, or a token cache) |
| User-provided `Middleware` implementations | Called on every request without synchronization | Users must make their middleware goroutine-safe |
| Modifying `*http.Client` after passing to `WithHTTPClient()` | The runtime stores the pointer and uses it on every request | Do not modify the `*http.Client` after passing it to `NewClient()` |

### Client Immutability After Construction

The `Client` struct is **effectively immutable** after `NewClient()` returns:

```go
type Client struct {
    baseURL    string           // immutable after construction
    httpClient *http.Client     // pointer stored, never reassigned
    middleware []Middleware      // slice stored, never appended/modified
    headers    http.Header      // cloned during construction, never modified after
    codec      JSONCodec        // interface stored, never reassigned
}
```

**No setters**: There are no `Set*` methods on `Client`. All configuration is via `NewClient()` options. This eliminates an entire class of concurrency bugs.

**Header cloning**: `WithDefaultHeader()` during construction adds to a `http.Header` that is **cloned** into the `Client`. The `Do()` function clones the default headers into each request's header map, so concurrent requests do not share header state:

```go
func Do[Req, Resp any](ctx context.Context, client *Client, endpoint Endpoint[Req, Resp], req Req) (*Resp, error) {
    httpReq, _ := http.NewRequestWithContext(ctx, endpoint.Method(), /* ... */)
    // Clone default headers — each request gets its own copy
    for k, v := range client.headers {
        httpReq.Header[k] = append([]string(nil), v...)
    }
    // ...
}
```

### Middleware Concurrency Contract

Middleware implementations MUST be goroutine-safe. The runtime does not add synchronization:

```go
// CORRECT: Stateless middleware — inherently goroutine-safe
func loggingMiddleware(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
    log.Printf("Request: %s %s", req.Method, req.URL)
    return next(req)
}

// CORRECT: Middleware with synchronized state
type rateLimiter struct {
    mu      sync.Mutex
    tokens  int
}
func (r *rateLimiter) RoundTrip(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
    r.mu.Lock()
    // ... check and decrement tokens
    r.mu.Unlock()
    return next(req)
}

// WRONG: Middleware with unsynchronized mutable state
type counter struct {
    count int // DATA RACE: modified without synchronization
}
func (c *counter) RoundTrip(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
    c.count++ // race condition when called from multiple goroutines
    return next(req)
}
```

The generated doc comment on auth middleware (ADR-018) includes a concurrency note:

```go
// WithBearerAuthFunc returns middleware that calls tokenFunc before each request.
// tokenFunc MUST be safe for concurrent use — it is called without synchronization
// from multiple goroutines when the Client is shared.
func WithBearerAuthFunc(tokenFunc func(ctx context.Context) (string, error)) openapigo.Middleware {
```

### Generated Endpoint Variables

Endpoint variables are declared at package level:

```go
var GetPet = openapigo.NewEndpoint[GetPetRequest, Pet]("GET", "/pets/{petId}").
    WithSuccessCodes(200).
    WithErrors(...)
```

These are initialized once during package initialization and never modified. `WithSuccessCodes()` and `WithErrors()` return **copies** (value receivers on `Endpoint[Req, Resp]`), so the chain of calls builds a new value that is assigned to the `var`.

**User responsibility**: If a user creates a local `Endpoint` variable and shares it across goroutines via a pointer or shared variable, they must ensure synchronization. This is an unusual pattern — the generated code always uses package-level `var`.

### Race Detection in CI

The CI pipeline (ADR-023) runs all tests with `-race`:

```bash
go test ./... -race -count=1
```

Integration tests specifically exercise concurrent `Do()` calls to verify the goroutine-safety contract:

```go
func TestClient_ConcurrentDo(t *testing.T) {
    client := openapigo.NewClient(openapigo.WithBaseURL(srv.URL))
    var wg sync.WaitGroup
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            _, _ = openapigo.Do(ctx, client, api.GetPet, api.GetPetRequest{PetID: "1"})
        }()
    }
    wg.Wait()
}
```

This test fails under `-race` if any internal state is improperly shared.

## Consequences

### Positive

- **Clear contract**: Users know exactly what is and isn't safe to share across goroutines
- **Safe by default**: The most common pattern (shared `*Client`, concurrent `Do()` calls) is safe without any user action
- **No internal locks in hot path**: `Do()` is lock-free — no `sync.Mutex` in the request/response path. Performance is bounded by HTTP I/O, not internal synchronization.
- **Race detector catches violations**: CI with `-race` verifies the contract holds

### Negative

- **User-provided components must be goroutine-safe**: Middleware, `tokenFunc`, and custom `JSONCodec` implementations are the user's responsibility. This is documented but can still be a source of bugs.
- **No `Client.SetHeader()` or `Client.AddMiddleware()`**: Immutability means configuration changes require creating a new `Client`. This is intentional but may surprise users expecting mutable clients.

### Risks

- **`http.Client` internal state**: Go's `http.Client` has internal mutable state (connection pool, cookie jar). This is goroutine-safe per Go docs, but unusual `http.Transport` configurations (e.g., custom `DialContext` with shared state) could introduce races. This is outside our control — it's the user's `http.Client`.
- **Future features**: If we add features like request-scoped configuration (e.g., per-request timeout overrides), we must maintain the immutability contract. Per-request options should be passed as arguments to `Do()`, not as mutations on `Client`.
