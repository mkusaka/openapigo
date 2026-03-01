# ADR-005: Minimum Go Version and Feature Utilization

## Status

Accepted

## Date

2026-03-01

## Context

Go releases new versions every six months. Each version introduces features that can improve the quality and ergonomics of generated code. We must choose a minimum Go version that balances access to modern features against compatibility with users' existing Go toolchains.

### Feature availability by version

| Feature | Version | Impact on this project |
|---------|---------|----------------------|
| Generics (basic) | 1.18 | Foundation for `Nullable[T]`, composition types |
| `slices`/`maps`/`cmp` packages | 1.21 | Utility functions in runtime library |
| `net/http` enhanced routing | 1.22 | Not directly used (client-side tool) |
| `iter.Seq[T]`/`iter.Seq2[K,V]` | 1.23 | Pagination iterators |
| Generic type aliases | 1.24 | Flexible type definitions in generated code |
| `json:",omitzero"` struct tag | 1.24 | Correct optional field omission |
| `encoding/json/v2` (experimental) | 1.25 | Future migration target |
| `new(expr)` | 1.26 | Ergonomic pointer construction |
| `errors.AsType[E]` | 1.26 | Type-safe error handling in generated code |
| Self-referential generics | 1.26 | Builder patterns, fluent APIs |
| Green Tea GC (default) | 1.26 | Free performance improvement |

### Go version support policy in the ecosystem

The Go project itself supports the two most recent major releases. As of March 2026, that means Go 1.25 and 1.26 are supported. Most actively-maintained Go projects target `N-1` or `N-2`.

## Decision

### Minimum Go Version: 1.24

We set the minimum required Go version to **1.24**, with generated code that **optionally leverages 1.26 features** when available.

**Why 1.24 and not 1.26**:

- 1.24 provides the two most critical features for correct generated code: `json:",omitzero"` and generic type aliases
- 1.24 was released February 2025, giving the ecosystem over a year to adopt
- Requiring the latest release (1.26, February 2026) would exclude users on the previous supported release (1.25)

**Why not lower than 1.24**:

- `json:",omitzero"` (1.24) is essential for correct optional/nullable field marshaling. Without it, we would need to generate custom `MarshalJSON` for every struct with optional fields, dramatically increasing generated code volume.
- Generic type aliases (1.24) enable cleaner generated type definitions.

### Feature Utilization Plan

#### Always Used (1.24+ baseline)

**`json:",omitzero"` struct tag**

All optional fields use `omitzero` instead of `omitempty`:

```go
type Pet struct {
    Name string          `json:"name"`
    Tag  *string         `json:"tag,omitzero"`
    Age  Nullable[int]   `json:"age,omitzero"`
}
```

`omitzero` calls `IsZero() bool` when available (our `Nullable[T]` implements it), and checks for the Go zero value otherwise. This is strictly more correct than `omitempty` for API types.

**Generic type aliases**

Used for schema aliases and composition simplifications:

```go
type Pets = []Pet
type PetMap[V any] = map[string]V
```

**Generics for runtime types**

`Nullable[T]`, composition wrappers, and error types all use generics:

```go
type Nullable[T any] struct { /* ... */ }
type OneOfNoMatchError struct { /* ... */ }
```

**`slices`/`maps` packages**

Used in the runtime library for operations like field key extraction, variant matching, etc.

**`iter.Seq[T]` for pagination**

When an API endpoint supports pagination, we generate an iterator function:

```go
func (c *Client) ListPetsIter(ctx context.Context, params ListPetsParams) iter.Seq2[Pet, error] {
    return func(yield func(Pet, error) bool) {
        cursor := params.Cursor
        for {
            resp, err := c.ListPets(ctx, ListPetsParams{
                Cursor: cursor,
                Limit:  params.Limit,
            })
            if err != nil {
                yield(Pet{}, err)
                return
            }
            for _, pet := range resp.Items {
                if !yield(pet, nil) {
                    return
                }
            }
            if resp.NextCursor == nil {
                return
            }
            cursor = resp.NextCursor
        }
    }
}
```

#### Leveraged When Available (1.26+)

**`new(expr)` in constructors**

Generated convenience constructors use `new(expr)` when the `go` directive in `go.mod` is 1.26+:

```go
// go 1.26+
func NewPet(name string) Pet {
    return Pet{Name: name}
}
func NewPetWithTag(name, tag string) Pet {
    return Pet{Name: name, Tag: new(tag)}
}
```

