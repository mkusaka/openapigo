# ADR-004: Nullable and Optional Field Handling

## Status

Accepted

## Date

2026-03-01

## Context

OpenAPI schemas express three distinct states for a field:

1. **Present with a value**: `{"name": "Fluffy"}`
2. **Present but null**: `{"name": null}`
3. **Absent (omitted)**: `{}`

This three-state distinction is critical for PATCH semantics:

- Absent field → do not modify
- Null field → explicitly set to null / clear the value
- Value field → update to the provided value

Go's type system does not natively express this three-state distinction:

| Go pattern | Absent | Null | Value | Correct? |
|-----------|--------|------|-------|----------|
| `string` | `""` | `""` | `"x"` | No — conflates all three |
| `*string` | `nil` | `nil` | `ptr("x")` | No — conflates absent and null |
| `json:",omitempty"` | omits zero | omits zero | marshals | No — cannot distinguish null from absent on unmarshal |
| `json:",omitzero"` (Go 1.24) | omits zero | omits zero | marshals | Better — but still conflates absent and null |

Existing tools' approaches:

- **oapi-codegen**: `*T` by default (two-state). Opt-in `nullable.Nullable[T]` for three-state.
- **ogen**: Custom `Opt[T]` / `OptNil[T]` wrappers. Functional but non-idiomatic (seen as verbose).
- **openapi-generator**: `*T` by default. Also generates `NullableString` / `NullableInt32` etc. wrappers (`value *T` + `isSet bool`) for three-state support in the Go target, but these are used only for explicitly nullable properties (`nullable: true` in OAS 3.0) — the general default is still `*T`.

### OpenAPI 3.1 nullable pattern

In OAS 3.1, nullable is expressed as:

```yaml
# OAS 3.0
type: string
nullable: true

# OAS 3.1 (JSON Schema aligned)
anyOf:
  - type: string
  - type: "null"

# or equivalently:
type: [string, "null"]
```

This means nullable fields naturally appear as `anyOf` with a null type variant.

## Decision

### Three Types for Three Patterns

We generate different Go representations based on the OpenAPI schema:

| OpenAPI field | Required? | Nullable? | Go type | JSON tag |
|--------------|-----------|-----------|---------|----------|
| Required, non-nullable | Yes | No | `T` | `json:"name"` |
| Optional, non-nullable | No | No | `*T` | `json:"name,omitzero"` |
| Required, nullable | Yes | Yes | `Nullable[T]` | `json:"name"` |
| Optional, nullable | No | Yes | `Nullable[T]` | `json:"name,omitzero"` |

### Nullable[T] — Three-State Generic Type

We provide a `Nullable[T]` type in the runtime library that correctly represents all three states:

```go
// Nullable represents a value that can be absent, null, or present.
//
//   - Zero value (Nullable[T]{}) represents absent — omitted from JSON via omitzero
//   - Null(T) represents an explicit JSON null
//   - Value(v) represents a present JSON value
type Nullable[T any] struct {
    value T
    set   bool // true if the field was present in JSON (null or value)
    valid bool // true if the field was present AND not null
}

// Value creates a Nullable holding a value.
func Value[T any](v T) Nullable[T] {
    return Nullable[T]{value: v, set: true, valid: true}
}

// Null creates a Nullable representing an explicit null.
func Null[T any]() Nullable[T] {
    return Nullable[T]{set: true, valid: false}
}

// Accessors
func (n Nullable[T]) Get() (T, bool)  { return n.value, n.valid }
func (n Nullable[T]) IsSet() bool     { return n.set }
func (n Nullable[T]) IsNull() bool    { return n.set && !n.valid }
func (n Nullable[T]) IsValue() bool   { return n.valid }
func (n Nullable[T]) IsZero() bool    { return !n.set } // for omitzero

func (n Nullable[T]) MarshalJSON() ([]byte, error) {
    if !n.valid {
        return []byte("null"), nil
    }
    return json.Marshal(n.value)
}

func (n *Nullable[T]) UnmarshalJSON(data []byte) error {
    n.set = true
    if string(data) == "null" {
        n.valid = false
        return nil
    }
    n.valid = true
    return json.Unmarshal(data, &n.value)
}
```

### `IsZero()` and `omitzero` Integration

Go 1.24 introduced the `json:",omitzero"` struct tag, which calls `IsZero() bool` to determine whether to omit a field. Our `Nullable[T].IsZero()` returns `true` when the field is absent (`set == false`), ensuring correct marshal behavior:

| State | `IsZero()` | JSON marshal |
|-------|-----------|-------------|
| Absent | `true` | field omitted |
| Null | `false` | `"field": null` |
| Value | `false` | `"field": "value"` |

This is the correct PATCH semantic: absent fields are omitted, null fields are explicitly serialized.

### anyOf / type-array Null Optimization

When an `anyOf` consists of exactly one schema plus `{type: "null"}`, we optimize it to `Nullable[T]` instead of generating a full anyOf wrapper struct. The same optimization applies to `type: [T, "null"]` (OAS 3.1 shorthand, which is first normalized to anyOf per ADR-009):

```yaml
# This anyOf:
anyOf:
  - type: string
  - type: "null"

# Generates Nullable[string], NOT StringOrNullAnyOf
```

Similarly, for OAS 3.0's `nullable: true`:

```yaml
# OAS 3.0
type: string
nullable: true

# Also generates Nullable[string]
```

This optimization avoids unnecessary wrapper structs for the extremely common nullable pattern.

### Optional Fields with `*T` and `new(expr)`

For optional non-nullable fields, we use `*T`:

```go
type Pet struct {
    Name string  `json:"name"`          // required
    Tag  *string `json:"tag,omitzero"`  // optional
}
```

Go 1.26's `new(expr)` eliminates the need for helper functions when constructing these:

```go
// Go 1.26+: direct construction
pet := Pet{Name: "Fluffy", Tag: new("friendly")}

// Go 1.24-1.25 fallback: ptr[T] helper
func ptr[T any](v T) *T { return &v }
pet := Pet{Name: "Fluffy", Tag: ptr("friendly")}
```

We generate constructor functions that leverage `new(expr)` (1.26+) or `ptr[T]` (1.24-1.25) for ergonomic struct creation.

**Known limitation: `*T` conflates absent and null**. When unmarshaling JSON, both `{}` (absent field) and `{"tag": null}` produce `Tag == nil`. Go's `encoding/json` does not distinguish these cases for pointer types. This is acceptable for non-nullable optional fields (the schema says the field cannot be null, so a conforming server should never send `null`). For fields where the absent/null distinction matters, use `Nullable[T]` — which is the mapping for nullable fields (see table above). **Struct reuse caveat for `*T`**: When unmarshaling into the **same** struct instance, `encoding/json` does NOT reset pointer fields for absent keys — an absent field **preserves the existing pointer value** from a prior unmarshal. Only `{"tag": null}` sets the pointer to `nil`. This means reusing a struct across multiple `json.Unmarshal` calls can leave stale data in `*T` fields. Since `*T` already conflates absent and null by design, this is acceptable for our mapping — the struct reuse concern is more relevant for `Nullable[T]` (see below), which does generate a zeroing `UnmarshalJSON`. Users who reuse `*T`-only structs across unmarshal calls should zero the struct first (`v = MyStruct{}`).

### Enum Fields

For enum fields, the generated named type is used directly:

```yaml
status:
  type: string
  enum: [active, inactive, pending]
```

```go
type PetStatus string

const (
    PetStatusActive   PetStatus = "active"
    PetStatusInactive PetStatus = "inactive"
    PetStatusPending  PetStatus = "pending"
)
```

Optional enum fields use `*PetStatus` with `omitzero`. Nullable enum fields use `Nullable[PetStatus]`.

### Summary of Type Mapping

```
Required + non-nullable  →  T
Optional + non-nullable  →  *T          (json:",omitzero")
Required + nullable      →  Nullable[T]
Optional + nullable      →  Nullable[T] (json:",omitzero")
anyOf: [T, null]         →  Nullable[T] (optimized, no wrapper struct)
```

## Consequences

### Positive

- **Three-state is correctly represented for `Nullable[T]` fields**: absent, null, and value are all distinguishable — critical for PATCH APIs. Note: `*T` fields (optional non-nullable) only distinguish two states (nil vs value); see "Known limitation" above.
- **`omitzero` integration**: leverages Go 1.24's best practice for optional field omission
- **`new(expr)` integration**: Go 1.26's pointer construction makes optional fields ergonomic
- **anyOf null optimization**: the most common anyOf pattern does not produce unnecessary wrapper types
- **Consistent**: one type (`Nullable[T]`) for all nullable fields regardless of OAS version (3.0 `nullable: true` or 3.1 `anyOf` with null)

### Negative

