# ADR-009: Type and Format Mapping

## Status

Accepted

## Date

2026-03-01

## Context

OpenAPI schemas use `type` and `format` to describe data shapes. Go needs concrete type mappings for each combination. Several edge cases require careful design:

1. **String formats**: `date-time`, `date`, `uuid`, `uri`, `email`, `byte`, `binary`, `password`
2. **Number formats**: `int32`, `int64`, `float`, `double`
3. **Multi-type** (OAS 3.1): `type: [string, integer]`
4. **Free-form objects**: `type: object` with no `properties`
5. **`prefixItems`** (OAS 3.1): tuple-like arrays
6. **`deprecated`**: annotation for deprecated schemas/operations

## Decision

### Primitive Type Mapping

| OpenAPI type | OpenAPI format | Go type | Notes |
|-------------|---------------|---------|-------|
| `string` | (none) | `string` | |
| `string` | `date-time` | `time.Time` | RFC 3339 |
| `string` | `date` | `openapigo.Date` | Custom type wrapping `time.Time` |
| `string` | `time` | `string` | No stdlib equivalent; kept as string |
| `string` | `duration` | `string` | ISO 8601 duration has no stdlib type |
| `string` | `uuid` | `string` | See discussion below |
| `string` | `uri` | `string` | `net/url.URL` is struct, not convenient for JSON |
| `string` | `email` | `string` | No stdlib type |
| `string` | `hostname` | `string` | No stdlib type |
| `string` | `ipv4` | `string` | `netip.Addr` possible but opinionated |
| `string` | `ipv6` | `string` | Same as ipv4 |
| `string` | `byte` | `[]byte` | Base64-encoded; `encoding/json` handles this natively |
| `string` | `binary` | `[]byte` | Context-free default. Overridden by media type context (see ADR-016): `multipart/form-data` → `openapigo.File`, `application/octet-stream` → `io.Reader`. **OAS 3.1 note**: JSON Schema 2020-12 uses `contentEncoding` and `contentMediaType` as the primary mechanism for binary strings. When `contentEncoding: base64` is present, the type is `[]byte` (same as `format: byte`). `contentMediaType` alone (without `contentEncoding`) is **informational only** — it describes the media type of the decoded content but does not affect the Go type mapping. The generator recognizes both `format: binary` (compatibility) and `contentEncoding` (3.1 native), with `contentEncoding` taking precedence when both are present. |
| `string` | `password` | `string` | Display hint only, no type difference |
| `integer` | (none) | `int64` | See note below |
| `integer` | `int32` | `int32` | |
| `integer` | `int64` | `int64` | |
| `number` | (none) | `float64` | |
| `number` | `float` | `float32` | |
| `number` | `double` | `float64` | |
| `boolean` | (none) | `bool` | |
| `array` | (none) | `[]T` | `T` determined by `items` |
| `object` | (none) | struct or `map[string]any` | Depends on `properties` presence |
| `null` | (none) | (used in Nullable[T]) | Not a standalone Go type |

**Integer without format**: We default to `int64` instead of `int` because JSON Schema `integer` has no size limit, and `int` is 32-bit on GOARCH=386/arm (32-bit platforms). Using `int64` ensures consistent behavior across all architectures. Users who prefer `int` (for ergonomics on 64-bit-only targets) can use `--integer-type=int`.

### String Format: `date-time`

Maps to `time.Time`. This is the most common and uncontroversial mapping:

```go
type Event struct {
    CreatedAt time.Time `json:"createdAt"`
}
```

`time.Time` marshals/unmarshals as RFC 3339 by default in `encoding/json`, which matches OpenAPI's `date-time` format.

### String Format: `date`

Go's `time.Time` includes time-of-day information, which is undesirable for date-only values. We provide a custom `Date` type:

```go
// In the runtime library
type Date struct {
    Year  int
    Month time.Month
    Day   int
}

func (d Date) MarshalJSON() ([]byte, error) {
    return json.Marshal(d.String())
}

func (d *Date) UnmarshalJSON(data []byte) error {
    var s string
    if err := json.Unmarshal(data, &s); err != nil {
        return err
    }
    t, err := time.Parse("2006-01-02", s)
    if err != nil {
        return err
    }
    d.Year, d.Month, d.Day = t.Date()
    return nil
}

func (d Date) String() string {
    return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

func (d Date) ToTime() time.Time {
    return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
}

func (d Date) IsZero() bool {
    return d.Year == 0 && d.Month == 0 && d.Day == 0
}
```

