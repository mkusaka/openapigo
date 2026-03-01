# ADR-003: Discriminator Handling Strategy

## Status

Accepted

## Date

2026-03-01

## Context

When unmarshaling a oneOf or anyOf value from JSON, we must determine which variant(s) the value matches. OpenAPI provides the `discriminator` object as an explicit hint, but it is optional. Without it, we need heuristic strategies.

The `discriminator` object has two fields:

- `propertyName` (required): the JSON property whose value identifies the variant
- `mapping` (optional): explicit map from discriminator values to schema references. Values can be either (a) a full `$ref` path (e.g., `'#/components/schemas/Cat'`) or (b) a bare schema name (e.g., `Cat`) which is resolved relative to the spec's schema namespace. If `mapping` is absent, the discriminator value is assumed to match the schema name extracted from the `$ref`.

### Discriminator patterns observed in real-world APIs

1. **Basic with explicit mapping**: `discriminator.mapping` maps string values to `$ref`s
2. **Implicit mapping**: no `mapping` — the `$ref` schema name IS the discriminator value
3. **Multi-value mapping**: multiple discriminator values map to the same schema
4. **allOf inheritance**: discriminator on a base schema; variants extend it via `allOf`
5. **Nested discriminators**: parent discriminator routes to a child schema that has its own discriminator
6. **anyOf + discriminator**: discriminator provides primary routing but other variants may also match

### The challenge without discriminator

Go's `json.Unmarshal` is permissive — it ignores unknown fields and zero-initializes missing fields. This means unmarshaling valid JSON into *any* struct will almost always succeed, making "try unmarshal and check for error" unreliable as a variant detection strategy.

We need more precise detection methods.

## Decision

### Priority-Based Strategy Selection

During code generation, we analyze the schemas in each oneOf/anyOf and automatically select the most precise detection strategy available. Strategies are tried in priority order:

```
Priority 1: Discriminator property (explicit or implicit mapping)
Priority 2: Required field difference
Priority 3: Unique property names
Priority 4: JSON value type discrimination
Priority 5: Full unmarshal fallback (last resort)
```

The generator selects the **highest-priority strategy that is applicable** to the given set of schemas.

### Strategy 1: Discriminator Property (Highest Priority)

When `discriminator` is present, we generate a fast-path that extracts the discriminator field from the raw JSON and switches on its value before performing a full unmarshal of the selected variant.

#### 1a. Explicit Mapping

```yaml
discriminator:
  propertyName: petType
  mapping:
    cat: '#/components/schemas/Cat'
    dog: '#/components/schemas/Dog'
```

```go
func (v *PetOneOf) UnmarshalJSON(data []byte) error {
    disc, err := extractDiscriminator(data, "petType")
    if err != nil {
        return err
    }
    switch disc {
    case "cat":
        v.Cat = new(Cat)
        return json.Unmarshal(data, v.Cat)
    case "dog":
        v.Dog = new(Dog)
        return json.Unmarshal(data, v.Dog)
    default:
        return &UnknownDiscriminatorError{
            Property: "petType",
            Value:    disc,
            Expected: []string{"cat", "dog"},
        }
    }
}
```

#### 1b. Implicit Mapping (no `mapping` field)

When `mapping` is omitted, the discriminator value equals the schema name from the `$ref`:

```yaml
oneOf:
  - $ref: '#/components/schemas/Cat'   # discriminator value: "Cat"
  - $ref: '#/components/schemas/Dog'   # discriminator value: "Dog"
discriminator:
  propertyName: petType
```

```go
switch disc {
case "Cat":
    v.Cat = new(Cat)
    return json.Unmarshal(data, v.Cat)
case "Dog":
    v.Dog = new(Dog)
    return json.Unmarshal(data, v.Dog)
}
```

#### 1c. Multi-Value Mapping

Multiple discriminator values can map to the same schema:

```yaml
discriminator:
  propertyName: type
  mapping:
    house_cat: '#/components/schemas/Cat'
    street_cat: '#/components/schemas/Cat'
    dog: '#/components/schemas/Dog'
```

```go
switch disc {
case "house_cat", "street_cat":
    v.Cat = new(Cat)
    return json.Unmarshal(data, v.Cat)
case "dog":
    v.Dog = new(Dog)
    return json.Unmarshal(data, v.Dog)
}
```

