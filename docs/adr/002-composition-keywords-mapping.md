# ADR-002: Composition Keywords Mapping (allOf / oneOf / anyOf / not)

## Status

Accepted

## Date

2026-03-01

## Context

OpenAPI defines four composition keywords that combine schemas:

| Keyword | Semantics | Existing Go tool support |
|---------|-----------|------------------------|
| `allOf` | Value must validate against **all** listed schemas | Decent (struct merge) |
| `oneOf` | Value must validate against **exactly one** schema | Poor (json.RawMessage hacks) |
| `anyOf` | Value must validate against **one or more** schemas | Essentially unsupported |
| `not` | Value must **not** validate against the schema | Unsupported |

Go lacks sum types, union types, and intersection types. Every existing generator compromises:

- **oapi-codegen**: Uses `json.RawMessage` with `AsCat()`/`AsDog()` accessor methods for oneOf/anyOf. No compile-time type safety. anyOf treated identically to oneOf (violating spec semantics).
- **ogen**: Generates custom sum type structs for oneOf. Produces voluminous code. anyOf handling is similar to oneOf.
- **openapi-generator**: Generates oneOf as a wrapper struct with `json.RawMessage` internally and `AsType()` accessors (similar to oapi-codegen). Limited type safety. anyOf support exists but is treated identically to oneOf.

The key insight is that `oneOf` and `anyOf` differ **only in validation strictness**, not in structural representation. Both can be represented as a struct where each variant is a pointer field. The difference is whether multiple non-nil fields are permitted.

## Decision

### allOf → Struct Embedding / Field Merge

All schemas in an `allOf` must validate simultaneously. In Go, this maps naturally to struct composition.

**Strategy**: Merge all properties from all schemas into a single struct. When the same property appears in multiple schemas, the generator analyzes compatibility:

- **Conflicting types where the property is in the effective required set** (required in **any** allOf branch): generation-time error. Since allOf unions all `required` arrays, a property required in even one branch is required in the merged schema. A required property must satisfy all branch types simultaneously, which is impossible for conflicting types (e.g., string ∩ integer = ∅). Example: schema A has `required: [name]` with `name: {type: string}` and schema B has `name: {type: integer}` (not required in B) — the effective required is `[name]` (from A), and the property must be both string and integer, which is impossible.
- **Conflicting types where the property is NOT required in any schema**: not necessarily impossible — an instance omitting the property (`{}`) can satisfy all branches. The generator emits a warning but proceeds. The property's type uses the type from the first schema containing it. The property is optional (pointer) in the generated struct. A `Validate()` method enforces the allOf constraint at runtime when the property is present.
- **Compatible types** (e.g., both say `type: string` but with different constraints): the constraints are merged (intersection of all constraint sets).

```yaml
# OpenAPI
allOf:
  - $ref: '#/components/schemas/Pet'
  - type: object
    required: [breed]
    properties:
      breed:
        type: string
```

```go
// Generated Go
type PetWithBreed struct {
    // Fields from Pet
    Name string `json:"name"`
    Tag  string `json:"tag,omitzero"`
    // Additional fields
    Breed string `json:"breed"`
}
```

**When embedding is safe** (no field name conflicts and no JSON tag conflicts), we use Go struct embedding:

```go
type PetWithBreed struct {
    Pet
    Breed string `json:"breed"`
}
```

**When conflicts exist**, we flatten all fields into a single struct to avoid Go's ambiguous selector errors.

**`required` propagation**: The union of all `required` arrays across the allOf schemas determines which fields are non-pointer (required) in the generated struct. **Note**: In JSON Schema 2020-12, `required` is an object assertion — it only applies when the instance is an object. Non-object instances trivially pass `required`. The generator assumes that schemas processed via allOf property merging are object schemas (they have `properties`), which is the standard pattern in OpenAPI. Schemas without `type: object` but with `properties` are treated as objects for code generation purposes.

**Scope limitation**: The allOf merge strategy handles `properties`, `required`, and simple constraints (format, min/max, etc.). When allOf branches include `additionalProperties`, `patternProperties`, or other structural keywords that interact across branches, the combined semantics can be complex. The generator handles specific cases:

- **One branch has `additionalProperties: false`**: The known-field set is the union of all branches' declared properties, applied only to that branch's validation (see ADR-006). This is the standard OpenAPI composition pattern (base schema + extension).
- **Multiple branches have `additionalProperties: false` with non-overlapping properties**: This is pathological — per strict JSON Schema, `allOf: [{properties:{a}, additionalProperties:false}, {properties:{b}, additionalProperties:false}]` only admits `{}`. The generator emits a **generation-time error** because no Go struct can faithfully represent this.
- **Other conflicting structural keywords** (e.g., conflicting `patternProperties` across branches): the generator emits a **generation-time error** rather than silently producing incorrect code.

### oneOf → Wrapper Struct with Exactly-One Validation

A `oneOf` value validates against exactly one schema. We generate a wrapper struct with one pointer field per variant and a `UnmarshalJSON` that enforces the exactly-one constraint.

```yaml
# OpenAPI
oneOf:
  - $ref: '#/components/schemas/Cat'
  - $ref: '#/components/schemas/Dog'
```

```go
// Generated Go
type CatOrDogOneOf struct {
    Cat *Cat
    Dog *Dog
}

func (v *CatOrDogOneOf) UnmarshalJSON(data []byte) error {
    // Try each variant using the appropriate strategy
    // (see ADR-003 for strategy selection)
    // ...

    matched := v.countMatched()
    if matched == 0 {
        return &OneOfNoMatchError{
            Data:       data,
            Candidates: []string{"Cat", "Dog"},
        }
    }
    if matched > 1 {
        return &OneOfMultipleMatchError{
            Data:    data,
            Matched: v.matchedNames(),
        }
    }
    return nil
}

func (v CatOrDogOneOf) MarshalJSON() ([]byte, error) {
    // oneOf invariant: exactly one variant must be non-nil.
    // Check for multiple non-nil variants BEFORE marshaling to prevent
    // silently picking the first one (which would violate oneOf semantics).
    count := 0
    if v.Cat != nil { count++ }
    if v.Dog != nil { count++ }
    if count > 1 {
        return nil, fmt.Errorf("CatOrDogOneOf: multiple variants set (%d); exactly one of Cat or Dog must be non-nil", count)
    }
    if v.Cat != nil {
        return json.Marshal(v.Cat)
    }
    if v.Dog != nil {
        return json.Marshal(v.Dog)
    }
    // No variant set — this is an invalid state for oneOf.
    // Return an error rather than silently producing "null",
    // which would be indistinguishable from a legitimate null value.
    return nil, fmt.Errorf("CatOrDogOneOf: no variant set; exactly one of Cat or Dog must be non-nil")
}

// Convenience
func (v CatOrDogOneOf) IsCat() bool { return v.Cat != nil }
func (v CatOrDogOneOf) IsDog() bool { return v.Dog != nil }

// Null variant handling: When a composition includes `type: "null"` as a
// variant (e.g., `oneOf: [Cat, {type: "null"}]`), this is the nullable
// pattern. The generator does NOT add a separate `Null *struct{}` variant.
// Instead, the entire composition type is wrapped in Nullable[T] (per ADR-004):
//   type NullableCat = Nullable[Cat]
// This avoids the ambiguity of nil meaning both "no variant" and "null variant".

// Constructors
// Go 1.26+: uses new(expr) for ergonomic pointer construction
//   func NewCatOrDogOneOfCat(c Cat) CatOrDogOneOf {
//       return CatOrDogOneOf{Cat: new(c)}
//   }
// Go 1.24-1.25: uses ptr[T] helper (see ADR-005)
func NewCatOrDogOneOfCat(c Cat) CatOrDogOneOf {
    return CatOrDogOneOf{Cat: ptr(c)}
}

func NewCatOrDogOneOfDog(d Dog) CatOrDogOneOf {
    return CatOrDogOneOf{Dog: ptr(d)}
}
```

### anyOf → Wrapper Struct with At-Least-One Validation

An `anyOf` value validates against one or more schemas **simultaneously**. This is the critical distinction from oneOf that existing Go tools ignore.

The struct shape is identical to oneOf. The primary difference is in unmarshal validation (anyOf allows multiple matches; oneOf does not), but marshal behavior also differs when multiple variants are set (see behavioral differences table below):

```yaml
# OpenAPI
anyOf:
  - $ref: '#/components/schemas/Cat'
  - $ref: '#/components/schemas/Dog'
```

