# ADR-012: unevaluatedProperties and unevaluatedItems

## Status

Accepted

## Date

2026-03-01

## Context

JSON Schema Draft 2019-09 introduced `unevaluatedProperties` and `unevaluatedItems` as stricter alternatives to `additionalProperties` and `additionalItems`. They remain available in JSON Schema 2020-12 (which reorganized vocabularies and replaced `additionalItems` with the `prefixItems` + `items` model) and in OAS 3.1+.

### The difference from additionalProperties

`additionalProperties` only considers properties defined in the **immediate** schema's `properties` and `patternProperties`. It does not account for properties defined in subschemas applied via `allOf`, `oneOf`, `anyOf`, `if/then/else`, etc.

`unevaluatedProperties` considers **all properties evaluated by any subschema** in the composition tree.

```yaml
# additionalProperties: false — BROKEN with allOf
allOf:
  - $ref: '#/components/schemas/Base'  # defines: name, type
  - type: object
    properties:
      extra: { type: string }
    additionalProperties: false
    # ← This rejects "name" and "type" because they are not
    #    in THIS schema's properties — they're in Base.
    #    This is a common spec authoring mistake.

# unevaluatedProperties: false — CORRECT with allOf
allOf:
  - $ref: '#/components/schemas/Base'  # defines: name, type
  - type: object
    properties:
      extra: { type: string }
unevaluatedProperties: false
# ← This allows name, type, and extra.
#    Only truly unknown properties are rejected.
```

### The difference from additionalItems

Similarly, `unevaluatedItems` considers items validated by `prefixItems` and any composition keywords:

```yaml
type: array
prefixItems:
  - type: string
  - type: integer
unevaluatedItems: false
# ← No array elements beyond those evaluated by prefixItems are allowed.
#    NOTE: This does NOT enforce a minimum length — without minItems,
#    arrays with 0 or 1 elements are valid (prefixItems only constrains
#    elements that ARE present at each position).
```

### Impact on Go type generation

The key question: do these keywords affect the **type** we generate, or only the **validation**?

Analysis:

| Keyword | Value | Type impact | Validation impact |
|---------|-------|-------------|-------------------|
| `unevaluatedProperties: false` | No extra props | None — struct already has all fields from allOf merge | Reject unknown keys |
| `unevaluatedProperties: true` | Extra props allowed | None — **not** the same as absent (see note below) | None for type generation |
| `unevaluatedProperties: {schema}` | Extra props must match schema | Same as `additionalProperties: {schema}` (ADR-006) | Validate extra prop values |
| `unevaluatedItems: false` | No extra array elements | None — tuple struct already fixed size | Reject extra elements |
| `unevaluatedItems: true` | Extra elements allowed | None — **not** the same as absent (see note below) | None for type generation |
| `unevaluatedItems: {schema}` | Extra elements must match schema | Same as tuple + `AdditionalItems []T` | Validate extra elements |

**Note on `true` vs absent**: Per JSON Schema 2020-12, `unevaluatedProperties: true` and `unevaluatedItems: true` are **not** semantically identical to absent. When present (even as `true`), they generate **annotations** that mark all properties/items as "evaluated" — this affects how parent or sibling schemas compute the evaluated set when they use `unevaluatedProperties: false` or `unevaluatedItems: false`. For **Go type generation** purposes, the `true` case produces the same Go type as absent. However, the generator **must track the annotation** during codegen: when resolving the evaluated property set for a parent schema's `unevaluatedProperties: false`, any child branch with `unevaluatedProperties: true` contributes **all** properties as evaluated (not just those explicitly listed in `properties`). The generator handles this by marking such branches as "evaluates-all" during composition resolution, ensuring the parent's `Validate()` correctly computes the evaluated set.

**Conclusion**: `unevaluatedProperties` and `unevaluatedItems` have **no impact on Go type generation** in the `false` and `true` cases. Only the `{schema}` case adds a structural element (a map or slice for the extra items), which follows the same pattern as `additionalProperties` (ADR-006) and `prefixItems` + `items` (ADR-009).

## Decision

### unevaluatedProperties: false → Validate() Only

For `allOf` schemas, the generated struct already contains all properties from all subschemas (because ADR-002 merges them into a single flat struct). For `oneOf`/`anyOf`, ADR-002 generates wrapper types with variant pointer fields — the evaluated property set depends on which variant matched at runtime and is computed dynamically in `Validate()` (see Risks section). The `unevaluatedProperties: false` constraint is enforced in `Validate()`.