### String Format: `uuid`

We map `uuid` to `string`, not to a third-party UUID library. Reasons:

1. **No stdlib UUID type** in Go
2. Importing `github.com/google/uuid` would impose a dependency on all generated code
3. UUID is fundamentally a string in JSON — the format is a validation hint, not a type distinction

Users who want typed UUIDs can use a CLI flag `--uuid-package=github.com/google/uuid` to override:

```go
// Default: string
type Pet struct {
    ID string `json:"id"` // format: uuid
}

// With --uuid-package=github.com/google/uuid:
import "github.com/google/uuid"
type Pet struct {
    ID uuid.UUID `json:"id"` // format: uuid
}
```

### Unknown Formats

Formats not listed in the table above are treated as `string` (for `type: string`) or the base type. A warning is emitted during generation:

```
WARN: Unknown format "custom-id" for type "string", using string
```

Custom format mappings can be provided via CLI:

```
--format-mapping="string:custom-id=mypackage.CustomID"
```

### Array Type Mapping

```yaml
type: array
items:
  type: string
```

```go
[]string
```

**OAS 3.0**: `items` is **required** when `type: array` (per the OAS 3.0.x specification). The generator emits an error if `items` is absent in an OAS 3.0 spec.

**OAS 3.1**: `items` is optional (JSON Schema 2020-12 semantics). When absent, the array accepts any items:

```go
// OAS 3.1: type: array (no items) → unconstrained element type
[]any
```

When `items` references a schema:

```go
// items: { $ref: '#/components/schemas/Pet' }
[]Pet
```

### Multi-Type (OAS 3.1)

OAS 3.1 allows `type` to be an array:

```yaml
type: [string, integer]
```

This is semantically equivalent to:

```yaml
anyOf:
  - type: string
  - type: integer
```

**Important**: `type: [T1, T2]` means "the value's type is **any of** the listed types", which aligns with `anyOf` semantics (not `oneOf`). In practice, for primitive types the distinction is moot (a value cannot simultaneously be a string and an integer), but we normalize to `anyOf` for correctness.

We normalize multi-type arrays to `anyOf` during schema parsing and apply the same anyOf handling from ADR-002:

```go
type StringOrIntAnyOf struct {
    StringValue *string
    IntValue    *int64 // int64, consistent with integer default (see table above)
}
```

**Subset type handling**: When the type array contains subset relationships (e.g., `type: [integer, number]` — every integer is also a valid number), naive oneOf normalization would cause the exactly-one-match constraint to always fail. For known subset pairs:

- `[integer, number]` → `number` (integer is a subset of number; emit `float64` and document that values may be integral)
- `[integer, number, ...]` → merge `integer` and `number` into `number` before applying anyOf

The generator detects these subset relationships and simplifies before generating the anyOf wrapper.

For the common case of `type: [T, "null"]`, we optimize to `Nullable[T]` (per ADR-004):

```yaml
type: [string, "null"]
# → Nullable[string]
```

### Free-Form Objects

An object schema with no `properties` and no `additionalProperties` constraint:

```yaml
# Free-form object
type: object
```

Maps to `map[string]any`:

```go
type Metadata = map[string]any
```

An object with `properties: {}` (explicitly empty) and `additionalProperties: false`:

```yaml
type: object
properties: {}
additionalProperties: false
```

Maps to an empty struct. **Important**: `encoding/json` v1 silently ignores unknown fields when unmarshaling into a struct, so `EmptyObject{}` does **not** reject `{"foo": 1}` — it accepts it silently. To enforce the `additionalProperties: false` constraint (reject unknown fields), `Validate()` is generated with raw field key tracking (per ADR-012), or users can use `encoding/json/v2`'s strict mode:

```go
type EmptyObject struct{}
```

### prefixItems (OAS 3.1 Tuples)

OAS 3.1 introduces `prefixItems` for tuple-like validation:

```yaml
type: array
prefixItems:
  - type: string
  - type: integer
  - type: boolean
```