```go
// go 1.24-1.25 fallback: use a helper
func NewPet(name string) Pet {
    return Pet{Name: name}
}
func NewPetWithTag(name, tag string) Pet {
    return Pet{Name: name, Tag: ptr(tag)}
}
func ptr[T any](v T) *T { return &v }
```

The generator reads the `go` directive from the target project's `go.mod` to decide which pattern to emit.

**`errors.AsType[E]` in error handling**

Generated error extraction code uses the generic form when targeting 1.26+:

```go
// go 1.26+
if apiErr, ok := errors.AsType[*APIError](err); ok {
    switch apiErr.StatusCode {
    case 404:
        // ...
    }
}

// go 1.24-1.25 fallback
var apiErr *APIError
if errors.As(err, &apiErr) {
    // ...
}
```

**Self-referential generics for builder patterns**

If we introduce a request builder pattern, self-referential generics (1.26) enable fluent method chaining that returns the concrete type:

```go
type RequestBuilder[B RequestBuilder[B]] interface {
    WithHeader(key, value string) B
    WithQuery(key, value string) B
}
```

This is optional and may not be generated in the initial version.

### encoding/json/v2 Readiness

`encoding/json/v2` is experimental in Go 1.25 and not yet stable. We design our runtime types to be compatible with both v1 and v2:

- `MarshalJSON` / `UnmarshalJSON` methods work with both versions
- We do **not** use `MarshalerTo` / `UnmarshalerFrom` (v2-only interfaces) yet
- When v2 stabilizes, we can add `MarshalJSONTo` / `UnmarshalJSONFrom` methods for better streaming performance without breaking v1 compatibility
- **Migration safety principles**: When v2 stabilizes, migration must follow these rules:
  1. **Default is v1-compatible behavior**: the generated code's default JSON semantics must not change when switching to v2. Use v2's compatibility options (`json.DefaultOptionsV1()`) as the baseline.
  2. **v2-specific semantics are explicit opt-in**: new v2 defaults (case-sensitive matching, nil-slice-as-empty-array, duplicate key rejection) are only enabled via explicit CLI flag (`--json-v2-semantics`) or per-field annotation.
  3. **Compatibility test gate**: no default semantic change is shipped until the full test suite passes with both v1 and v2 backends under identical inputs, confirming wire-format equivalence.
- Key v2 defaults we anticipate benefiting from (once explicitly opted in):
  - Case-sensitive field matching (more correct for API types)
  - Nil slices marshal as `[]` (not `null`)
  - Duplicate key detection

### Features NOT Used

| Feature | Why not |
|---------|---------|
| `net/http` enhanced routing (1.22) | Client-side tool — server routing not relevant |
| `unique.Handle[T]` (1.23) | Premature optimization; enum constants are sufficient |
| `testing/synctest` (1.25) | Internal testing only, not generated code |
| `crypto/*` packages | Not relevant to API client generation |
| `sync.WaitGroup.Go` (1.25) | Not needed in generated client code |

## Consequences

### Positive

- **1.24 minimum is pragmatic**: covers the vast majority of actively-maintained Go projects while providing essential features
- **Conditional 1.26 features**: users on the latest Go get the best ergonomics without excluding others
- **`omitzero` is always available**: the single most impactful feature for correct API type generation
- **json/v2 ready**: when it stabilizes, migration is straightforward

### Negative

- **Users on Go 1.23 or earlier cannot use this tool**: they must upgrade to at least 1.24
- **Conditional generation adds complexity**: the generator must read `go.mod` and emit different code for different Go versions
- **`new(expr)` syntax is unfamiliar**: some developers may be confused by `new("value")` in generated code

### Risks

- If `json:",omitzero"` behavior changes in a future Go release, generated code may need updates. This is unlikely given Go's compatibility promise.
- `encoding/json/v2` may stabilize with interface changes that require significant adaptation. We monitor the json/v2 working group's progress and design for forward compatibility.

## References

- [Go 1.24 Release Notes](https://go.dev/doc/go1.24)
- [Go 1.26 Release Notes](https://go.dev/doc/go1.26)
- [encoding/json/v2 experimental blog post](https://go.dev/blog/jsonv2-exp)
- [errors.AsType proposal](https://github.com/golang/go/issues/51945)
