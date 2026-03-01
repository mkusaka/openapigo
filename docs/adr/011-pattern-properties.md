# ADR-011: patternProperties Mapping

## Status

Accepted

## Date

2026-03-01

## Context

JSON Schema's `patternProperties` allows defining schemas for properties whose **keys match a regular expression**:

```yaml
type: object
patternProperties:
  "^x-":
    type: string
  "^[0-9]+$":
    type: integer
```

This means: any property whose key starts with `x-` must be a string, and any property whose key is purely numeric must be an integer.

Go has no mechanism for regex-based field dispatch at the type level. A `map[string]T` has a single value type. Struct fields have fixed names.

### Prevalence in practice

`patternProperties` is relatively rare in real-world OpenAPI specs. The most common usage is:

1. **Extension properties** (`^x-`): OpenAPI's own extension mechanism uses `x-` prefixed properties. APIs sometimes expose this pattern to consumers.
2. **Locale maps** (`^[a-z]{2}(-[A-Z]{2})?$`): Internationalized content keyed by locale codes.
3. **Dynamic key namespaces**: Properties grouped by prefix (e.g., `env_*`, `header_*`).

In the vast majority of cases, all pattern groups share the **same value type** (usually `string` or a single schema).

## Decision

### Case 1: Single Pattern, Single Type → `map[string]T`