### Strategy 2: Required Field Difference

When no discriminator is present, we check whether the set of `required` fields differs between variants. If they do, we can detect the variant by checking for the presence of required fields in the JSON.

**Limitation**: This strategy relies on field presence in the JSON object, not on schema validation. Since `encoding/json` v1 accepts unknown fields by default (unless `additionalProperties: false` is explicitly set and enforced), a JSON object may contain fields from multiple variant schemas. The required-field check narrows the candidate set but does not guarantee schema-level validity. The subsequent `json.Unmarshal` into the candidate struct provides a secondary check (type mismatches will fail). For schemas where required fields are insufficient to distinguish variants, the generator falls through to lower-priority strategies.

```yaml
# Cat requires: [name, meow]
# Dog requires: [name, bark]
# Differentiating fields: meow (Cat-only), bark (Dog-only)
```

```go
func (v *CatOrDogOneOf) UnmarshalJSON(data []byte) error {
    fields, err := extractFieldKeys(data)
    if err != nil {
        return err
    }

    var matched int
    if hasAll(fields, "name", "meow") {
        v.Cat = new(Cat)
        if err := json.Unmarshal(data, v.Cat); err == nil {
            matched++
        } else {
            v.Cat = nil
        }
    }
    if hasAll(fields, "name", "bark") {
        v.Dog = new(Dog)
        if err := json.Unmarshal(data, v.Dog); err == nil {
            matched++
        } else {
            v.Dog = nil
        }
    }

    return v.validateMatchCount(matched, data)
}
```

`extractFieldKeys` parses the JSON object once into `map[string]json.RawMessage` to check field presence without full struct unmarshaling.

### Strategy 3: Unique Property Names

When required fields overlap (or are not specified), we look for **required** properties that are unique to each variant — properties that are both required and exist in one schema but not in any other. Only required properties are used because optional properties may be absent in a valid instance, causing false-negative matches. If no required unique properties exist for all variants, this strategy is not applicable and the generator falls back to Strategy 4 or 5.

**Limitation**: This heuristic uses schema-defined property names, not runtime validation. Since JSON Schema (with absent `additionalProperties`) allows any properties in an object, the presence of a "unique" property does not strictly prove the value conforms to that variant's schema. However, in practice, API responses include only documented properties, making this heuristic reliable for well-defined specs. The generator emits a warning when this strategy is used, recommending discriminator addition for robust variant detection.

```yaml
# Cat has: name, meow, whiskers
# Dog has: name, bark, tail_wagging
# Unique to Cat: meow, whiskers
# Unique to Dog: bark, tail_wagging
# We pick one unique field per variant as the indicator
```

```go
func (v *CatOrDogOneOf) UnmarshalJSON(data []byte) error {
    fields, err := extractFieldKeys(data)
    if err != nil {
        return err
    }

    var matched int
    if _, ok := fields["meow"]; ok {  // meow is unique to Cat
        v.Cat = new(Cat)
        if err := json.Unmarshal(data, v.Cat); err != nil {
            v.Cat = nil // unmarshal failed — not a valid Cat
        } else {
            matched++
        }
    }
    if _, ok := fields["bark"]; ok {  // bark is unique to Dog
        v.Dog = new(Dog)
        if err := json.Unmarshal(data, v.Dog); err != nil {
            v.Dog = nil // unmarshal failed — not a valid Dog
        } else {
            matched++
        }
    }

    return v.validateMatchCount(matched, data)
}
```

### Strategy 4: JSON Value Type Discrimination

When the oneOf/anyOf mixes different JSON types (object, array, string, number, boolean, null), we discriminate by inspecting the first byte of the JSON value.

```yaml
oneOf:
  - type: string
  - type: integer
  - $ref: '#/components/schemas/Cat'  # object
```

