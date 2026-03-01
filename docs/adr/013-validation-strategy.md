# ADR-013: Validation Constraints and Validate() Strategy

## Status

Accepted

## Date

2026-03-01

## Context

OpenAPI / JSON Schema defines numerous validation constraints that restrict values beyond their type:

| Category | Keywords |
|----------|----------|
| String | `minLength`, `maxLength`, `pattern` |
| Number | `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `multipleOf` |
| Array | `minItems`, `maxItems`, `uniqueItems` |
| Object | `minProperties`, `maxProperties` |
| Enum | `enum` (covered in ADR-008) |
| Composition | `not` (covered in ADR-002), `if/then/else` (covered in ADR-010) |
| Schema-level | `unevaluatedProperties`, `unevaluatedItems` (covered in ADR-012) |
| Field presence | `required`, `dependentRequired` (covered in ADR-010) |

These constraints do **not** affect the Go type (a `string` with `maxLength: 100` is still `string`). The question is whether and how to provide runtime validation.

### Design tension

ADR-001 establishes a "thin wrapper, no *automatic* runtime validation" philosophy inspired by openapi-fetch. However:

1. openapi-fetch operates in TypeScript where many constraints can be expressed at the type level (literal types, template literals, branded types). Go cannot express `string` with `maxLength: 100` at the type level.
2. Go is used for servers as well as clients — server-side input validation is a common need.
3. Users of all existing Go generators (oapi-codegen, ogen) expect some form of validation.

The resolution: **generate validation code but never call it automatically**. The user opts in by calling `Validate()`.

## Decision

### Unified Validate() Interface

Every generated type that has any validation constraints implements a common interface:

```go
// In the runtime library
type Validatable interface {
    Validate() error
}
```

This allows generic validation of any generated type:

```go
func validateAll(values ...Validatable) error {
    var errs []error
    for _, v := range values {
        if err := v.Validate(); err != nil {
            errs = append(errs, err)
        }
    }
    return errors.Join(errs...)
}
```

### Validation Error Types

All validation errors share a common structure:

```go
// Base validation error
type ValidationError struct {
    Field      string // JSON field path (e.g., "address.zip_code")
    Constraint string // constraint name (e.g., "maxLength")
    Message    string // human-readable message
    Value      any    // the invalid value (for debugging)
}

func (e *ValidationError) Error() string {
    return fmt.Sprintf("validation failed: %s.%s: %s", e.Field, e.Constraint, e.Message)
}

// Collection of validation errors
type ValidationErrors struct {
    Errors []*ValidationError
}

func (e *ValidationErrors) Error() string {
    // ...
}

// Implements Unwrap() []error for errors.Is/As compatibility
func (e *ValidationErrors) Unwrap() []error {
    // ...
}
```

### String Constraints

```yaml
name:
  type: string
  minLength: 1
  maxLength: 100
  pattern: "^[a-zA-Z]"
```

```go
func (v CreatePetRequest) Validate() error {
    var errs []*ValidationError

    // name: minLength=1 (Unicode character count per JSON Schema)
    if utf8.RuneCountInString(v.Name) < 1 {
        errs = append(errs, &ValidationError{
            Field:      "name",
            Constraint: "minLength",
            Message:    "length must be >= 1, got 0",
            Value:      v.Name,
        })
    }
    // name: maxLength=100 (Unicode character count per JSON Schema)
    if utf8.RuneCountInString(v.Name) > 100 {
        errs = append(errs, &ValidationError{
            Field:      "name",
            Constraint: "maxLength",
            Message:    fmt.Sprintf("length must be <= 100, got %d", utf8.RuneCountInString(v.Name)),
            Value:      v.Name,
        })
    }
    // name: pattern=^[a-zA-Z]
    if !patternName.MatchString(v.Name) {
        errs = append(errs, &ValidationError{
            Field:      "name",
            Constraint: "pattern",
            Message:    fmt.Sprintf("must match pattern %q", "^[a-zA-Z]"),
            Value:      v.Name,
        })
    }

    if len(errs) > 0 {
        return &ValidationErrors{Errors: errs}
    }
    return nil
}