Go does not have tuple types. We map this to a generated struct with positional fields and custom JSON marshal/unmarshal:

```go
type StringIntBoolTuple struct {
    V0  string
    V1  int64
    V2  bool
    Len int // number of elements present (set by UnmarshalJSON)
}

func (t *StringIntBoolTuple) UnmarshalJSON(data []byte) error {
    var arr []json.RawMessage
    if err := json.Unmarshal(data, &arr); err != nil {
        return err
    }
    // NOTE: prefixItems does NOT enforce a minimum array length.
    // Without explicit minItems, arrays shorter than the prefixItems count
    // are valid per JSON Schema 2020-12 — only present elements are validated
    // against their positional schema. Missing elements get Go zero values.
    // Use Validate() with minItems to enforce minimum length.
    t.Len = len(arr)
    if t.Len > 3 { t.Len = 3 } // cap at number of prefixItems fields
    if len(arr) >= 1 {
        if err := json.Unmarshal(arr[0], &t.V0); err != nil {
            return fmt.Errorf("element 0: %w", err)
        }
    }
    if len(arr) >= 2 {
        if err := json.Unmarshal(arr[1], &t.V1); err != nil {
            return fmt.Errorf("element 1: %w", err)
        }
    }
    if len(arr) >= 3 {
        if err := json.Unmarshal(arr[2], &t.V2); err != nil {
            return fmt.Errorf("element 2: %w", err)
        }
    }
    return nil
}

func (t StringIntBoolTuple) MarshalJSON() ([]byte, error) {
    arr := make([]any, 0, t.Len)
    if t.Len >= 1 { arr = append(arr, t.V0) }
    if t.Len >= 2 { arr = append(arr, t.V1) }
    if t.Len >= 3 { arr = append(arr, t.V2) }
    return json.Marshal(arr)
}
```

The `Len` field tracks how many positional elements were present during unmarshal (set in `UnmarshalJSON`). This ensures round-trip fidelity: unmarshaling `["hello"]` produces `Len: 1`, and `MarshalJSON` outputs `["hello"]` — not `["hello", 0, false]`. When constructing a tuple in Go code, users must set `Len` to indicate how many fields are meaningful.

**`prefixItems` without `items`**: When `items` is absent (no keyword), JSON Schema 2020-12 defaults to `items: true` — additional elements beyond the prefix are allowed with no constraints. The generated struct does **not** preserve these additional elements (they are silently dropped during unmarshal, similar to how `encoding/json` drops unknown object fields). If users need to preserve additional elements, they should explicitly declare `items: <schema>` in their OpenAPI spec, which triggers the `AdditionalItems` field generation shown below.

When `prefixItems` is combined with `items` (additional items beyond the tuple prefix), we add a catch-all field:

```yaml
type: array
prefixItems:
  - type: string
  - type: integer
items:
  type: boolean
```

```go
type MyTuple struct {
    V0             string
    V1             int64
    AdditionalItems []bool
}
```

### Deprecated

Schemas or operations marked as `deprecated: true` generate Go `Deprecated` doc comments:

```yaml
OldPet:
  type: object
  deprecated: true
  properties:
    name: { type: string }
```

```go
// Deprecated: OldPet is deprecated.
type OldPet struct {
    Name string `json:"name"`
}
```

For deprecated operations:

```go
// Deprecated: GetOldPet is deprecated.
func (c *Client) GetOldPet(ctx context.Context) (*OldPet, error) {
    // ...
}
```

Go tooling (gopls, staticcheck) recognizes the `Deprecated:` comment prefix and shows warnings in IDEs.

### Schema Without `type`

When a schema has no `type` field (valid in OpenAPI — the value can be anything):

```yaml
# No type specified
AnyValue: {}
```

```go
type AnyValue = any
```

When combined with object-specific keywords but no type:

```yaml
# Has properties but no explicit type (implicitly object)
MySchema:
  properties:
    name: { type: string }
```