When there is exactly one pattern, no `properties`, and either `additionalProperties: false` (non-pattern keys forbidden) or the value type is `any`, the schema maps to a typed map. When `additionalProperties` is absent and the value type is specific (e.g., `string`), the generator uses `map[string]json.RawMessage` instead (see Case 2's clarification on absent `additionalProperties`) to avoid unmarshal errors from non-pattern keys with different value types.

```yaml
type: object
patternProperties:
  "^x-":
    type: string
additionalProperties: false
```

```go
type Extensions = map[string]string
```

Key pattern validation is generated in `Validate()`:

```go
func validateExtensionsKeys(m map[string]string) error {
    re := patternExtension // package-level compiled regex (see Regex Compilation section)
    for key := range m {
        if !re.MatchString(key) {
            return &PatternPropertyKeyError{
                Key:     key,
                Pattern: "^x-",
            }
        }
    }
    return nil
}
```

### Case 2: Multiple Patterns, Same Type → `map[string]T`

When all patterns share the same value type:

```yaml
type: object
patternProperties:
  "^x-":
    type: string
  "^ext-":
    type: string
additionalProperties: false
```

```go
type Extensions = map[string]string
```

`Validate()` checks that pattern-matched keys have correct value types and satisfy all constraints from their matching pattern schemas. **Clarification on `additionalProperties` absent and type safety**: When `additionalProperties` is absent (not explicitly set) and the pattern value type `T` is specific (e.g., `string`), keys not matching any pattern are still captured in `map[string]T` by `json.Unmarshal`. If such keys have values of a different type, unmarshal returns an error — rejecting input that is valid per the schema. **To avoid this false rejection**, the generator uses `map[string]json.RawMessage` (same as Case 3) when `additionalProperties` is absent AND the pattern value type is not `any`. This ensures that non-pattern keys with arbitrary value types don't cause unmarshal errors. The `map[string]T` form (Cases 1/2) is used only when (a) `additionalProperties: false` is set (non-pattern keys are forbidden), or (b) `additionalProperties` has the same type `T` as all patterns, or (c) all values can be `any`. Pattern matching is enforced only in `Validate()`: keys not matching any pattern are treated as validation errors only when `additionalProperties: false` is explicitly present. When `additionalProperties` is absent, `Validate()` only checks that pattern-matched keys have correct value types — keys matching no pattern are **not** validation errors (they are valid "additional properties" under the absent-means-true default). **Multi-pattern AND enforcement**: When a key matches multiple `patternProperties` regexes, JSON Schema requires the value to satisfy ALL matching patterns' schemas (AND semantics). `Validate()` checks each key against all matching patterns and reports errors for any constraint violation from any matching pattern. This includes ALL constraints from each pattern's schema (type, format, minLength, maxLength, pattern, minimum, maximum, etc.), not just type checks.

### Case 3: Multiple Patterns, Different Types → `map[string]json.RawMessage` + Typed Accessors

When patterns have different value types:

```yaml
type: object
patternProperties:
  "^x-":
    type: string
  "^[0-9]+$":
    type: integer
```

```go
type MixedPatternProps struct {
    entries map[string]json.RawMessage
}

func (m *MixedPatternProps) UnmarshalJSON(data []byte) error {
    return json.Unmarshal(data, &m.entries)
}

func (m MixedPatternProps) MarshalJSON() ([]byte, error) {
    return json.Marshal(m.entries)
}

// Typed accessor for ^x- pattern (string values)
func (m MixedPatternProps) Extension(key string) (string, error) {
    raw, ok := m.entries[key]
    if !ok {
        return "", &KeyNotFoundError{Key: key}
    }
    var v string
    err := json.Unmarshal(raw, &v)
    return v, err
}

// Typed accessor for ^[0-9]+$ pattern (integer values)
func (m MixedPatternProps) NumericEntry(key string) (int, error) {
    raw, ok := m.entries[key]
    if !ok {
        return 0, &KeyNotFoundError{Key: key}
    }
    var v int
    err := json.Unmarshal(raw, &v)
    return v, err
}

// Iterate all entries
func (m MixedPatternProps) All() map[string]json.RawMessage {
    return m.entries
}
```

The accessor method names are derived from the pattern by extracting meaningful segments:

| Pattern | Accessor name |
|---------|--------------|
| `^x-` | `Extension(key)` |
| `^[0-9]+$` | `NumericEntry(key)` |
| `^header_` | `HeaderEntry(key)` |
| (unrecognizable) | `PatternN(key)` (positional fallback) |

### Case 4: `properties` + `patternProperties` → ADR-006 Pattern

When both fixed `properties` and `patternProperties` are present, we use the same approach as `additionalProperties` (ADR-006): a struct with named fields plus a map for pattern-matched entries.

```yaml
type: object
required: [name]
properties:
  name: { type: string }
patternProperties:
  "^x-":
    type: string
```

```go
type Widget struct {
    Name       string            `json:"name"`
    Extensions map[string]string `json:"-"` // ^x- pattern properties
}

func (w *Widget) UnmarshalJSON(data []byte) error {
    type plain Widget
    if err := json.Unmarshal(data, (*plain)(w)); err != nil {
        return err
    }

    var raw map[string]json.RawMessage
    if err := json.Unmarshal(data, &raw); err != nil {
        return err
    }

    for key, val := range raw {
        if isKnownFieldWidget(key) { // case-insensitive, matching encoding/json v1 (see ADR-006)
            // Skip known fields for map capture — they're already in struct fields.
            // NOTE: If a known field name also matches a patternProperties regex,
            // the pattern's value constraints are still validated in Validate(),
            // not during unmarshal. See Validate() below.
            continue
        }
        if patternExtension.MatchString(key) { // package-level compiled regex (see Regex Compilation section)
            if w.Extensions == nil {
                w.Extensions = make(map[string]string)
            }
            var s string
            if err := json.Unmarshal(val, &s); err != nil {
                return fmt.Errorf("pattern property %q: %w", key, err)
            }
            w.Extensions[key] = s
        }
    }
    return nil
}

func (w Widget) MarshalJSON() ([]byte, error) {
    type plain Widget
    base, err := json.Marshal(plain(w))
    if err != nil {
        return nil, err
    }
    if len(w.Extensions) == 0 {
        return base, nil
    }
    var merged map[string]json.RawMessage
    if err := json.Unmarshal(base, &merged); err != nil {
        return nil, err
    }
    for key, val := range w.Extensions {
        // Guard 1: do not overwrite known struct fields (case-insensitive,
        // matching encoding/json v1 behavior). Same pattern as ADR-006.
        if isKnownFieldWidget(key) {
            continue
        }
        // Guard 2: if AdditionalProperties also exists (Case 5), do not
        // overwrite keys already in the Extensions map. patternProperties
        // takes precedence over additionalProperties per JSON Schema spec.
        // Keys in Extensions (patternProperties) are written first; keys
        // in AdditionalProperties that duplicate a pattern key are skipped.
        encoded, err := json.Marshal(val)
        if err != nil {
            return nil, fmt.Errorf("pattern property %q: %w", key, err)
        }
        merged[key] = encoded
    }
    return json.Marshal(merged)
}

// isKnownFieldWidget uses case-fold comparison to match encoding/json v1's
// case-insensitive field matching behavior (see ADR-006 for rationale).
// NOTE: This deviates from JSON Schema's case-sensitive property matching.
// The trade-off is intentional: if encoding/json maps a key to a struct field
// (via case folding), we must not also capture it as an additional property.
var knownFieldsWidget = []string{"name"}

func isKnownFieldWidget(key string) bool {
    for _, k := range knownFieldsWidget {
        if strings.EqualFold(k, key) {
            return true
        }
    }
    return false
}
```

### Case 5: `patternProperties` + `additionalProperties` Together

When both are present, `patternProperties` takes precedence for matching keys, and `additionalProperties` applies to keys that match no pattern:

```yaml
type: object
patternProperties:
  "^x-":
    type: string
additionalProperties:
  type: integer
```

```go
type Mixed struct {
    Extensions           map[string]string `json:"-"` // ^x- keys
    AdditionalProperties map[string]int    `json:"-"` // all other keys
}
```

**UnmarshalJSON**: Each key is tested against patterns first; only keys matching no pattern go to `AdditionalProperties`. This enforces JSON Schema's evaluation order: `patternProperties` takes precedence over `additionalProperties`.

**MarshalJSON**: Pattern-matched entries are written from their respective pattern maps first, then `AdditionalProperties` entries. Keys in `AdditionalProperties` that match a pattern regex are **skipped** (they should have been classified into the pattern map during unmarshal). If such misclassified keys exist (e.g., from manual construction), they are silently dropped from output — `Validate()` is the authoritative mechanism for detecting misclassifications. Both maps use the `isKnownField` guard (ADR-006) to prevent overwriting known struct fields. This follows the same interaction semantics as ADR-006.

### Regex Compilation

Pattern regexes are compiled once at package init time (not per call):

```go
var (
    patternExtension = regexp.MustCompile(`^x-`)
    patternNumeric   = regexp.MustCompile(`^[0-9]+$`)
)
```

### Validation

`Validate()` checks that:

1. Each key in the map matches its expected pattern
2. Each value conforms to its pattern's schema (for typed maps, type matching is enforced by Go's type system at unmarshal time; other schema constraints such as `minLength`, `maxLength`, `pattern`, `minimum`, etc. are checked by `Validate()`. For `json.RawMessage` maps, validation attempts unmarshal into the expected type and checks all constraints)