To detect unevaluated (unknown) properties, we need the raw JSON field keys. We track these via a hidden field populated during unmarshal:

```go
type PetWithBreed struct {
    Name  string  `json:"name"`
    Tag   *string `json:"tag,omitzero"`
    Breed string  `json:"breed"`

    rawFieldKeys []string // populated by UnmarshalJSON, unexported
}

func (v *PetWithBreed) UnmarshalJSON(data []byte) error {
    // Standard unmarshal
    type plain PetWithBreed
    if err := json.Unmarshal(data, (*plain)(v)); err != nil {
        return err
    }

    // Track raw field keys for unevaluatedProperties validation
    var raw map[string]json.RawMessage
    if err := json.Unmarshal(data, &raw); err != nil {
        return err
    }
    v.rawFieldKeys = make([]string, 0, len(raw))
    for key := range raw {
        v.rawFieldKeys = append(v.rawFieldKeys, key)
    }
    return nil
}

func (v PetWithBreed) Validate() error {
    // Compute the set of evaluated property names.
    //
    // IMPORTANT: Per JSON Schema 2020-12, "evaluated" means a property was
    // successfully processed by a subschema that APPLIED to the instance.
    // For branch-dependent keywords (oneOf, anyOf, if/then/else):
    //   - Only the MATCHED branch contributes evaluated properties
    //   - Failed branches do NOT contribute (their properties are unevaluated)
    //
    // For allOf: all branches always apply, so all contribute.
    // For patternProperties: any key matching a pattern is evaluated.
    evaluated := map[string]bool{
        "name":  true, // from Base (allOf[0]) — allOf always applies
        "tag":   true, // from Base (allOf[0])
        "breed": true, // from allOf[1]
    }

    // For oneOf: add properties from the ONE matched variant only.
    // The matched variant is recorded during UnmarshalJSON (ADR-002/003).
    // Example: if oneOf matched Cat, add Cat's properties but NOT Dog's.
    //
    // For anyOf: add properties from ALL matched variants (not just one).
    // ALL variants whose unmarshal succeeded contribute to the evaluated set.
    // Example: if anyOf matched both Cat and Dog, add BOTH Cat's and Dog's properties.
    //
    // IMPORTANT: Per JSON Schema 2020-12, "evaluated" means a property was
    // processed by a subschema that APPLIED to the instance (i.e., passed
    // schema validation, not just unmarshaled successfully). During unmarshal,
    // the variant matching logic (ADR-002/003) records which branches succeeded.
    // However, unmarshal success alone does not guarantee schema validity
    // (e.g., encoding/json silently ignores missing required fields). Therefore,
    // Validate() re-checks recorded matches against their branch constraints
    // (required fields, type constraints, etc.) before including their
    // properties in the evaluated set. This two-phase approach ensures that
    // only truly matching branches contribute evaluated properties.

    // For patternProperties: any key matching a pattern regex is evaluated
    // Example: if patternProperties has "^x-": {type: string},
    // then "x-custom" is evaluated even though it's not in properties.

    for _, key := range v.rawFieldKeys {
        if !evaluated[key] {
            return &UnevaluatedPropertyError{
                Property:  key,
                Evaluated: maps.Keys(evaluated),
            }
        }
    }
    return nil
}
```

**Note**: The `rawFieldKeys` field is generated when any of the following keywords is present: `unevaluatedProperties: false`, `additionalProperties: false` (ADR-006), or `dependentRequired` (ADR-010). These all require raw JSON key presence detection. Schemas without any of these keywords do not incur the overhead of tracking raw keys.

### unevaluatedProperties: {schema} → AdditionalProperties Map

When `unevaluatedProperties` specifies a schema (not just `false`), unevaluated properties must conform to that schema. This follows the same pattern as `additionalProperties: {schema}` (ADR-006):

```yaml
allOf:
  - type: object
    properties:
      name: { type: string }
unevaluatedProperties:
  type: string
```

```go
type Named struct {
    Name                 string            `json:"name"`
    AdditionalProperties map[string]string `json:"-"`
}
```

The `UnmarshalJSON` logic is the same as ADR-006 Case 3, but the set of "known fields" is gathered from **all subschemas in the composition tree**, not just the immediate schema.

