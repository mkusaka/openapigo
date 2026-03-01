# ADR-008: Enum, Const, and Default Values

## Status

Accepted

## Date

2026-03-01

## Context

Go does not have a native enum type. OpenAPI uses `enum` and `const` as validation keywords that constrain values, and `default` as an annotation keyword (per JSON Schema 2020-12 §9.2, `default` is not a validation constraint but provides a suggested value for documentation and code generation):

```yaml
# enum: value must be one of the listed values
status:
  type: string
  enum: [active, inactive, pending]

# const: value must be exactly this
version:
  type: string
  const: "2.0"

# default: suggested value when omitted (annotation, not validation)
pageSize:
  type: integer
  default: 20
```

### Challenges in Go

1. **No enum type**: Go's `const` + `iota` pattern provides named constants but no compiler enforcement that a variable only holds valid values
2. **No exhaustive switch**: Go does not warn if a switch on a "enum" type misses a case
3. **Zero value conflict**: Go's zero value for `string` is `""`, which may or may not be a valid enum value. For `int`, `0` may or may not be valid.
4. **Default values**: Go struct fields are zero-initialized. There is no built-in mechanism to distinguish "user passed zero" from "user did not set this field."

### How existing tools handle enums

- **oapi-codegen**: Named string type + constants. No validation. `type PetStatus string; const PetStatusActive PetStatus = "active"`
- **ogen**: Named type + constants + generated `Validate()` method that checks against allowed values
- **openapi-generator**: `*string` with no typing — loses all enum information
- **openapi-typescript**: String literal union type (`"active" | "inactive" | "pending"`) — perfectly type-safe

## Decision

### Enum → Named Type + Constants + Validate Method

For string enums:

```yaml
PetStatus:
  type: string
  enum: [active, inactive, pending]
```

```go
// Named type
type PetStatus string

// Constants
const (
    PetStatusActive   PetStatus = "active"
    PetStatusInactive PetStatus = "inactive"
    PetStatusPending  PetStatus = "pending"
)

// All valid values (for iteration and validation)
func PetStatusValues() []PetStatus {
    return []PetStatus{
        PetStatusActive,
        PetStatusInactive,
        PetStatusPending,
    }
}

// Validate checks if the value is a known enum member
func (v PetStatus) Validate() error {
    switch v {
    case PetStatusActive, PetStatusInactive, PetStatusPending:
        return nil
    default:
        return &InvalidEnumError{
            Type:    "PetStatus",
            Value:   string(v),
            Allowed: []string{"active", "inactive", "pending"},
        }
    }
}
```

### Integer Enums

```yaml
Priority:
  type: integer
  enum: [1, 2, 3]
```

```go
type Priority int

const (
    PriorityLow    Priority = 1
    PriorityMedium Priority = 2
    PriorityHigh   Priority = 3
)

func PriorityValues() []Priority {
    return []Priority{PriorityLow, PriorityMedium, PriorityHigh}
}

func (v Priority) Validate() error {
    switch v {
    case PriorityLow, PriorityMedium, PriorityHigh:
        return nil
    default:
        return &InvalidEnumError{
            Type:    "Priority",
            Value:   fmt.Sprintf("%d", v),
            Allowed: []string{"1", "2", "3"},
        }
    }
}
```

### Enum Constant Naming

We generate constant names by combining the type name with a cleaned-up version of the value:

| Type | Value | Constant name |
|------|-------|--------------|
| `PetStatus` | `"active"` | `PetStatusActive` |
| `PetStatus` | `"in-progress"` | `PetStatusInProgress` |
| `PetStatus` | `"404_not_found"` | `PetStatus404NotFound` |
| `Priority` | `1` | `PriorityLow` (if `x-enum-varnames` exists) |
| `Priority` | `1` | `Priority1` (fallback) |

**Vendor extension support**: If `x-enum-varnames` or `x-enum-descriptions` extensions are present, we use them for constant names and Go doc comments:

```yaml
Priority:
  type: integer
  enum: [1, 2, 3]
  x-enum-varnames: [Low, Medium, High]
  x-enum-descriptions:
    - Low priority task
    - Medium priority task
    - High priority task
```

```go
// Low priority task
const PriorityLow Priority = 1
// Medium priority task
const PriorityMedium Priority = 2
// High priority task
const PriorityHigh Priority = 3
```

### Enum with Empty String