```go
func (v *MixedOneOf) UnmarshalJSON(data []byte) error {
    trimmed := bytes.TrimLeft(data, " \t\r\n")
    if len(trimmed) == 0 {
        return &OneOfNoMatchError{Data: data}
    }

    switch trimmed[0] {
    case '"':
        v.StringValue = new(string)
        return json.Unmarshal(data, v.StringValue)
    case '{':
        v.Cat = new(Cat)
        return json.Unmarshal(data, v.Cat)
    case '[':
        // array — route to the array variant if one exists
        // (extend this switch when the oneOf includes an array schema)
        return &OneOfNoMatchError{Data: data, Candidates: []string{"string", "Cat", "bool", "int"}}
    case 't', 'f':
        v.BoolValue = new(bool)
        return json.Unmarshal(data, v.BoolValue)
    case 'n':
        // null — if one of the oneOf variants is {type: "null"},
        // set that variant. Otherwise, no variant matches null.
        // For anyOf/oneOf with a nullable variant (ADR-004 normalization),
        // null is handled at the Nullable[T] level before reaching this code.
        return &OneOfNoMatchError{Data: data, Candidates: []string{"string", "Cat", "bool", "int"}}
    default:
        // number (digits or -)
        v.IntValue = new(int)
        return json.Unmarshal(data, v.IntValue)
    }
}
```

### Strategy 5: Full Unmarshal Fallback

When none of the above strategies can distinguish variants (e.g., all variants are objects with identical required fields and overlapping properties), we fall back to trying all variants.

```go
func (v *AmbiguousOneOf) UnmarshalJSON(data []byte) error {
    // Try each variant; keep those that succeed
    var matched int

    v.A = new(SchemaA)
    if err := json.Unmarshal(data, v.A); err != nil {
        v.A = nil
    } else {
        matched++
    }

    v.B = new(SchemaB)
    if err := json.Unmarshal(data, v.B); err != nil {
        v.B = nil
    } else {
        matched++
    }

    return v.validateMatchCount(matched, data)
}
```

**Warning**: This strategy is unreliable because Go's `json.Unmarshal` rarely fails on valid JSON objects. The generator emits a compile-time warning comment when this fallback is used:

```go
// WARNING: No discriminator, no required-field difference, and no unique properties
// detected for this oneOf. Variant detection may be unreliable.
// Consider adding a discriminator to the OpenAPI spec.
```

### Discriminator with anyOf

For anyOf with a discriminator, we use the discriminator for primary routing but then also check remaining variants (since anyOf permits multiple matches):

```go
func (v *PetAnyOf) UnmarshalJSON(data []byte) error {
    disc, err := extractDiscriminator(data, "petType")
    if err != nil {
        return err
    }

    // Primary: discriminator-based
    switch disc {
    case "cat":
        v.Cat = new(Cat)
        if err := json.Unmarshal(data, v.Cat); err != nil {
            v.Cat = nil // discriminator matched but unmarshal failed — data is malformed
        }
    case "dog":
        v.Dog = new(Dog)
        if err := json.Unmarshal(data, v.Dog); err != nil {
            v.Dog = nil
        }
    }

    // Secondary: check remaining variants via required fields / unique props
    // (anyOf allows multiple simultaneous matches).
    // NOTE: If the discriminator value was unknown (no switch case matched),
    // the secondary checks still run. **Spec deviation**: OAS 3.1 states that
    // discriminator values not in the mapping SHOULD cause validation failure.
    // However, for anyOf, we deliberately allow structural matching as a
    // fallback because anyOf permits multiple simultaneous matches and the
    // discriminator is a routing optimization, not the sole matching mechanism.
    // For oneOf, unknown discriminator values produce UnknownDiscriminatorError
    // (no secondary fallback). An unknown discriminator value in anyOf that
    // matches no variant (primary or secondary) results in AnyOfNoMatchError.
    if v.Cat == nil && matchesCat(data) {
        v.Cat = new(Cat)
        if err := json.Unmarshal(data, v.Cat); err != nil {
            v.Cat = nil
        }
    }
    if v.Dog == nil && matchesDog(data) {
        v.Dog = new(Dog)
        if err := json.Unmarshal(data, v.Dog); err != nil {
            v.Dog = nil
        }
    }

    if v.Cat == nil && v.Dog == nil {
        return &AnyOfNoMatchError{Data: data}
    }
    return nil
}
```

### allOf Inheritance Pattern

When a base schema has a discriminator and variants extend it via allOf, the generator:

1. Identifies the inheritance relationship (variant uses `allOf` with the base schema)
2. Flattens the base schema's fields into each variant struct
3. Determines the variant set: if `discriminator.mapping` is present, only the mapped schemas are included. Otherwise, discovers all schemas that extend the base via allOf (by scanning the spec's schema graph) and emits a warning recommending explicit mapping
4. Generates a **separate** discriminated union type (`PetOneOf`) alongside the base type (`Pet`). **Reference resolution**: `$ref` to a schema with a discriminator resolves to the **union type** (`PetOneOf`) everywhere — in response bodies, request bodies, model properties, array items, etc. The sole exception is within `allOf` (for inheritance), where the reference resolves to the **base type** (`Pet`) to flatten fields. This ensures discriminator dispatch is preserved wherever the polymorphic schema is used

```yaml
Pet:
  type: object
  required: [petType]
  properties:
    petType: { type: string }
  discriminator:
    propertyName: petType
Cat:
  allOf:
    - $ref: '#/components/schemas/Pet'
    - type: object
      properties:
        meow: { type: boolean }
```

```go
// Variant types include base fields
type Cat struct {
    PetType string `json:"petType"`
    Meow    bool   `json:"meow,omitzero"`
}

type Dog struct {
    PetType string `json:"petType"`
    Bark    bool   `json:"bark,omitzero"`
}

// Pet is the base struct with shared fields. It is used within allOf
// for field inheritance. Both Pet and PetOneOf are always generated.
type Pet struct {
    PetType string `json:"petType"`
}

// PetOneOf is the discriminated union type.
// $ref resolution: references to a discriminator-bearing schema resolve to
// PetOneOf everywhere (response bodies, request bodies, model properties,
// array items, etc.) EXCEPT within allOf inheritance (where Pet is used
// for field flattening). See "allOf Inheritance Pattern" above.
// The Pet base type is also exported for direct use when users need access
// to shared fields without discriminator dispatch.
type PetOneOf struct {
    Cat *Cat
    Dog *Dog
}
```

### Nested Discriminators

Each discriminated union handles its own discriminator independently. Nesting composes naturally because `json.Unmarshal` on the child type triggers the child's own `UnmarshalJSON`:

```go
// Level 1: "category" discriminator
func (v *AnimalOneOf) UnmarshalJSON(data []byte) error {
    disc, err := extractDiscriminator(data, "category")
    if err != nil {
        return err
    }
    switch disc {
    case "pet":
        v.Pet = new(PetOneOf)
        return json.Unmarshal(data, v.Pet) // triggers PetOneOf.UnmarshalJSON
    case "wild":
        v.WildAnimal = new(WildAnimalOneOf)
        return json.Unmarshal(data, v.WildAnimal)
    }
    return &UnknownDiscriminatorError{Property: "category", Value: disc}
}

// Level 2: "petType" discriminator (triggered by json.Unmarshal above)
func (v *PetOneOf) UnmarshalJSON(data []byte) error {
    disc, err := extractDiscriminator(data, "petType")
    if err != nil {
        return err
    }
    switch disc {
    case "cat":
        v.Cat = new(Cat)
        return json.Unmarshal(data, v.Cat)
    case "dog":
        v.Dog = new(Dog)
        return json.Unmarshal(data, v.Dog)
    }
    return &UnknownDiscriminatorError{Property: "petType", Value: disc}
}
```

**Base schema as abstract type**: In the allOf inheritance + discriminator pattern, the base schema (`Pet`) acts as an **abstract type** — valid API responses always match one of the known variant types (`Cat`, `Dog`), identified by the discriminator value. An unknown discriminator value produces `UnknownDiscriminatorError`. This is the standard behavior across all OpenAPI generators that support discriminators (oapi-codegen, ogen, etc.) and matches the OpenAPI spec's intent: the discriminator mechanism explicitly declares that the property value determines the concrete type. If the API can return values that don't match any known variant, the spec should either (a) add those types to the discriminator mapping, or (b) not use a discriminator.

### Interface Generation for oneOf with Discriminator

When a oneOf has an explicit discriminator, we additionally generate a sealed Go interface that enables idiomatic type switches. This is only for oneOf (not anyOf, since anyOf permits multiple simultaneous matches which interfaces cannot express).

```go
// Sealed interface (unexported marker method prevents external implementation)
// NOTE: The method is named DiscriminatorValue(), not PetType(), to avoid
// colliding with the PetType struct field defined on each variant.
type PetVariant interface {
    petVariant()
    DiscriminatorValue() string
}

// Each variant implements the interface
func (*Cat) petVariant()                    {}
func (c *Cat) DiscriminatorValue() string   { return "cat" }

func (*Dog) petVariant()                    {}
func (d *Dog) DiscriminatorValue() string   { return "dog" }

// The wrapper struct exposes the interface
func (v PetOneOf) Value() PetVariant {
    if v.Cat != nil {
        return v.Cat
    }
    if v.Dog != nil {
        return v.Dog
    }
    return nil
}
```

This allows users to write idiomatic Go:

```go
switch pet := v.Value().(type) {
case *Cat:
    fmt.Println(pet.Meow)
case *Dog:
    fmt.Println(pet.Bark)
}
```

### Runtime Helper: extractDiscriminator

The runtime library provides an efficient discriminator extraction function:

```go
// extractDiscriminator reads a single string field from a JSON object.
// It unmarshals the full object into a map (not streaming); see Negative section.
func extractDiscriminator(data []byte, field string) (string, error) {
    var fields map[string]json.RawMessage
    if err := json.Unmarshal(data, &fields); err != nil {
        return "", fmt.Errorf("parsing JSON object: %w", err)
    }
    raw, ok := fields[field]
    if !ok {
        return "", &MissingDiscriminatorError{Property: field}
    }
    var value string
    if err := json.Unmarshal(raw, &value); err != nil {
        return "", &InvalidDiscriminatorError{
            Property: field,
            RawValue: raw,
            Err:      err,
        }
    }
    return value, nil
}
```

## Consequences

### Positive

- **Discriminator-based dispatch is O(1)**: no trial-and-error unmarshaling
- **Strategy selection is automatic**: users don't configure detection — the generator chooses the best available strategy
- **Nested discriminators compose naturally**: each level is independent
- **Interface generation enables idiomatic Go type switches**: users familiar with Go patterns feel at home
- **All six real-world discriminator patterns are supported**

### Negative

- **Strategy 5 (fallback) is unreliable**: schemas without distinguishing features produce ambiguous code. We mitigate with compile-time warnings.
- **Interface is only generated for oneOf with discriminator**: not available for anyOf or discriminator-less oneOf. Users must use struct field access in those cases.
- **extractDiscriminator parses the full JSON object into a map**: this is not maximally efficient but is simple and correct. A future optimization could use streaming JSON parsing to read only the needed field.

### Risks

- Some APIs place the discriminator field deep in the object or use non-string discriminators (e.g., integer enum). We currently only support top-level string discriminator fields. When the generator detects non-string discriminators or discriminator fields not at the top level, it selects a lower-priority strategy **at code generation time** (e.g., required-field detection or try-all fallback instead of discriminator routing). At runtime, once a discriminator-based strategy is chosen, `extractDiscriminator` errors (missing field, invalid type) are always returned immediately — there is no runtime fallback to other strategies. Note: **nested discriminated unions** (oneOf within oneOf, each with its own top-level discriminator) are fully supported (see "Nested Discriminators" section above) — each level handles its own discriminator independently.
- **Discriminator routing does not guarantee schema validity**: after `json.Unmarshal` succeeds for the discriminator-selected variant, the data may still not conform to the variant's full schema (because `encoding/json` v1 ignores unknown fields and zero-initializes missing required fields). This is consistent with the project's opt-in validation principle (ADR-001, ADR-013): `UnmarshalJSON` handles deserialization; `Validate()` handles schema compliance. **To enforce strict schema conformance**, users should either (a) call `Validate()` after unmarshal, or (b) use `--validate-on-unmarshal` (ADR-013), which automatically calls `Validate()` at the end of `UnmarshalJSON`. The generator emits a comment on discriminator-based `UnmarshalJSON` noting this: `// NOTE: Unmarshal populates known fields; call Validate() for full schema conformance.` This is the same behavior as non-discriminated types — no Go JSON library validates schema constraints during unmarshal by default.