We infer `type: object` **only** when **exclusively** object-specific keywords are present (`properties`, `additionalProperties`, `required`) and the schema is in an OAS 3.0 spec. This inference emits a **generation-time warning**: `WARN: schema "MySchema" has no explicit 'type' but has object-specific keywords (properties, additionalProperties, required). Inferring type: object for OAS 3.0 compatibility. Add explicit 'type: object' to suppress this warning.` The warning ensures users are aware of the inference, which technically narrows the schema beyond its JSON Schema semantics. Note: `patternProperties` is NOT used for inference because it is not part of the OAS 3.0 Schema Object (it is a JSON Schema keyword available only in OAS 3.1+). In OAS 3.1 (JSON Schema 2020-12 semantics), `properties`/`required`/etc. are **applicator** keywords that apply to any instance type — they do not declare that the value must be an object. A schema with `properties` but no `type` can validate non-object values (the `properties` keyword simply has no effect on them). Therefore, in OAS 3.1 mode, we do **not** infer `type: object` from these keywords alone; instead, the schema is treated as unconstrained by type, and the Go type is `any` unless further analysis (e.g., all composition branches are objects) narrows it.

**Important**: Composition keywords (`allOf`, `oneOf`, `anyOf`) do **not** imply object type. These keywords can combine schemas of any type (strings, arrays, objects, etc.). When a schema has only composition keywords and no `type`, we resolve the type by analyzing the composed schemas rather than defaulting to object:

```yaml
# No type, but anyOf contains string schemas — NOT an object
FlexibleValue:
  anyOf:
    - type: string
    - type: integer
# → StringOrIntegerAnyOf (from ADR-002), NOT map[string]any
```

### Recursive Schemas

Schemas can reference themselves:

```yaml
TreeNode:
  type: object
  properties:
    value: { type: string }
    children:
      type: array
      items:
        $ref: '#/components/schemas/TreeNode'
```

In Go, recursive types require indirection to break the infinite size. Slice-based recursion (e.g., `[]TreeNode`) works without pointers because a Go slice header is fixed-size. However, for **direct struct recursion** (e.g., `TreeNode` containing a `TreeNode` field), a pointer is needed. Array-of-self recursion via slices is the common case:

```go
type TreeNode struct {
    Value    string     `json:"value,omitzero"`
    Children []TreeNode `json:"children,omitzero"` // slice: no pointer needed
}
```

The generator detects cycles in the schema reference graph using a **visited set** during depth-first traversal and automatically inserts pointer indirection **only where structurally needed**. Specifically, pointer indirection is inserted at a back-edge only when the reference would cause an infinite-size struct — i.e., when the field type is a **direct struct** (not behind a slice, map, or pointer, which already provide indirection). For example, `[]TreeNode` does not need `[]*TreeNode` because a slice header is fixed-size; but a field `Parent TreeNode` needs `Parent *TreeNode`. **Mutual recursion** (e.g., `A → B → A`) is supported: the cycle detector operates on the full `$ref` graph, not just self-references. For mutual recursion, the pointer is inserted at the **first back-edge** encountered during depth-first traversal that requires indirection (deterministic because schemas are processed in alphabetical order). Example: `A.field1 → B`, `B.field2 → A` → one of the fields gets `*A` or `*B` (whichever closes the cycle first in DFS order).

## Consequences

### Positive

- **Minimal external dependencies**: only `time.Time` from stdlib for date-time; all other formats use `string` or custom runtime types
- **`Date` type prevents time-of-day ambiguity**: cleaner than `time.Time` for date-only fields
- **Multi-type normalized to anyOf**: no separate code path, reuses existing composition logic
- **Recursive schemas handled automatically**: cycle detection prevents infinite struct sizes
- **`deprecated` is visible in IDEs**: standard Go doc comment convention

### Negative

- **UUID as `string`**: loses UUID-specific validation and parsing. Mitigated by `--uuid-package` flag.
- **No IP address type**: `netip.Addr` would be more type-safe but imposes opinions
- **Tuple structs have opaque field names**: `V0`, `V1`, `V2` are not descriptive. Users should prefer named schemas.
- **`Date` runtime type is a dependency**: minimal but non-zero

### Risks

- Custom format mappings (`--format-mapping`) increase complexity and may produce type mismatches if the custom type doesn't implement `json.Marshaler`/`json.Unmarshaler`. We document the interface requirements.
- Recursive schemas with multiple mutual references may produce complex pointer patterns. The cycle detector handles common mutual recursion (two-type cycles like `A → B → A`) but exotic cases (three or more types forming a cycle, or multiple independent cycles) may require manual review of the generated pointer placement.