- **`Nullable[T]` is not stdlib**: users must import our runtime library for three-state fields. However, this is minimal — a single generic type.
- **Learning curve**: developers accustomed to `*T` must learn when `Nullable[T]` is used and why
- **`encoding/json` v1 behavior**: `Nullable[T].UnmarshalJSON` is called when the JSON field is present (including when the value is `null`), but is **not** called when the field is absent. This is the correct and expected behavior — an absent field leaves `Nullable[T]` at its zero value (`set == false`), which represents the "absent" state. The `omitzero` tag + `IsZero()` ensures correct round-trip: absent fields are omitted during marshal.
- **Struct reuse invariant**: `Nullable[T]` fields retain their `set=true` state across multiple `json.Unmarshal` calls into the **same** struct instance (because `encoding/json` does not call `UnmarshalJSON` for absent fields, so `set` is never reset). To enforce this **mechanically** (not just by documentation), the generator produces a custom `UnmarshalJSON` for every struct containing `Nullable[T]` fields that resets the struct to its zero value before decoding. **Scope**: This reset is generated only for structs containing `Nullable[T]`, not for structs with only `*T` optional fields. For `*T`-only structs, the absent/null conflation already exists by design (per ADR-004's type mapping table), so there is no three-state information to lose. On a **freshly zeroed** struct, both absent and null produce `nil`. On a **reused** struct, absent preserves the prior value while null sets it to `nil` (see "Struct reuse caveat" above) — but since `*T` already conflates absent and null, the worst case is stale data, not a semantic misclassification. Users who reuse `*T`-only structs should zero the struct first (`v = MyStruct{}`):

  ```go
  func (v *MyStruct) UnmarshalJSON(data []byte) error {
      // Reset to zero value — clears stale Nullable[T].set flags from prior unmarshal.
      *v = MyStruct{}
      type plain MyStruct
      return json.Unmarshal(data, (*plain)(v))
  }
  ```

  This ensures that even if user code reuses a struct instance, absent `Nullable[T]` fields correctly report `set=false`. This is particularly critical for PATCH semantics: without the reset, reusing a struct from a previous unmarshal would incorrectly indicate fields as "present" when they are absent in the new input.

  **Decoder policy limitation**: The `type plain MyStruct` alias trick combined with `json.Unmarshal` in the generated method creates a **new** decoder internally, which does not inherit policies from an outer `json.Decoder` (e.g., `DisallowUnknownFields()`). This means strict decoding policies set by the caller are silently ignored for structs with this generated `UnmarshalJSON`. This is an inherent limitation of Go's `encoding/json` v1 architecture — custom `UnmarshalJSON` methods cannot access the outer decoder's options. **Mitigation**: Users requiring strict decoding should use `Validate()` (which can check for unevaluated properties per ADR-012) rather than relying on `DisallowUnknownFields()`. When `encoding/json/v2` stabilizes, its options model propagates through the call chain, resolving this limitation.
- **Required nullable field absence and re-marshal semantic shift**: When a required nullable field is absent from JSON, `Nullable[T]` remains at its zero value (`set=false`). This absence is **not** caught by `UnmarshalJSON` — it is enforced by `Validate()`, which checks that required fields have `set=true`. For strict enforcement at unmarshal time, use `--validate-on-unmarshal` (ADR-013). **Re-marshal risk**: If a required nullable field is absent (schema violation) and the struct is marshaled without validation, `MarshalJSON` produces `null` (because `set=false` → `IsZero()=true` → omitted by `omitzero`; but if `omitzero` is not present for required fields, `!valid` → `null`). This silently converts "absent" (schema violation) into "explicit null" (valid but different semantic). For PATCH-sensitive APIs, always call `Validate()` after unmarshal (or use `--validate-on-unmarshal`) to catch absent required fields before re-marshaling.

### Risks

- **Go version requirement**: The `omitzero` struct tag requires **Go 1.24+**. The generator enforces this via two mechanisms: (1) checks `go.mod`'s `go` directive and emits an error if the target version is below 1.24: `ERROR: openapigo requires Go 1.24+ for omitzero support. Your go.mod specifies go X.Y.`, and (2) every generated file includes a `//go:build go1.24` build constraint as a compile-time safety net, so that building with Go < 1.24 produces a clear build error rather than silently ignoring `omitzero` (which would cause absent `Nullable` fields to marshal as `null` instead of being omitted, breaking wire compatibility).
- If `encoding/json/v2` changes the semantics of `omitzero` or the `IsZero()` protocol before stabilization, we may need to adapt. We pin to documented Go 1.24+ behavior.
- Some users may prefer `*T` universally for simplicity. We could offer a CLI flag (`--nullable-as-pointer`) to downgrade `Nullable[T]` to `*T` for users who don't need three-state semantics.
