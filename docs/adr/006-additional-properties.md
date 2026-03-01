# ADR-006: additionalProperties Mapping

## Status

Accepted

## Date

2026-03-01

## Context

The `additionalProperties` keyword in OpenAPI/JSON Schema controls whether an object may contain properties beyond those listed in `properties` (and `patternProperties`, if present — see ADR-011). Per JSON Schema 2020-12, `additionalProperties` applies only to keys that do NOT match any entry in `properties` or `patternProperties`. It has three forms:

| Schema | Meaning |
|--------|---------|
| `additionalProperties: true` (or absent) | Any extra key-value pairs allowed, values can be any type |
| `additionalProperties: false` | No extra properties allowed (strict) |
| `additionalProperties: {type: string}` | Extra properties allowed, values must match the given schema |
| `additionalProperties: {$ref: ...}` | Extra properties allowed, values must match the referenced schema |

The challenge arises when an object has **both** defined `properties` and `additionalProperties`. Go structs cannot simultaneously have named fields and act as a map. Every existing Go generator handles this differently and imperfectly.

### How existing tools handle this

- **oapi-codegen**: Generates `AdditionalProperties map[string]T` alongside struct fields with custom marshal/unmarshal. Works but verbose.
- **ogen**: Generates a separate `Additional` map field.
- **openapi-generator**: Ignores `additionalProperties` in many cases.
- **openapi-typescript**: Uses TypeScript index signatures (`[key: string]: T`) which have no Go equivalent.

### Real-world usage patterns

1. **Pure map** (`type: object` + `additionalProperties: {type: string}`, no `properties`): very common for key-value stores, labels, metadata
2. **Strict object** (`properties` + `additionalProperties: false`): common for well-defined API models
3. **Mixed object** (`properties` + `additionalProperties: {schema}`): less common but used for extensible configurations
4. **Catch-all** (`properties` + `additionalProperties: true`): common when APIs may add fields without breaking changes

## Decision

### Case 1: Pure Map (no `properties`, only `additionalProperties`)

When a schema has no `properties` (or empty `properties`) and only `additionalProperties`, it maps directly to a Go map:

```yaml
type: object
additionalProperties:
  type: string
```

```go
// No constraints → type alias (no methods needed)
type Labels = map[string]string
```

When the schema has constraints (`minProperties`, `maxProperties`, value-level constraints like `pattern`, etc.), the generator uses a **defined type** instead of a type alias, enabling `Validate()` method attachment:

```yaml
type: object
additionalProperties:
  type: string
minProperties: 1
maxProperties: 10
```

```go
// Has constraints → defined type (can attach Validate())
type Labels map[string]string

func (l Labels) Validate() error {
    if len(l) < 1 { return &MinPropertiesError{Min: 1, Actual: len(l)} }
    if len(l) > 10 { return &MaxPropertiesError{Max: 10, Actual: len(l)} }
    return nil
}
```

```yaml
type: object
additionalProperties:
  $ref: '#/components/schemas/Widget'
```

```go
type WidgetMap = map[string]Widget
```

```yaml
type: object
additionalProperties: true
```

```go
type FreeFormObject = map[string]any
```

**Type alias vs defined type rule**: The generator uses a type alias (`type X = map[...]`) when the schema has no constraints that require validation. When any constraint exists (`minProperties`, `maxProperties`, value-schema constraints, etc.), it generates a defined type (`type X map[...]`) so that `Validate()` can be attached. This ensures `Validate()` is always available when the schema has constraints.

### Case 2: Strict Object (`additionalProperties: false`)