When `""` is a valid enum value, the zero value of the Go string type is a valid enum member. This requires no special handling — it's simply another constant:

```go
const PetStatusEmpty PetStatus = ""
```

### Nullable Enums

Nullable enums use `Nullable[PetStatus]` (per ADR-004):

```yaml
status:
  anyOf:
    - $ref: '#/components/schemas/PetStatus'
    - type: "null"
```

```go
type Pet struct {
    Status Nullable[PetStatus] `json:"status,omitzero"`
}
```

### Const → Typed Constant

The `const` keyword is available in **OAS 3.1+** (JSON Schema 2020-12). In **OAS 3.0.x**, `const` is not part of the Schema Object; the same effect is achieved with a single-value `enum: [value]`. The generator normalizes `enum: [singleValue]` to the same output as `const: singleValue` regardless of OAS version.

**OAS 3.0 + `const` keyword**: If a `const` keyword appears in an OAS 3.0.x spec, the generator emits a **warning** (not an error) and treats it as `enum: [value]` for pragmatic compatibility: `WARN: 'const' is not part of OAS 3.0.x Schema Object; treating as enum with single value. Consider using 'enum: [value]' for spec compliance.` This allows generators to handle real-world specs that use `const` despite the OAS version, while still alerting the user to the deviation. **Detection guarantee**: The generator's own JSON/YAML parser preserves all keys (it does not filter by OAS version during parsing). The `const` keyword is always detected regardless of OAS version. **Third-party parser risk**: If a third-party parser that strips unknown keywords is used as a pre-processing step, `const` could be silently lost. To mitigate this, the generator validates that its input was parsed by a keyword-preserving parser: when the `--input` is a file path (not a pre-parsed AST), the generator uses its built-in parser exclusively. When receiving pre-parsed input via programmatic API, the generator emits a warning: `WARN: Using pre-parsed schema input. Ensure the parser preserves all JSON Schema keywords including 'const'. Silent keyword loss cannot be detected.` Users who pipe through tools like `swagger-cli bundle` should verify keyword preservation in their pipeline.

The `const` keyword in OpenAPI means the value must be exactly the specified value. For **primitive types** (string, integer, number, boolean), we generate a Go constant:

```yaml
version:
  type: string
  const: "2.0"
```

```go
const VersionConst = "2.0"
```

When `const` appears inline in a schema property:

```yaml
Pet:
  type: object
  properties:
    species:
      type: string
      const: "cat"
```

```go
type Pet struct {
    Species string `json:"species"` // always "cat"
}

// Constructor enforces the const value
func NewPet() Pet {
    return Pet{Species: "cat"}
}
```

**Optional field with const**: When a `const` property is not in the `required` array, the field is optional (may be absent) but when present must equal the const value. The Go type uses a pointer:

```go
type Pet struct {
    Species *string `json:"species,omitzero"` // when present, must be "cat"
}
```

The constructor sets it, but users can leave it nil (absent). The `Validate()` method checks the const constraint **only when the field is non-nil** (present).

**Important caveat**: `*string` conflates absent and null (per ADR-004). A JSON input `{"species": null}` sets the pointer to `nil`, indistinguishable from absent. For non-nullable fields, `encoding/json` unmarshal to `*string` treats null as `nil`. This means a non-nullable `const` field cannot distinguish between "absent" (valid — field is optional) and "null" (invalid — field is non-nullable) using the pointer alone. **Null detection for non-nullable fields**: The `Validate()` method for non-nullable optional fields with `const` performs two checks: (1) a non-null check (when the schema does not allow null), and (2) the const value check. Both are combined in the same method. The non-null check uses the struct's `rawFieldKeys` (populated by `UnmarshalJSON`) to distinguish "field absent" from "field present as null". When the key is present in `rawFieldKeys` but the pointer is `nil`, `Validate()` reports a null violation. Per ADR-004/ADR-012, `rawFieldKeys` is always generated for structs containing non-nullable `*T` fields when `Validate()` is generated (not `--skip-validation`), so null detection is available whenever validation is available. The const check itself only fires for non-nil (actually-present) values:

```go
func (v Pet) Validate() error {
    if v.Species != nil && *v.Species != "cat" {
        return &ValidationError{
            Field:      "species",
            Constraint: "const",
            Message:    fmt.Sprintf("must be %q, got %q", "cat", *v.Species),
        }
    }
    return nil
}
```