```go
func (w Widget) Validate() error {
    // Validate extension map keys match the pattern
    for key := range w.Extensions {
        if !patternExtension.MatchString(key) {
            return &PatternPropertyKeyError{
                Key:     key,
                Pattern: "^x-",
            }
        }
    }

    // Per JSON Schema, patternProperties constraints ALSO apply to properties
    // whose names match the pattern, even if they are listed in "properties".
    // The struct fields were populated by encoding/json (not by our pattern map),
    // so we must validate known fields against matching patterns here.
    // This includes ALL constraints from the patternProperties schema (type, format,
    // minLength, maxLength, pattern, etc.), not just type checks. The generator
    // emits per-field pattern checks when a known field name matches any
    // patternProperties regex at generation time.
    //
    // Example: if properties defines "x-version": {type: string} and
    // patternProperties defines "^x-": {type: string, maxLength: 50},
    // then Validate() checks w.XVersion against maxLength=50.

    return nil
}
```

## Consequences

### Positive

- **Common case (single pattern, single type) is clean**: plain `map[string]T`
- **Mixed with `properties` reuses ADR-006 pattern**: consistent approach
- **Typed accessors for multi-type patterns**: type-safe per-pattern access
- **Regex compiled once**: no per-operation overhead

### Negative

- **Multi-type pattern props use `json.RawMessage`**: requires explicit accessor calls, not direct map access
- **Accessor naming from regex patterns is heuristic**: may produce unclear names for exotic patterns
- **Pattern matching in UnmarshalJSON adds overhead**: regex evaluation per key per unmarshal. Negligible for typical object sizes.

### Risks

- Extremely complex regex patterns may not compile in Go's `regexp` package (Go uses RE2, which does not support lookahead/lookbehind). We document this limitation and fall back to `map[string]any` for unsupported patterns.
- When `patternProperties` and `additionalProperties` overlap (a key matches both a pattern and is "additional"), `patternProperties` takes precedence per JSON Schema spec. Our implementation must enforce this ordering.