```go
// Generated Go
type CatOrDogAnyOf struct {
    Cat *Cat // non-nil if value matches Cat schema
    Dog *Dog // non-nil if value matches Dog schema
}

func (v *CatOrDogAnyOf) UnmarshalJSON(data []byte) error {
    // Try ALL variants (not short-circuit)
    // ...

    if v.Cat == nil && v.Dog == nil {
        return &AnyOfNoMatchError{
            Data:       data,
            Candidates: []string{"Cat", "Dog"},
        }
    }
    // Multiple non-nil is VALID for anyOf
    return nil
}

func (v CatOrDogAnyOf) MarshalJSON() ([]byte, error) {
    // Multiple matched: merge fields from all non-nil variants
    if v.Cat != nil && v.Dog != nil {
        return marshalMerge(v.Cat, v.Dog)
    }
    if v.Cat != nil {
        return json.Marshal(v.Cat)
    }
    if v.Dog != nil {
        return json.Marshal(v.Dog)
    }
    // No variant set — same as oneOf, this is an invalid state.
    // Return an error rather than silently producing "null".
    return nil, fmt.Errorf("CatOrDogAnyOf: no variant set; at least one of Cat or Dog must be non-nil")
}

// anyOf-specific: check how many variants matched
func (v CatOrDogAnyOf) MatchCount() int {
    count := 0
    if v.Cat != nil { count++ }
    if v.Dog != nil { count++ }
    return count
}
func (v CatOrDogAnyOf) IsCat() bool     { return v.Cat != nil }
func (v CatOrDogAnyOf) IsDog() bool     { return v.Dog != nil }
```

**Key behavioral differences between oneOf and anyOf:**

| Behavior | oneOf | anyOf |
|----------|-------|-------|
| 0 matches on unmarshal | Error | Error |
| 1 match on unmarshal | OK | OK |
| 2+ matches on unmarshal | **Error** | **OK** |
| Marshal with 2+ non-nil | N/A (invariant violation) | **Merge fields** (objects) / **Error or first value** (non-objects; see marshalMerge below) |

### not → Validation Function Only

The `not` keyword cannot be represented in Go's type system ("all types except X" is not expressible). We generate a validation function but do not alter the type:

```yaml
# OpenAPI
not:
  type: string
```

```go
// Generated Go: validation helper only
func validateNotString(data []byte) error {
    // JSON null is NOT a string — skip validation for null values.
    if string(data) == "null" {
        return nil
    }
    var s string
    if err := json.Unmarshal(data, &s); err == nil {
        return &NotViolationError{ForbiddenType: "string"}
    }
    return nil // ok — it's not a string
}
```

`not` is uncommon in practice and is primarily a validation concern. We generate validation helpers but do not attempt to encode it in the Go type system.

### Naming Convention

Generated wrapper types are named by joining variant names with the composition keyword:

| Schema | Generated type name |
|--------|-------------------|
| `oneOf: [Cat, Dog]` | `CatOrDogOneOf` |
| `anyOf: [Cat, Dog]` | `CatOrDogAnyOf` |
| `oneOf: [Cat, Dog, Bird]` | `CatOrDogOrBirdOneOf` |

If the composition appears inside a named schema (e.g., `components/schemas/Pet`), we use the schema name:

| Schema | Generated type name |
|--------|-------------------|
| `Pet: { oneOf: [Cat, Dog] }` | `Pet` (the oneOf IS the type) |

### marshalMerge Semantics

For anyOf marshaling when multiple variants are set, `marshalMerge` performs a **JSON object merge**:

1. Marshal each non-nil variant to JSON
2. If **all** marshaled values are JSON objects (`{...}`): merge into a single `map[string]json.RawMessage`. For conflicting keys (same key, different values per JSON semantic equality), `marshalMerge` returns an error (`ErrAnyOfConflictingKeys`). When values are identical (same key, same value), the key is included once. This ensures `MarshalJSON` never silently produces a payload that conflicts with the input variants. Note: when constructed via `UnmarshalJSON` (from the same JSON input), all variants' shared keys always have identical values, so conflicts only occur when users manually set different values on different variants.
3. If **any** marshaled value is NOT a JSON object (e.g., a string, number, array, or null): merging is not possible. All non-nil variants' JSON must be **semantically identical** (JSON-value equality, not byte equality); if they differ, `marshalMerge` returns an error (`ErrAnyOfConflictingValues`). If they are semantically identical, `marshalMerge` returns the first variant's marshaled JSON (since all are equivalent). Semantic comparison normalizes values by unmarshaling each variant's JSON via `json.NewDecoder` with `UseNumber()` into `any`, then comparing with a custom `jsonEqual` function that performs **mathematical numeric comparison** (parsing `json.Number` values via `new(big.Rat).SetString()` and comparing with `Rat.Cmp`). This handles differences in numeric representation (`1` vs `1.0` vs `1e0`), key ordering, and whitespace that byte-level comparison would incorrectly flag as mismatches. Using `UseNumber` preserves integer precision beyond float64's 53-bit mantissa (e.g., `9007199254740993` vs `9007199254740992` are correctly distinguished), and `big.Rat` comparison provides exact arithmetic for all JSON-representable numbers. Note that custom `MarshalJSON` implementations may produce different byte output even from the same input data, which is another reason byte comparison is insufficient. When constructed via `UnmarshalJSON` (from the same input), semantic identity is always satisfied because all variants were unmarshaled from the same JSON value. Divergence can occur when users manually set different values on multiple variants. A generation-time warning is emitted when anyOf mixes object and non-object schemas.
4. Marshal the merged map

**Limitation — marshalMerge may produce schema-invalid JSON**: Object merge can produce JSON that matches no individual variant's schema. For example, if Cat has `{"name": "Fluffy", "whiskers": true}` and Dog has `{"name": "Fluffy", "breed": "lab"}` (same `name` — no conflict), merging produces `{"name": "Fluffy", "whiskers": true, "breed": "lab"}` which may match neither Cat nor Dog's validation rules due to extra fields. If conflicting keys are detected (different values for the same key), `MarshalJSON` returns an error.

**Why MarshalJSON does not validate the merged output**: Validating against variant schemas inside `MarshalJSON` would require embedding schema validation logic (property sets, constraints, additionalProperties rules) into every anyOf type's marshal method — essentially duplicating `Validate()` inside `MarshalJSON`. This violates the project's principle of opt-in validation (ADR-013) and adds significant code and runtime cost to every marshal call. Instead, the responsibility is on the caller: **always call `Validate()` before marshaling** when multiple anyOf variants are set. The `Validate()` method checks that the combined state satisfies at least one variant schema. This is the same pattern used throughout the project: `MarshalJSON`/`UnmarshalJSON` handle serialization; `Validate()` handles schema compliance.

**Recommendation**: For anyOf types where multiple variants are set, users should call `Validate()` before marshaling to verify the combined state satisfies at least one schema. We emit a generation-time warning when anyOf variants have overlapping properties with different semantics.

**Design decision**: We explicitly chose NOT to embed schema validation inside `MarshalJSON`, accepting that `MarshalJSON` success does not guarantee schema validity for multi-variant anyOf. The alternative (validation in marshal) was rejected because: (1) it violates the opt-in validation principle (ADR-001, ADR-013); (2) it duplicates `Validate()` logic in every anyOf marshal method; (3) Go's standard library types (`time.Time`, `net.IP`, etc.) similarly allow `MarshalJSON` to succeed with logically invalid values. This trade-off is inherent to representing anyOf multi-match in Go and applies to all existing Go OpenAPI generators that support anyOf.

## Consequences

### Positive

- **anyOf is faithfully represented**: multiple simultaneous matches are preserved, unlike every existing Go tool
- **oneOf and anyOf share the same struct shape**: consistent API, easy to understand
- **Type-safe at compile time**: users access `.Cat` and `.Dog` directly, no type assertions on `interface{}`
- **Validation matches spec semantics**: oneOf rejects multi-match, anyOf allows it

### Negative

- **Variant detection without discriminator is heuristic**: relying on required fields or unique properties can produce false positives for loosely-defined schemas (mitigated by strategy selection in ADR-003)
- **marshalMerge adds complexity**: merging multiple variants' JSON requires careful handling of conflicting keys
- **not is second-class**: validation only, no type-level enforcement

### Risks

- Schemas without `required` fields and without a discriminator make variant detection unreliable. In the fallback case (try all), Go's permissive `json.Unmarshal` may succeed for all variants. We document this limitation and recommend discriminator usage.
