# ADR-001: Project Philosophy — Thin Wrapper Approach

## Status

Accepted

## Date

2026-03-01

## Context

Existing Go OpenAPI client generators fall into two camps:

1. **Heavy generators** (openapi-generator, go-swagger): produce large, opinionated SDKs with deep runtime dependencies, framework lock-in, and non-idiomatic Go code.
2. **Moderate generators** (oapi-codegen, ogen): produce cleaner code but still embed significant logic — custom JSON codecs, built-in OpenTelemetry, or router coupling.

Neither camp provides what the TypeScript ecosystem achieves with **openapi-fetch + openapi-typescript**: a clean separation between *generated types* (build-time, zero runtime) and a *thin fetch wrapper* (minimal runtime that leverages those types for compile-time safety).

The Go ecosystem lacks a tool that:

- Generates idiomatic Go types faithful to the OpenAPI spec
- Provides a minimal HTTP client wrapper typed by those generated types
- Stays out of the way — no framework opinions, no runtime validation, no bundled observability

## Decision

We adopt the **openapi-fetch design philosophy** adapted for Go:

### 1. Types Are Generated, Runtime Is Minimal

The CLI generates Go types (structs, interfaces, type aliases) from OpenAPI specs. A small runtime library provides:

- Generic HTTP client wrapper
- JSON marshal/unmarshal helpers for composition types (oneOf, anyOf, allOf)
- Error types

The runtime library targets **< 1000 lines of Go code** — comparable to openapi-fetch's ~700 lines of JavaScript.

### 2. No Automatic Runtime Validation

Generated code does **not** automatically validate requests or responses at runtime. Type safety is the primary contract, enforced at compile time through Go's type system.

However, OpenAPI defines constraints that Go's type system cannot express (e.g., `maxLength`, `pattern`, `minimum`). For these, we generate opt-in `Validate()` methods (see ADR-013) that users can call explicitly. The key principle:

- **Validation code is generated** — available for users who need it
- **Validation is not called automatically by default** — not in marshal, unmarshal, or HTTP calls. This default can be overridden via `--validate-on-unmarshal` (see ADR-013), which is an explicit user opt-in to stricter behavior. **Flag interaction**: `--skip-validation` and `--validate-on-unmarshal` are **mutually exclusive** — the generator emits a fatal error if both are specified: `ERROR: --skip-validation and --validate-on-unmarshal are mutually exclusive. --skip-validation prevents Validate() generation, which --validate-on-unmarshal requires.` This is enforced at CLI flag parsing time before any code generation occurs.
- **Users opt in** by calling `Validate()` on request/response types
- **Decode invariants are NOT validation**: `UnmarshalJSON` for oneOf/anyOf types performs structural match-count checks (e.g., "exactly one variant matched" for oneOf, "at least one variant matched" for anyOf) and discriminator routing (rejecting unknown discriminator values). These are **decode invariants**, not schema validation — they are essential for constructing a valid Go value and cannot be deferred to `Validate()`. Without them, the Go struct would be in an undefined state (no variant set, or wrong variant populated). Similarly, discriminator-based `UnmarshalJSON` returns `UnknownDiscriminatorError` for unrecognized discriminator values. These checks always run regardless of `--validate-on-unmarshal`.

This matches openapi-fetch's philosophy of staying out of the way while acknowledging that Go cannot express all schema constraints at the type level like TypeScript can.

### 3. `(data, error)` Return Pattern

API calls return `(T, error)` following Go conventions. HTTP error responses (4xx, 5xx) are returned as typed error values, not panics. This mirrors openapi-fetch's `{ data, error, response }` discriminated union pattern, adapted to Go idioms.

```go
pet, err := openapigo.Do(ctx, client, api.GetPet, api.GetPetRequest{PetID: "123"})
if err != nil {
    // Go 1.26+:
    //   if apiErr, ok := errors.AsType[*openapigo.APIError](err); ok {
    //       log.Error("API error", "status", apiErr.StatusCode)
    //   }
    // Go 1.24-1.25:
    var apiErr *openapigo.APIError
    if errors.As(err, &apiErr) {
        log.Error("API error", "status", apiErr.StatusCode)
    }
    return err
}
// pet is *api.Pet — typed as the 200 response schema
```

### 4. Middleware-Based Extensibility

Authentication, retries, logging, and observability are **not** built into the generated code or runtime. Users compose behavior through Go's standard `http.RoundTripper` interface or a middleware chain on the client:

```go
client := openapigo.NewClient(
    openapigo.WithBaseURL("https://api.example.com"),
    openapigo.WithHTTPClient(myHTTPClient),
    openapigo.WithMiddleware(authMiddleware, loggingMiddleware),
)
```

### 5. Endpoint-Based Type Safety

Inspired by openapi-fetch's path-literal type safety, we leverage Go generics to achieve equivalent type safety through **generated Endpoint objects** (see ADR-014 for full design).

Each API operation is a typed `Endpoint[Req, Resp]` variable. A generic `Do()` function infers request and response types from the endpoint:

```go
// The endpoint variable carries all type information
pet, err := openapigo.Do(ctx, client, api.GetPet, api.GetPetRequest{
    PetID: "123",
})
// pet is *api.Pet — inferred from api.GetPet, no manual generic annotation
```

Key properties:

- The **Endpoint object** determines available parameters and response types (Go's equivalent of openapi-fetch's path literal)
- Required parameters are represented as non-pointer (non-optional) fields in the request struct, providing **type-level safety** (e.g., `string` vs `*string`). Note: Go's keyed struct literals allow omitting fields (defaulting to zero values), so compile-time **presence** enforcement is not absolute — a required `string` field omitted from the literal silently becomes `""`. Full presence validation requires calling `Validate()` (ADR-013).
- No manual generic type annotations needed at call sites — Go infers `Resp` from the endpoint
- The runtime client is minimal (~500 lines) and does not grow with the API surface

## Consequences

### Positive

- **Minimal learning curve**: users already know `net/http` and `(T, error)`
- **No vendor lock-in**: no framework dependencies, no router opinions
- **Small binary impact**: minimal runtime means negligible binary size overhead
- **Composable**: standard Go patterns for middleware, testing, mocking
- **Predictable**: generated code is readable and debuggable — no magic

### Negative

- **No automatic runtime safety net**: if the server returns data that doesn't match the schema, JSON unmarshal may produce zero values or partial data. Note: `encoding/json.Unmarshal` does return errors for malformed JSON (`SyntaxError`) and some type mismatches (`UnmarshalTypeError`), but many schema violations (missing required fields, constraint violations, extra properties) are silently accepted. Calling `Validate()` (ADR-013) catches these.
- **Users must handle cross-cutting concerns themselves**: auth, retries, rate limiting require explicit setup
- **Less "batteries included"** than ogen or commercial generators (Speakeasy, Stainless)

### Risks

- Go's type system is less expressive than TypeScript's — some openapi-fetch patterns (template literal types, conditional types) have no direct Go equivalent. We must find Go-idiomatic alternatives.
- `encoding/json/v2` is still experimental (Go 1.25). We design for `encoding/json` v1 with a migration path to v2.

## References

- [openapi-fetch documentation](https://openapi-ts.dev/openapi-fetch/)
- [openapi-typescript documentation](https://openapi-ts.dev/)
- [openapi-fetch RFC #2204 — Design Philosophy](https://github.com/openapi-ts/openapi-typescript/issues/2204)