// Pattern compiled once at package level
var patternName = regexp.MustCompile(`^[a-zA-Z]`)
```

**String length**: We use `len()` (byte length) for ASCII-safe patterns and `utf8.RuneCountInString()` when the schema or context suggests Unicode content. The JSON Schema spec defines `minLength`/`maxLength` in terms of Unicode characters, so the default is `utf8.RuneCountInString()`:

```go
if utf8.RuneCountInString(v.Name) < 1 { // correct per JSON Schema
```

### Number Constraints

```yaml
age:
  type: integer
  minimum: 0
  maximum: 150
price:
  type: number
  exclusiveMinimum: 0
  multipleOf: 0.01
```

```go
func (v Product) Validate() error {
    var errs []*ValidationError

    // age: minimum=0, maximum=150
    if v.Age != nil {
        if *v.Age < 0 {
            errs = append(errs, &ValidationError{
                Field:      "age",
                Constraint: "minimum",
                Message:    fmt.Sprintf("must be >= 0, got %d", *v.Age),
            })
        }
        if *v.Age > 150 {
            errs = append(errs, &ValidationError{
                Field:      "age",
                Constraint: "maximum",
                Message:    fmt.Sprintf("must be <= 150, got %d", *v.Age),
            })
        }
    }

    // price: exclusiveMinimum=0
    if v.Price < 0 || v.Price == 0 {
        errs = append(errs, &ValidationError{
            Field:      "price",
            Constraint: "exclusiveMinimum",
            Message:    fmt.Sprintf("must be > 0, got %f", v.Price),
        })
    }
    // price: multipleOf=0.01
    // NOTE: math.Remainder has IEEE 754 precision issues for floats.
    // e.g., math.Remainder(0.3, 0.01) != 0 due to floating point representation.
    // We use an epsilon-based comparison to handle this correctly.
    if !isMultipleOf(v.Price, 0.01) {
        errs = append(errs, &ValidationError{
            Field:      "price",
            Constraint: "multipleOf",
            Message:    fmt.Sprintf("must be a multiple of 0.01, got %f", v.Price),
        })
    }

    if len(errs) > 0 {
        return &ValidationErrors{Errors: errs}
    }
    return nil
}
```

**Float precision**: `multipleOf` for floats uses an epsilon-based comparison in the runtime library:

```go
// isMultipleOf checks if value is a multiple of divisor,
// tolerating IEEE 754 floating-point precision errors.
//
// Algorithm: compute quotient = value / divisor, then check if quotient
// is close to an integer. This avoids the pitfall of relative-epsilon
// approaches where epsilon grows linearly with |value| (e.g., at
// value=1e12 a relative epsilon of max(|value|,|divisor|)*1e-9 = 1000,
// making any value pass).
func isMultipleOf(value, divisor float64) bool {
    if divisor == 0 {
        return false
    }
    // Quotient-rounding approach: compute value/divisor, check if the quotient
    // is close to an integer. This is more robust than remainder-based approaches
    // because the tolerance is on the quotient (dimensionless), not on the
    // remainder (which scales with divisor magnitude).
    //
    // Example: isMultipleOf(500000, 0.01) → quotient=5e7, round(5e7)=5e7 → pass.
    // The remainder-based approach would fail because math.Remainder(500000, 0.01)
    // returns ~1e-11 which exceeds the divisor-scaled epsilon of 1e-11.
    q := value / divisor
    rounded := math.Round(q)
    // Tolerance: scale with quotient magnitude to handle IEEE 754 precision.
    // At small quotients, use absolute 1e-9. At large quotients, use a
    // relative tolerance based on machine epsilon (2.22e-16) × safety factor.
    // The factor of 128 (≈ 7 bits) tolerates accumulated error from
    // representing value, divisor, and the division result.
    tolerance := math.Max(1e-9, math.Abs(rounded) * 128 * 2.220446049250313e-16)
    return math.Abs(q-rounded) <= tolerance
}
```

For integer types where `multipleOf` is also an integer, exact modulo (`%`) is used — no precision issues. When `multipleOf` is a non-integer (e.g., `multipleOf: 0.5` with `type: integer`), the float-based `isMultipleOf` function is used instead, since Go's `%` operator requires integer operands. For currency-like values (`multipleOf: 0.01`), the generated comment recommends using integer cents instead.

**Limitation**: For extremely large quotients (value/divisor > ~1e15), the quotient itself loses integer precision due to float64's 53-bit mantissa. At this scale, the tolerance also grows large enough to accept non-multiples (e.g., at value=1e15, tolerance ≈ 28, meaning values off by up to 28 units from a true multiple pass the check). This is inherent to IEEE 754 and affects all float-based multipleOf implementations. For values where exact arithmetic is required (e.g., financial calculations), users should use integer types with appropriate scaling (e.g., cents instead of dollars) and integer `multipleOf`, which uses exact modulo (`%`) with no precision issues. The generated `Validate()` includes a comment noting this limitation when float `multipleOf` is used.

### Array Constraints

```yaml
tags:
  type: array
  items: { type: string }
  minItems: 1
  maxItems: 10
  uniqueItems: true
```

```go
func (v Pet) Validate() error {
    var errs []*ValidationError

    // tags: minItems=1
    if len(v.Tags) < 1 {
        errs = append(errs, &ValidationError{
            Field:      "tags",
            Constraint: "minItems",
            Message:    fmt.Sprintf("must have >= 1 items, got %d", len(v.Tags)),
        })
    }
    // tags: maxItems=10
    if len(v.Tags) > 10 {
        errs = append(errs, &ValidationError{
            Field:      "tags",
            Constraint: "maxItems",
            Message:    fmt.Sprintf("must have <= 10 items, got %d", len(v.Tags)),
        })
    }
    // tags: uniqueItems=true
    if !isUnique(v.Tags) {
        errs = append(errs, &ValidationError{
            Field:      "tags",
            Constraint: "uniqueItems",
            Message:    "items must be unique",
        })
    }

    if len(errs) > 0 {
        return &ValidationErrors{Errors: errs}
    }
    return nil
}
```

The `isUnique` helper is provided in the runtime library. Two variants handle comparable and non-comparable item types:

```go
// isUnique checks uniqueness for comparable types (fast path, O(n) via map).
func isUnique[T comparable](s []T) bool {
    seen := make(map[T]struct{}, len(s))
    for _, v := range s {
        if _, ok := seen[v]; ok {
            return false
        }
        seen[v] = struct{}{}
    }
    return true
}

// isUniqueAny checks uniqueness for non-comparable types (e.g., structs
// containing slices or maps) using JSON-canonical comparison. O(n) expected
// via map-based dedup (after canonicalization).
//
// IMPORTANT: We compare via JSON canonicalization rather than reflect.DeepEqual
// because JSON Schema defines instance equality by mathematical value for numbers
// (1 and 1.0 are equal), while reflect.DeepEqual(int(1), float64(1.0)) returns false.
//
// Canonicalization: each element is marshaled to JSON, unmarshaled into `any`
// (which coerces all JSON numbers to float64), then re-marshaled to produce a
// canonical JSON string. The re-marshal step (json.Marshal) sorts map keys
// alphabetically, which normalizes key ordering.
// This ensures {"a":1,"b":2} and {"b":2,"a":1} are treated as equal, and
// handles json.RawMessage fields whose original byte order may differ.
//
// LIMITATION: The unmarshal-to-any step coerces all JSON numbers to float64,
// which loses precision for integers beyond 2^53 (e.g., 9007199254740992 and
// 9007199254740993 both become the same float64). This means isUniqueAny may
// incorrectly report two distinct large integers as duplicates, or fail to
// detect that two values differing only beyond float64 precision are actually
// identical. For arrays with large-integer items, users should ensure the item
// type maps to int64 (which uses the isUnique fast path with exact comparison).
// A future enhancement could use json.NewDecoder with UseNumber() to preserve
// integer precision during canonicalization.
func isUniqueAny(s any) bool {
    rv := reflect.ValueOf(s)
    // Canonicalize each element: marshal → unmarshal to any → re-marshal
    jsons := make([]string, rv.Len())
    for i := 0; i < rv.Len(); i++ {
        b, err := json.Marshal(rv.Index(i).Interface())
        if err != nil {
            // If marshaling fails, fall back to reflect.DeepEqual
            return isUniqueAnyFallback(rv)
        }
        // Normalize: unmarshal to any (coerces numbers to float64)
        var normalized any
        if err := json.Unmarshal(b, &normalized); err != nil {
            return isUniqueAnyFallback(rv)
        }
        // Normalize negative zero: JSON Schema defines -0 == 0 (mathematical
        // equality), but json.Marshal(float64(-0)) produces "-0" ≠ "0".
        normalized = normalizeNegZero(normalized)
        // Re-marshal the normalized value for canonical string comparison
        canon, err := json.Marshal(normalized)
        if err != nil {
            return isUniqueAnyFallback(rv)
        }
        jsons[i] = string(canon)
    }
    seen := make(map[string]struct{}, len(jsons))
    for _, j := range jsons {
        if _, ok := seen[j]; ok {
            return false
        }
        seen[j] = struct{}{}
    }
    return true
}
```

The generator selects `isUnique` when the item type is a **deeply value-comparable type** at codegen time, and `isUniqueAny` otherwise. **Important**: Go's `==` operator on structs compares pointer **addresses**, not pointed-to values. After JSON unmarshaling, semantically identical objects get distinct heap allocations, so `map[T]struct{}` would treat them as unique — a false negative for `uniqueItems`. Therefore, the `isUnique` fast path is restricted to types where `==` provides **value equality**:

  - Primitive types: `string`, `int`, `int32`, `int64`, `float32`, `float64`, `bool`
  - Named enum types (which are primitive aliases)
  - Structs whose **all fields** are themselves deeply value-comparable (no pointers, no interfaces, no slices, no maps)

  Types that include `*T` fields, interface fields (`any`, named interfaces), slices, maps, or `json.RawMessage` always use `isUniqueAny` regardless of Go's `comparable` constraint. This ensures JSON Schema's value-equality semantics are respected.

### Object Constraints

```yaml
metadata:
  type: object
  additionalProperties: { type: string }
  minProperties: 1
  maxProperties: 50
```

```go
// Validated in the containing struct's Validate()
if len(v.Metadata) < 1 {
    errs = append(errs, &ValidationError{
        Field: "metadata", Constraint: "minProperties",
    })
}
if len(v.Metadata) > 50 {
    errs = append(errs, &ValidationError{
        Field: "metadata", Constraint: "maxProperties",
    })
}
```

### Recursive Validation

When a struct contains fields that are themselves validatable types, `Validate()` recurses:

```go
func (v Order) Validate() error {
    var errs []*ValidationError

    // Validate own fields
    // ...

    // Recurse into nested types
    if err := v.Customer.Validate(); err != nil {
        errs = append(errs, prefixErrors("customer", err)...)
    }
    for i, item := range v.Items {
        if err := item.Validate(); err != nil {
            errs = append(errs, prefixErrors(fmt.Sprintf("items[%d]", i), err)...)
        }
    }

    if len(errs) > 0 {
        return &ValidationErrors{Errors: errs}
    }
    return nil
}
```

`prefixErrors` prepends the field path to nested validation errors, producing paths like `"customer.address.zip_code"`.

### Pointer / Nullable Fields

Validation is skipped for nil pointers and absent `Nullable[T]` values — you cannot validate a value that isn't there. Null `Nullable[T]` values are also skipped (the field is explicitly nullable per the schema, so null is a valid value):

**Limitation**: For optional non-nullable fields (Go type `*T`), JSON `null` sets the pointer to nil — the same state as absent. `Validate()` cannot distinguish these cases and skips nil pointers unconditionally. This means null values on non-nullable `*T` fields are **not rejected** by `Validate()`. This is an inherent limitation of `encoding/json` v1's conflation of absent and null for pointer types (see ADR-004). Enforcement of the non-null constraint requires `rawFieldKeys` tracking (to detect the field's presence in the JSON) combined with a nil-pointer check, which is generated when `additionalProperties: false`, `unevaluatedProperties: false`, or `dependentRequired` triggers `rawFieldKeys` generation.

```go
// Optional field: skip if absent
if v.Age != nil {
    if *v.Age < 0 {
        // validate
    }
}

// Nullable field: skip if absent or null
if v.Status.IsValue() {
    val, _ := v.Status.Get()
    if err := val.Validate(); err != nil {
        // validate
    }
}
```

### Unified with Existing Validate() Methods

Enum `Validate()` (ADR-008), `dependentRequired` validation (ADR-010), and `unevaluatedProperties` validation (ADR-012) are all merged into the same `Validate()` method:

```go
func (v ComplexType) Validate() error {
    var errs []*ValidationError

    // Constraint validation
    if len(v.Name) < 1 { /* ... */ }

    // Enum validation
    if err := v.Status.Validate(); err != nil { /* ... */ }

    // DependentRequired validation (uses rawFieldKeys for JSON-level presence
    // detection — Go's nil check on *T cannot distinguish absent from JSON null,
    // but JSON Schema's dependentRequired triggers on property presence regardless
    // of value). rawFieldKeys is generated when any of: unevaluatedProperties: false,
    // additionalProperties: false, or dependentRequired is present.
    if slices.Contains(v.rawFieldKeys, "creditCard") && !slices.Contains(v.rawFieldKeys, "billingAddress") { /* ... */ }

    // UnevaluatedProperties validation
    for _, key := range v.rawFieldKeys { /* ... */ }

    // Recursive validation
    if err := v.Address.Validate(); err != nil { /* ... */ }

    if len(errs) > 0 {
        return &ValidationErrors{Errors: errs}
    }
    return nil
}
```

### No Validate() When No Constraints

Types with no validation constraints do **not** get a `Validate()` method. This keeps generated code minimal and avoids empty methods.

### CLI Flags

| Flag | Effect |
|------|--------|
| `--skip-validation` | Do not generate `Validate()` methods at all |
| `--validate-on-unmarshal` | Generate `UnmarshalJSON` that calls `Validate()` after parsing (**explicit opt-in** — overrides the default no-auto-validation contract from ADR-001) |
| `--validate-on-unmarshal-skip=enum` | When combined with `--validate-on-unmarshal`, excludes specific validation categories from auto-validation. This allows strict validation on most constraints while preserving forward compatibility for enums (ADR-008's open enum default). Supported categories: `enum`, `const`, `pattern`, `all`. |

The default generates `Validate()` but does not call it automatically. The `--validate-on-unmarshal` flag is an **explicit user override** of the ADR-001 principle (which states validation is not called automatically *by default*): when a user passes this flag, they are deliberately choosing stricter behavior at the cost of the thin-wrapper philosophy. This flag is never the default and is clearly documented as a deviation.

**Interaction with ADR-008 (open enums)**: By default, `--validate-on-unmarshal` calls the full `Validate()` method, which includes enum validation. This means unknown enum values (from servers adding new values) would be rejected on unmarshal, breaking forward compatibility. Users who want auto-validation but need forward-compatible enums should use `--validate-on-unmarshal --validate-on-unmarshal-skip=enum`. This is documented in the generated code's header comment when the flag is active.

## Consequences

### Positive

- **Consistent pattern**: one `Validate()` method per type, combining all constraint types
- **Opt-in by default**: consistent with ADR-001 thin wrapper philosophy
- **Composable**: `Validatable` interface allows generic validation utilities
- **Recursive**: nested types are validated with proper field path reporting
- **Zero overhead when not used**: no validation code runs unless explicitly called
- **All errors collected**: validation does not short-circuit on first error, returning all violations at once

### Negative

- **Generated code volume**: `Validate()` methods can be lengthy for types with many constrained fields. The `--skip-validation` flag mitigates this.
- **String length uses RuneCount**: slightly slower than `len()` but correct per spec. APIs dealing exclusively with ASCII can override via `--string-length=bytes`.
- **Float multipleOf precision**: IEEE 754 makes exact float comparison unreliable. We use a quotient-rounding approach (check if `value/divisor` is close to an integer) which is better than remainder-based approaches but not perfect for extreme values. Integer types do not have this issue.

### Risks

- `pattern` regex in OpenAPI uses ECMA-262 (JavaScript) regex syntax, while Go uses RE2. Some patterns (lookahead, backreferences) are not supported in Go. The generator attempts to compile each pattern with `regexp.Compile()`; if compilation fails:
  1. A **generation-time warning** is emitted: `WARN: Pattern "(?=.*[A-Z])" uses ECMA-262 features not supported by Go's RE2 engine. This constraint will not be validated at runtime.`
  2. The specific `pattern` constraint is **omitted** from the generated `Validate()` method (no code is generated for it).
  3. A comment is added to the generated code noting the skipped constraint.

  **Impact**: The generated code does NOT silently skip the constraint at runtime — the constraint is entirely absent from the generated `Validate()`. This is a **known coverage gap**: the schema's `pattern` constraint goes unenforced. Users who need these patterns should use a ECMA-262-compatible regex library via custom validation middleware.

  ```go
  // WARNING: Pattern "(?=.*[A-Z])" uses ECMA-262 features not supported
  // by Go's regexp package. This constraint is NOT validated.
  // Use custom validation if strict pattern checking is required.
  ```

- Deeply nested recursive types could theoretically cause stack overflow in `Validate()`. We do not add cycle detection — schemas with genuine circular validation are rare and typically indicate a spec error.