### unevaluatedItems: false → Validate() Only

For tuple types (prefixItems), `unevaluatedItems: false` means no elements beyond those evaluated by `prefixItems`, `items`, and `contains` are allowed. Per JSON Schema 2020-12, items evaluated by **any** of these keywords are considered "evaluated":

- `prefixItems` evaluates items by position
- `items` evaluates all items beyond prefixItems
- `contains` evaluates items matching the contains schema

When `items` is present (not just `prefixItems`), **all elements beyond `prefixItems` are evaluated by `items`**, making `unevaluatedItems: false` effectively a no-op (no elements can be "unevaluated"). The generator detects this case and omits `rawElements`/`rawLen` tracking since it would be dead code. `unevaluatedItems: false` is only meaningful when:

1. Only `prefixItems` is used (no `items`) — rejects elements beyond the tuple
2. Only `contains` is used (no `items`) — rejects elements not matched by contains
3. `prefixItems` + `contains` (no `items`) — elements evaluated by either prefixItems (by position) or contains (by schema match)

**`items` + `contains` + `unevaluatedItems: false`**: When both `items` and `contains` are present, `items` evaluates ALL elements beyond `prefixItems`, so `contains`-matched indices are a subset of the already-evaluated set. No sparse index tracking is needed — the `items` evaluation subsumes everything.

The tuple struct is already fixed-size (ADR-009). Validation rejects extra elements.

**Simple case (prefixItems only, no `contains`)**: When only `prefixItems` is used (no `items`, no `contains`), the evaluated indices are simply `0..len(prefixItems)-1`. Validation checks that no elements exist beyond the prefix:

```go
type MyTuple struct {
    V0       string
    V1       int
    rawLen   int // populated by UnmarshalJSON
}

func (t *MyTuple) UnmarshalJSON(data []byte) error {
    var arr []json.RawMessage
    if err := json.Unmarshal(data, &arr); err != nil {
        return err
    }
    t.rawLen = len(arr)
    // NOTE: prefixItems does NOT enforce a minimum array length.
    // Without explicit minItems, shorter arrays are valid — elements
    // are validated against their positional schema only when present.
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
    return nil
}

func (t MyTuple) Validate() error {
    if t.rawLen > 2 {
        return &UnevaluatedItemsError{
            Expected: 2,
            Actual:   t.rawLen,
        }
    }
    return nil
}
```

**Complex case (`contains` + `unevaluatedItems: false`)**: When `contains` is present, items matching the `contains` schema are considered "evaluated" regardless of their position. The evaluated set is **sparse** (not contiguous), so a simple length check is insufficient. Instead, `Validate()` must track which indices were evaluated by `contains`:

```go
func (t MyContainsTuple) Validate() error {
    // evaluatedIndices tracks which array indices have been evaluated
    // by prefixItems, items, or contains.
    evaluatedIndices := make(map[int]bool)

    // Indices evaluated by prefixItems (0..N-1)
    for i := 0; i < t.prefixLen; i++ {
        evaluatedIndices[i] = true
    }

    // Indices evaluated by contains: check each raw element against
    // the contains schema and mark matching indices as evaluated.
    for i, raw := range t.rawElements {
        if matchesContainsSchema(raw) {
            evaluatedIndices[i] = true
        }
    }

    // Any index NOT in evaluatedIndices is unevaluated
    for i := 0; i < len(t.rawElements); i++ {
        if !evaluatedIndices[i] {
            return &UnevaluatedItemsError{
                UnevaluatedIndex: i,
                TotalItems:       len(t.rawElements),
            }
        }
    }
    return nil
}
```

### unevaluatedItems: {schema} → Typed Validation of Unevaluated Elements

When `unevaluatedItems` specifies a schema, unevaluated elements must conform to that schema. Unlike `items` (which evaluates contiguous tail elements), `unevaluatedItems` applies to elements that were **not evaluated by any other keyword** (`prefixItems`, `items`, `contains`, or composition branches). When `contains` is involved, the unevaluated indices can be **sparse** (non-contiguous), requiring per-index tracking rather than a simple slice:

```go
type MyTuple struct {
    V0          string
    V1          int
    rawElements []json.RawMessage // preserved for unevaluatedItems validation
}

func (t MyTuple) Validate() error {
    // Determine which indices were evaluated by prefixItems, items, contains
    evaluatedIndices := make(map[int]bool)
    for i := 0; i < min(len(t.rawElements), 2); i++ {
        evaluatedIndices[i] = true // prefixItems
    }
    // ... contains evaluation ...

    // Validate unevaluated elements against the schema
    for i, raw := range t.rawElements {
        if !evaluatedIndices[i] {
            var val bool
            if err := json.Unmarshal(raw, &val); err != nil {
                return &UnevaluatedItemsError{Index: i, Err: err}
            }
        }
    }
    return nil
}
```

When `contains` is not used and only `prefixItems` is present, the unevaluated elements form a contiguous tail (indices >= prefixItems count), and a simpler `AdditionalItems []T` slice representation is used (same as ADR-009):

```go
type MyTuple struct {
    V0              string
    V1              int
    AdditionalItems []bool // unevaluatedItems: {type: boolean}, no contains
}
```

### Generation Conditions

| Condition | Extra generated code |
|-----------|---------------------|
| `unevaluatedProperties: false` present | `rawFieldKeys` field + custom `UnmarshalJSON` + `Validate()` check |
| `unevaluatedProperties: {schema}` present | `AdditionalProperties` map (same as ADR-006) |
| `unevaluatedItems: false` present (no `contains`, no `items`) | `rawLen` field + `Validate()` check |
| `unevaluatedItems: false` present (with `contains`, no `items`) | `rawElements []json.RawMessage` field + sparse index tracking in `Validate()` |
| `unevaluatedItems: {schema}` present (no `contains`, no `items`) | `AdditionalItems` slice (same as ADR-009) |
| `unevaluatedItems: {schema}` present (with `contains`, no `items`) | `rawElements []json.RawMessage` field + sparse index validation in `Validate()` |
| `unevaluatedItems` present (with `items`) | No extra code — `items` evaluates all remaining elements, making `unevaluatedItems` a no-op |
| None of the above | No extra code |

## Consequences

### Positive

- **No unnecessary code**: raw field tracking is only added when `unevaluatedProperties: false` is actually used
- **Correct composition-aware validation**: the evaluated field set is gathered from the full allOf/oneOf/anyOf tree, not just the immediate schema
- **Consistent with existing patterns**: `{schema}` variants reuse ADR-006 and ADR-009 mechanisms
- **Validation is opt-in**: consistent with ADR-001 philosophy

### Negative

- **Double-parse overhead when `unevaluatedProperties: false` is present**: unmarshal into struct + unmarshal into raw map to capture field keys. Same as ADR-006 for `additionalProperties` with defined properties.
- **`rawFieldKeys` is a hidden field**: technically part of the struct's memory but not exported. Users cannot accidentally break it, but it does increase struct size slightly.

### Risks

- `oneOf`/`anyOf` with `unevaluatedProperties: false` requires knowing which variant matched to determine the evaluated field set. The variant matched during unmarshal must be tracked. This interacts with ADR-002/003's composition type handling. **Failed branches must NOT contribute to the evaluated set** — this is a common implementation error. For `anyOf`, **all matching branches** contribute (unlike oneOf where only one matches).
- When `unevaluatedProperties` is a **schema** (not just `false`) and the type uses `oneOf`/`anyOf`, the set of "known" (evaluated) properties depends on which branch matched at runtime. The `UnmarshalJSON` must record which variant was selected so that `Validate()` can compute the correct evaluated set dynamically.
- `if/then/else` similarly requires knowing whether `if` matched to determine whether `then` or `else` properties are evaluated.
- `patternProperties` keys that match a regex pattern are evaluated. The generator must emit pattern-matching logic in `Validate()` to check each raw key against all `patternProperties` patterns.
- `unevaluatedItems` interaction with `contains`: Per JSON Schema 2020-12, items that match the `contains` schema are considered evaluated. When `unevaluatedItems: false` is combined with `contains` (but no `items`), elements evaluated by `prefixItems` (by position) and elements matching `contains` (by schema match) are both considered evaluated. The runtime must track which array indices matched `contains` during validation, in addition to the indices covered by `prefixItems`.
- Deeply nested composition trees (allOf containing oneOf containing allOf) make the "evaluated properties" set complex to compute. For simple cases (static property lists), we resolve at codegen time. For dynamic cases (oneOf/if-then-else branches), the evaluated set is computed at runtime in `Validate()` based on the matched variant.