**Complex-type const** (object/array): Go's `const` keyword only supports primitive types. For `const` with object or array values, we generate an **unexported function** that returns the const value (using a function instead of a `var` prevents external mutation of the reference value) and enforce the constraint in `Validate()`. The struct also includes a `rawJSON []byte` field (populated by `UnmarshalJSON` with the full input bytes) to enable exact JSON-level comparison — without this, `encoding/json`'s silent dropping of unknown keys would cause false positives (see validation code below):

```yaml
config:
  type: object
  const: { "mode": "strict", "version": 2 }
```

```go
// Generated as an unexported package-level function returning the const value.
// Using a function (not a var) prevents external mutation of the reference value.
func configConstValue() Config { return Config{Mode: "strict", Version: 2} }

func (v Config) Validate() error {
    // JSON-level semantic comparison for complex const.
    // JSON Schema `const` requires exact JSON value equality, NOT Go struct equality.
    //
    // CRITICAL: We compare against the RAW JSON input, not the re-marshaled Go struct.
    // encoding/json silently drops unknown keys during unmarshal into a struct, so
    // re-marshaling the struct loses information: input {"mode":"strict","extra":1}
    // would marshal back to {"mode":"strict"}, falsely passing the const check.
    //
    // The rawJSON field is populated by UnmarshalJSON (the full JSON input bytes).
    // When the struct was constructed in Go code (not from JSON), rawJSON is nil,
    // and we fall back to marshal-based comparison (acceptable because Go-constructed
    // values cannot have extra keys).
    //
    // Comparison uses jsonEqual() (same approach as marshalMerge in ADR-002):
    // unmarshal both into `any` via json.NewDecoder with UseNumber(), then compare
    // with jsonEqual() which handles key ordering, whitespace, and numeric
    // representation differences (1 vs 1.0 vs 1e0).
    constJSON, _ := json.Marshal(configConstValue())
    var actualJSON []byte
    if v.rawJSON != nil {
        actualJSON = v.rawJSON
    } else {
        var err error
        actualJSON, err = json.Marshal(v)
        if err != nil {
            return fmt.Errorf("config: failed to marshal for const comparison: %w", err)
        }
    }
    if !jsonEqual(constJSON, actualJSON) {
        return &ValidationError{
            Field: "config", Constraint: "const",
            Message: "must equal the const value",
        }
    }
    return nil
}
```

**Const validation in `Validate()`**: Since Go's type system cannot enforce a specific string value, the `const` constraint is enforced at runtime via the `Validate()` method (per ADR-013):

```go
func (v Pet) Validate() error {
    var errs []*ValidationError
    if v.Species != "cat" {
        errs = append(errs, &ValidationError{
            Field:      "species",
            Constraint: "const",
            Message:    fmt.Sprintf("must be %q, got %q", "cat", v.Species),
            Value:      v.Species,
        })
    }
    // ... other validations
    if len(errs) > 0 {
        return &ValidationErrors{Errors: errs}
    }
    return nil
}
```

For discriminated unions, `const` on the discriminator property is automatically handled by the discriminator strategy (ADR-003) — the switch case matches the const value.

### Default Values

`default` is an **annotation keyword** in JSON Schema 2020-12 (§9.2) — it suggests a value for when a field is omitted but does not mandate runtime behavior. The generator uses it for code generation at two levels:

#### Level 1: Documentation (always)

The default value is always included as a Go doc comment:

```go
type ListPetsParams struct {
    // PageSize is the number of items per page.
    // Default: 20
    PageSize *int `json:"page_size,omitzero"`
}
```

#### Level 2: Constructor Functions (always generated when defaults exist)

We generate constructor functions that apply default values:

```yaml
ListPetsParams:
  type: object
  properties:
    page_size:
      type: integer
      default: 20
    sort_by:
      type: string
      default: "created_at"
      enum: [created_at, updated_at, name]
```

```go
type ListPetsParams struct {
    // Default: 20
    PageSize *int `json:"page_size,omitzero"`
    // Default: "created_at"
    SortBy *ListPetsParamsSortBy `json:"sort_by,omitzero"`
}

// NewListPetsParams creates a ListPetsParams with default values applied.
// Go 1.26+: uses new(expr) syntax
//   func NewListPetsParams() ListPetsParams {
//       return ListPetsParams{
//           PageSize: new(20),
//           SortBy:   new(ListPetsParamsSortByCreatedAt),
//       }
//   }

// Go 1.24-1.25 (uses ptr[T] helper from ADR-005):
func NewListPetsParams() ListPetsParams {
    return ListPetsParams{
        PageSize: ptr(20),
        SortBy:   ptr(ListPetsParamsSortByCreatedAt),
    }
}
```