When `additionalProperties` is explicitly `false`, we generate a struct with a custom `UnmarshalJSON` that tracks raw field keys (for unknown-field detection). **Important**: `encoding/json` v1 **silently discards** unknown JSON fields during unmarshal — it does not reject them. This means the `additionalProperties: false` constraint is **not enforced by the Go type alone**. Enforcement is provided by `Validate()` (which uses `rawFieldKeys` from `UnmarshalJSON` to detect unknown fields, following the same pattern as ADR-012's `unevaluatedProperties: false`), or by `encoding/json/v2`'s strict mode. The `Validate()` method is generated for schemas with `additionalProperties: false` (unless `--skip-validation` is specified, in which case neither `Validate()` nor `rawFieldKeys` tracking is generated).

```yaml
type: object
required: [name, age]
properties:
  name: { type: string }
  age: { type: integer }
additionalProperties: false
```

```go
type Person struct {
    Name         string   `json:"name"`
    Age          int      `json:"age"`
    rawFieldKeys []string // populated by UnmarshalJSON for unknown-field detection
}
```

A custom `UnmarshalJSON` tracks raw field keys, and `Validate()` uses them to reject unknown fields (see ADR-012 for the pattern). When `--skip-validation` is specified, neither `rawFieldKeys` tracking nor `Validate()` is generated, and the struct is a plain struct with no additional fields.

**Known limitation (case-variant keys in strict objects)**: `encoding/json` v1 matches JSON keys to struct fields using case-folding (`bytes.EqualFold`), not case-sensitive comparison. This means `{"NAME": "x"}` is matched to a struct field tagged `json:"name"` — the key is treated as a known field, not an additional property. For `additionalProperties: false`, `Validate()` uses the same case-fold matching to classify keys. As a result, case-variant keys (e.g., `NAME` vs `name`) are **not** rejected as unknown. This deviates from JSON Schema's case-sensitive property matching. We accept this tradeoff because: (a) rejecting case-variants that `encoding/json` successfully decoded would create an inconsistency between the decoded struct state and the validation result, (b) case-variant property names are extremely rare in real-world APIs, (c) `encoding/json/v2` resolves this by using case-sensitive matching by default. The generated documentation warns about this limitation.

### Case 3: Object with Typed additionalProperties

When both `properties` and `additionalProperties: {schema}` are present, we generate a struct with an extra `AdditionalProperties` map field and custom `MarshalJSON` / `UnmarshalJSON` to merge known fields with the map.

```yaml
type: object
required: [name]
properties:
  name: { type: string }
  age: { type: integer }
additionalProperties:
  type: string
```

```go
type Person struct {
    Name                 string            `json:"name"`
    Age                  *int              `json:"age,omitzero"`
    AdditionalProperties map[string]string `json:"-"`
}

func (p *Person) UnmarshalJSON(data []byte) error {
    // Step 1: Unmarshal known fields via a type alias (avoids infinite recursion)
    type plain Person
    if err := json.Unmarshal(data, (*plain)(p)); err != nil {
        return err
    }

    // Step 2: Unmarshal everything into a raw map
    var raw map[string]json.RawMessage
    if err := json.Unmarshal(data, &raw); err != nil {
        return err
    }

    // Step 3: Remove known fields, unmarshal the rest as additional properties.
    // Use case-insensitive comparison to match encoding/json v1 behavior.
    for key, val := range raw {
        if isKnownFieldPerson(key) {
            continue
        }
        if p.AdditionalProperties == nil {
            p.AdditionalProperties = make(map[string]string)
        }
        var s string
        if err := json.Unmarshal(val, &s); err != nil {
            return fmt.Errorf("additional property %q: %w", key, err)
        }
        p.AdditionalProperties[key] = s
    }
    return nil
}

func (p Person) MarshalJSON() ([]byte, error) {
    // Step 1: Marshal known fields
    type plain Person
    base, err := json.Marshal(plain(p))
    if err != nil {
        return nil, err
    }

    // Step 2: If no additional properties, return as-is
    if len(p.AdditionalProperties) == 0 {
        return base, nil
    }

    // Step 3: Merge additional properties into the JSON object
    // IMPORTANT: Skip keys that collide with known fields to prevent
    // additionalProperties from overwriting structured field values.
    var merged map[string]json.RawMessage
    if err := json.Unmarshal(base, &merged); err != nil {
        return nil, err
    }
    for key, val := range p.AdditionalProperties {
        if isKnownFieldPerson(key) {
            continue // do not overwrite known fields (case-insensitive)
        }
        encoded, err := json.Marshal(val)
        if err != nil {
            return nil, fmt.Errorf("additional property %q: %w", key, err)
        }
        merged[key] = encoded
    }
    return json.Marshal(merged)
}
```

**Key design decisions:**

- The `AdditionalProperties` field uses `json:"-"` to prevent standard marshal/unmarshal from touching it. All handling goes through the custom methods.
- We use a type alias trick (`type plain Person`) to call the default marshal/unmarshal for known fields without infinite recursion.
- Known field names are generated as a static set at codegen time, not computed at runtime. **Important**: Go's `encoding/json` v1 matches field names using **case-folding** (`bytes.EqualFold`), not simple lowercase comparison. The `knownFields` check must use the same folding algorithm to avoid duplication (JSON key matched by `encoding/json` to a struct field AND captured as an additional property):
  ```go
  // knownFieldsPerson stores the canonical JSON tag names.
  var knownFieldsPerson = []string{"name", "age"}

  // isKnownFieldPerson checks using the same case-fold logic as encoding/json v1.
  // encoding/json uses bytes.EqualFold (Unicode case folding), not strings.ToLower.
  // These differ: e.g., "ſ" (U+017F, long s) folds to "s" under EqualFold but
  // not under ToLower. Using ToLower would cause mismatches.
  func isKnownFieldPerson(key string) bool {
      for _, k := range knownFieldsPerson {
          if strings.EqualFold(k, key) {
              return true
          }
      }
      return false
  }
  ```
  The linear scan is acceptable because the number of known fields per struct is small (typically < 20). For structs with many fields, the generator can use a pre-computed fold map if needed.

  **Known limitation (case-variant key loss)**: OpenAPI/JSON Schema defines property names as **case-sensitive**, but `encoding/json` v1 uses case-insensitive matching. When JSON input contains case-variant keys (e.g., `{"name":"base","NAME":"override"}`), `encoding/json` matches both to the same struct field (last value wins), and the `isKnownField` guard prevents either from being captured as additional properties. Result: the case-variant key is silently lost during round-trip. This is an inherent limitation of `encoding/json` v1 and cannot be fixed without a custom JSON decoder. We accept this tradeoff because (a) case-variant property names are extremely rare in practice, (b) the alternative (case-sensitive `isKnownField`) would cause the same key to appear in **both** the struct field and `AdditionalProperties`, which is worse. This limitation is resolved when `encoding/json/v2` is adopted (v2 uses case-sensitive matching by default).

- **MarshalJSON skips known field keys** from AdditionalProperties to prevent overwriting. This is critical: without this guard, a user setting `AdditionalProperties["name"] = "evil"` would overwrite the structured `Name` field in the output JSON. The skip is silent (no error returned) because this is a **defensive guard**, not an error condition — the struct field's value is authoritative, and the duplicate key in the map is simply redundant. If callers need to detect this condition, `Validate()` reports when `AdditionalProperties` contains keys that collide with known fields. **Important**: The generator produces a `Validate()` method for structs with `AdditionalProperties` (Cases 3, 4, and 5 with `--additional-properties=preserve` or `strict`) unless `--skip-validation` is specified. The key-collision check is one of the constraints validated, alongside any schema constraints on the additional property values. When `--skip-validation` is used, `MarshalJSON` still silently skips collisions, but no `Validate()` is available to detect them — this is an accepted tradeoff of the skip-validation mode.

### Case 4: Object with `additionalProperties: true` (Untyped)

Same pattern as Case 3, but the map value type is `any`:

```yaml
type: object
required: [name]
properties:
  name: { type: string }
additionalProperties: true
```

```go
type Person struct {
    Name                 string         `json:"name"`
    AdditionalProperties map[string]any `json:"-"`
}
// Same custom MarshalJSON / UnmarshalJSON as Case 3, with map[string]any
```

### Case 5: `additionalProperties` Absent (Default)

Per the JSON Schema specification, when `additionalProperties` is not specified, additional properties are **allowed** (equivalent to `additionalProperties: true`).

**Important**: When a schema is `type: object` with **no `properties`** (or empty `properties`) and **no `additionalProperties`**, it is a **free-form object** — equivalent to `additionalProperties: true` with no named fields. This always generates `map[string]any` (same as Case 1 with `additionalProperties: true`), regardless of the `--additional-properties` flag. The flag only affects schemas that have **both** `properties` and absent `additionalProperties` — i.e., Case 5 below.

For schemas with `properties` and absent `additionalProperties`, in practice most Go API consumers do not need to access unknown fields. We take a **pragmatic default**:

- When `additionalProperties` is **absent** and `properties` is present: generate a plain struct with no `rawFieldKeys` tracking or custom `UnmarshalJSON`. Unlike `additionalProperties: false` (which generates `rawFieldKeys` + `Validate()` for unknown-field detection), the absent case produces a minimal struct. The standard `encoding/json` decoder silently ignores unknown fields anyway.
- When `additionalProperties` is **explicitly `true`**: generate the struct with `AdditionalProperties map[string]any` (Case 4).

This avoids generating custom marshal/unmarshal for the majority of schemas where `additionalProperties` is simply unspecified and not intentionally used.

**Known deviation from JSON Schema**: Per JSON Schema, absent `additionalProperties` means `true` (additional properties allowed). Our default **ignores** additional properties when absent (generating a plain struct with no `rawFieldKeys` or custom `UnmarshalJSON`), which is similar to but **not identical to** `false`: `additionalProperties: false` generates `rawFieldKeys` + `Validate()` that actively rejects unknown fields, while absent generates a minimal struct where unknown fields are silently dropped by `encoding/json`. The practical effect of our default is:

1. **Forward compatibility risk**: If the server adds new fields in a response, they are silently dropped during unmarshal. This is acceptable for clients (Go's `encoding/json` drops unknown fields by default anyway), but users who round-trip data (unmarshal → modify → marshal) will lose those fields.
2. **Validation mismatch**: If `Validate()` checks for unevaluated properties, the absent vs. `false` distinction matters.

CLI flags for controlling this behavior:

| Flag | Effect |
|------|--------|
| `--additional-properties=ignore` | (Default) Absent → plain struct, no extra fields captured |
| `--additional-properties=preserve` | Absent → `AdditionalProperties map[string]json.RawMessage` to preserve unknown fields during round-trips |
| `--additional-properties=strict` | Absent → `true`, full map generation for all types without explicit `additionalProperties: false` |

**Scope of `--additional-properties` flag**: This flag applies **only** to Case 5 — schemas with `properties` and absent `additionalProperties`. It does NOT affect: (a) schemas with no `properties` (free-form objects → always `map[string]any`), (b) schemas with explicit `additionalProperties: false/true/{schema}` (Cases 1-4, 6), (c) `patternProperties`-only schemas (ADR-011 — these use their own type generation rules independent of this flag). Specifically, ADR-011's `map[string]json.RawMessage` for multi-pattern schemas is determined by the `patternProperties` structure, not by this flag.

### Case 6: `additionalProperties` as a Complex Schema

When `additionalProperties` references a complex schema (including composition types), the map value type matches the generated Go type:

```yaml
type: object
required: [id]
properties:
  id: { type: string }
additionalProperties:
  oneOf:
    - type: string
    - type: integer
```

```go
type ExtensibleRecord struct {
    ID                   string                      `json:"id"`
    AdditionalProperties map[string]StringOrIntOneOf  `json:"-"`
}
```

### Accessor Methods

For ergonomic access to additional properties, we generate getter and setter methods:

```go
func (p *Person) GetAdditionalProperty(key string) (string, bool) {
    if p.AdditionalProperties == nil {
        return "", false
    }
    v, ok := p.AdditionalProperties[key]
    return v, ok
}

func (p *Person) SetAdditionalProperty(key, value string) {
    if p.AdditionalProperties == nil {
        p.AdditionalProperties = make(map[string]string)
    }
    p.AdditionalProperties[key] = value
}
```

## Consequences

### Positive

- **Pure map case is clean**: `map[string]T` with no overhead
- **Absent case is clean**: plain struct with no overhead. Strict object (`additionalProperties: false`) adds `rawFieldKeys` tracking and `Validate()` for unknown-field detection.
- **Mixed case preserves round-trip fidelity**: unknown fields are not silently lost
- **Pragmatic default**: absent `additionalProperties` does not bloat generated code
- **Type-safe additional properties**: when the schema specifies a type, the map is typed accordingly

### Negative

- **Custom MarshalJSON/UnmarshalJSON for mixed objects**: adds generated code volume and slight runtime overhead (double-parse on unmarshal)
- **Key collision risk**: if a JSON key matches a known field name but is also in `AdditionalProperties`, the known field takes precedence. This is correct per spec but may surprise users who manually set `AdditionalProperties`.
- **`encoding/json` v1 field ordering**: marshaled JSON may reorder fields. This is generally acceptable for JSON but may affect snapshot testing.

### Risks

- The double-parse in `UnmarshalJSON` (once for known fields, once for the raw map) has performance implications for large objects. For the common case (small API responses), this is negligible. For bulk data endpoints, users should consider streaming approaches.
- When `encoding/json/v2` stabilizes, its `UnknownFieldsHandler` interface may provide a cleaner mechanism. We design for v1 with awareness of v2's direction.