#### What We Do NOT Do

We do **not** automatically apply defaults during JSON unmarshaling. Reasons:

1. It conflates "server didn't send this field" with "apply default" — the user may want to distinguish these
2. It makes generated `UnmarshalJSON` more complex for every struct with defaults
3. It's inconsistent with the openapi-fetch philosophy (thin wrapper, no implicit behavior)

Users who want defaults applied on unmarshal can call the constructor first and then unmarshal into it (existing values are preserved for absent fields in JSON):

```go
params := NewListPetsParams() // defaults applied
json.Unmarshal(data, &params) // only overrides fields present in JSON
```

**Interaction with ADR-004 (Nullable zero-reset)**: Structs containing `Nullable[T]` fields have a generated `UnmarshalJSON` that performs `*v = MyStruct{}` (zero-reset) at the start (per ADR-004). This zero-reset wipes constructor-applied defaults before decoding. To reconcile: when a struct has **both** `Nullable[T]` fields **and** fields with `default` values, the generated `UnmarshalJSON` re-applies defaults for **absent** fields after decoding, using `rawFieldKeys` tracking to determine which fields were present in the JSON input:

```go
func (v *ListPetsParams) UnmarshalJSON(data []byte) error {
    *v = ListPetsParams{} // zero reset (ADR-004: Nullable correctness)
    type plain ListPetsParams
    if err := json.Unmarshal(data, (*plain)(v)); err != nil {
        return err
    }
    var raw map[string]json.RawMessage
    if err := json.Unmarshal(data, &raw); err != nil {
        return err
    }
    v.rawFieldKeys = make([]string, 0, len(raw))
    for key := range raw {
        v.rawFieldKeys = append(v.rawFieldKeys, key)
    }
    // Re-apply defaults for absent fields (not present in JSON input)
    if _, ok := raw["page_size"]; !ok {
        v.PageSize = ptr(20)
    }
    if _, ok := raw["sort_by"]; !ok {
        v.SortBy = ptr(ListPetsParamsSortByCreatedAt)
    }
    return nil
}
```

For structs **without** `Nullable[T]` fields (no generated `UnmarshalJSON`), the constructor-then-unmarshal pattern works directly as described above. The default re-application is only generated when the zero-reset conflict exists.

### Enum Validation Strategy

Validation is **opt-in at the call site**, consistent with ADR-001 (no runtime validation by default):

```go
pet, err := client.GetPet(ctx, id)
if err != nil {
    return err
}

// Opt-in validation
if err := pet.Status.Validate(); err != nil {
    log.Warn("unknown pet status", "status", pet.Status)
}
```

The `Validate()` method is generated for every enum type but never called automatically by generated code. This allows APIs to evolve (add new enum values) without breaking existing clients.

### Open vs Closed Enums

By default, enums are treated as **open** — unknown values are accepted at runtime (the named type is just a `string` or `int` under the hood). The `Validate()` method enables opt-in strictness.

A CLI flag `--strict-enums` could generate `UnmarshalJSON` methods that reject unknown values, but this is not the default because it breaks forward compatibility when servers add new enum members.

## Consequences

### Positive

- **Named types provide IDE support**: autocomplete suggests valid enum values
- **`Values()` function enables iteration**: useful for building UIs, validation, documentation
- **`Validate()` is opt-in**: clients don't break when servers add new enum values
- **Constructors apply defaults**: ergonomic and explicit, no hidden behavior
- **Vendor extensions supported**: `x-enum-varnames` produces clean Go constant names

### Negative

- **No compile-time enum exhaustiveness**: Go cannot enforce that a switch covers all enum cases. Third-party linters (like `exhaustive`) can partially address this.
- **No compile-time enum assignment safety**: `PetStatus("invalid")` compiles without error. This is fundamental to Go's type system.
- **Default values require constructor usage**: users who construct structs with `{}` get zero values, not defaults. This is a documentation/education concern.

### Risks

- Integer enums without `x-enum-varnames` produce unhelpful constant names (`Priority1`, `Priority2`). We document this and recommend the extension.
- Boolean "enums" (`enum: [true, false]`) are degenerate — we generate `bool` with no enum wrapper.
