# ADR-010: Conditional Schemas (if/then/else, dependentRequired, dependentSchemas)

## Status

Accepted

## Date

2026-03-01

## Context

JSON Schema 2020-12 (used by OAS 3.1) introduces conditional schema application:

- **`if`/`then`/`else`**: Apply different sub-schemas based on whether the value validates against the `if` schema
- **`dependentRequired`**: If property A is present, properties B and C must also be present
- **`dependentSchemas`**: If property A is present, the value must also validate against an additional schema

These keywords express constraints that are **conditional on runtime values**. Go's type system is static — there is no way to express "field X is required only when field Y has value Z" at the type level.

### Real-world usage

**if/then/else** is used in practice for:

1. Conditional required fields based on a type/kind discriminator
2. Conditional property types based on a mode selector
3. Conditional constraints (e.g., different min/max based on a field value)

**dependentRequired** is used for:

1. Related fields that must appear together (e.g., credit_card requires billing_address)
2. Conditional completeness requirements

**dependentSchemas** is used for:

1. Adding properties when a trigger property is present
2. Changing constraints when a trigger property is present

### Key observation

When the `if` condition checks a **single property against a `const` or `enum` value**, the if/then/else is semantically equivalent to a **discriminated union** (oneOf with discriminator). This is by far the most common usage pattern.

## Decision

### if/then/else on Enum/Const → Normalize to oneOf + Discriminator

When the `if` schema tests a single property against a `const` value (or an enum with one value), we normalize the entire if/then/else construct into a `oneOf` with a discriminator and apply ADR-002 and ADR-003.

**Detection criteria**: The `if` schema must:

1. Reference a single property **with `required: [prop]`** — to ensure the property is actually present (without `required`, a missing property means `if` evaluates to `true` for any instance without that property, which would incorrectly route those instances to `then`)
2. That property has `const` or a single-value `enum`
3. No other constraints in the `if` schema beyond `required` and `properties` (in particular, `required` alone without `const`/`enum` is NOT sufficient for normalization — it tests field presence, not a specific value)
4. **The discriminator property must be required in the base schema** (the enclosing schema's own `required` array). If the property is not required at the base level, instances without the property are valid, and the original `if` would evaluate to false (routing to `else`). But the normalized oneOf + discriminator would produce a `MissingDiscriminatorError`, which is a behavioral change. The generator checks this precondition and falls back to "Complex if/then/else → Validate() Only" when the property is not base-required.

**Precondition: closed string enum**: Normalization to oneOf requires that the discriminator property has a **closed value set** (an `enum` constraint) AND is `type: string`. Non-string enums (integer, boolean) are not supported for normalization because the discriminator mechanism (ADR-003) operates on string values extracted from raw JSON via `extractDiscriminator`. Non-string discriminators would require type-aware extraction and comparison, which adds complexity without clear real-world demand. When the discriminator property is non-string or has no `enum` constraint, the generator falls back to "Complex if/then/else → Validate() Only". If the discriminator is a free-form `string` without `enum`, the value space is unbounded and the fallback variant would need to accept "all other strings" — but the normalized oneOf + discriminator uses exact value matching, so unknown values would produce `UnknownDiscriminatorError` instead of matching the original `else` branch. This changes semantics (the original `else` succeeds for any non-matching value, including unknown strings, `null`, and missing property). Therefore, when the discriminator property has no `enum` constraint, the generator also falls back to "Complex if/then/else → Validate() Only".

**Exhaustiveness and exclusivity check**: When normalizing to oneOf (with a closed enum), the generator verifies two properties: (1) **Coverage**: all enum values are covered by some branch, and (2) **Exclusivity**: each enum value appears in **exactly one** branch (no duplicate assignments). If an enum value appears in multiple `if` branches, the generator emits a fatal error: `ERROR: discriminator value "X" appears in multiple if/then/else branches. Each value must map to exactly one branch for oneOf normalization.` This ensures the one-to-one mapping required by oneOf semantics. If some values have no branch, the generator adds a **fallback variant** that inherits the `else` branch constraints (if `else` is present) or contains only the shared (non-conditional) fields (if no `else`). The `else` branch is never silently dropped — it represents the schema that applies when no `if` condition matches, and its constraints (required fields, additional properties, etc.) must be preserved in the fallback variant.

**oneOf exclusivity guarantee**: The normalized oneOf's exclusivity is enforced by the discriminator switch, which matches exactly one `const` value per branch. The fallback variant's discriminator constraint is set to the remaining values not covered by any `if` branch — using `const` if exactly one value remains, or `enum` if multiple values remain. Since the discriminator routes by exact value match (not by schema validation), the `not(if)` condition is implicitly enforced: a discriminator value of "personal" always routes to the personal branch, never to the business branch. Two branches cannot match simultaneously because each branch has a distinct `const` value.

**Example normalization**:

```yaml
# Input: if/then/else
type: object
required: [type, name]
properties:
  type:
    type: string
    enum: [personal, business]
  name: { type: string }
  company_name: { type: string }
  tax_id: { type: string }
if:
  required: [type]
  properties:
    type: { const: business }
then:
  required: [company_name, tax_id]
```

The generator normalizes this to:

```yaml
# Normalized: oneOf + discriminator
oneOf:
  - title: AccountPersonal
    type: object
    required: [type, name]
    properties:
      type: { type: string, const: personal }
      name: { type: string }
      company_name: { type: string }
      tax_id: { type: string }
  - title: AccountBusiness
    type: object
    required: [type, name, company_name, tax_id]
    properties:
      type: { type: string, const: business }
      name: { type: string }
      company_name: { type: string }
      tax_id: { type: string }
discriminator:
  propertyName: type
```

Which generates (per ADR-002/003):

```go
type AccountPersonal struct {
    Type        string  `json:"type"`         // const: "personal"
    Name        string  `json:"name"`
    CompanyName *string `json:"company_name,omitzero"` // inherited from base, optional in this variant
    TaxID       *string `json:"tax_id,omitzero"`       // inherited from base, optional in this variant
}

type AccountBusiness struct {
    Type        string `json:"type"` // const: "business"
    Name        string `json:"name"`
    CompanyName string `json:"company_name"`
    TaxID       string `json:"tax_id"`
}

type Account struct {
    Personal *AccountPersonal
    Business *AccountBusiness
}

func (v *Account) UnmarshalJSON(data []byte) error {
    disc, err := extractDiscriminator(data, "type")
    if err != nil {
        return err
    }
    switch disc {
    case "personal":
        v.Personal = new(AccountPersonal)
        return json.Unmarshal(data, v.Personal)
    case "business":
        v.Business = new(AccountBusiness)
        return json.Unmarshal(data, v.Business)
    default:
        return &UnknownDiscriminatorError{Property: "type", Value: disc}
    }
}
```

### Chained if/then/else (Multiple Conditions)

When `else` contains another `if/then/else` (chained conditions), each branch with a const/enum test becomes a variant in the oneOf, **provided** all of the following hold: (1) every `if` node in the chain independently satisfies the detection criteria, (2) **all `if` nodes test the same discriminator property** (e.g., all check `type`), and (3) the discriminator property has a closed enum covering all branches (including the final `else`). If any `if` node tests a different property, the chain cannot be normalized to a single-discriminator oneOf and falls back to "Complex if/then/else → Validate() Only":

```yaml
required: [type]
properties:
  type: { type: string, enum: [a, b, c] }
if:
  required: [type]
  properties: { type: { const: a } }
then:
  required: [field_a]
else:
  if:
    required: [type]
    properties: { type: { const: b } }
  then:
    required: [field_b]
  else:
    required: [field_c]
```

Normalizes to a three-variant oneOf. **Required propagation**: Each variant inherits the base schema's `required` array (the enclosing schema's own `required`) plus the branch-specific `then`/`else` `required`. **Property propagation**: Similarly, each variant inherits all base schema `properties` (not just the ones from `then`/`else`). The base schema's properties are shared across all branches — the normalized variant includes the union of base properties and branch-specific properties. The cumulative required for variant `a` is `[type, field_a]` (base `required: [type]` + then `required: [field_a]`). Nested `else.then` and `else.else` branches similarly accumulate: each branch's required is the union of the base required and all `then`/`else` required arrays on the path from the root to that branch. If any `if` node in the chain does **not** satisfy the detection criteria, the entire chain falls back to "Complex if/then/else → Validate() Only".

### Complex if/then/else → Validate() Only

When the `if` condition is not a simple const/enum check on a single property, we cannot normalize to oneOf. Instead, we generate:

1. A **superset struct** containing all properties from `then` and `else` branches (all as optional)
2. A **`rawFieldKeys` field** (when any `if` condition uses `required` to check property presence, since `*T` conflates absent and null per ADR-004)
3. A **`Validate()` method** that implements the conditional logic

**Type-varying properties**: When the same property name has **different types** across branches (e.g., `value` is `string` in `then` but `integer` in `else`), a single Go struct field cannot represent both types. The generator handles this by:
- Using `json.RawMessage` as the field type for the conflicting property
- Generating typed accessor methods for each branch's type: `ValueAsString() (string, error)`, `ValueAsInt() (int64, error)`
- `Validate()` checks that the raw value matches the branch-appropriate type based on the discriminator/condition
- A generation-time warning is emitted: `WARN: property "value" has different types across if/then/else branches (string, integer). Using json.RawMessage with typed accessors.`

This preserves round-trip fidelity (the raw JSON is kept) while providing type-safe access per branch.

**Important**: When `if` contains `required: [x]`, the condition is "property `x` is present in the JSON" (not "property `x` is non-nil in Go"). Since `*T` conflates absent and null, `Validate()` must use `rawFieldKeys` to evaluate `if.required` conditions correctly. The generator produces `rawFieldKeys` tracking for any struct with complex if/then/else where `if` uses `required`.

```yaml
# Complex condition: if minimum age, then require guardian
if:
  properties:
    age: { maximum: 17 }
then:
  required: [guardian_name]
```

```go
type Registration struct {
    Age          int     `json:"age"`
    GuardianName *string `json:"guardian_name,omitzero"`
    rawFieldKeys []string // populated by UnmarshalJSON for if/required conditions
}

func (v Registration) Validate() error {
    // if age <= 17, then guardian_name is required
    // Note: age is a value type (required), so nil-check is not needed.
    // For if-conditions that use `required: [prop]`, we use rawFieldKeys:
    //   if hasRawKey(v.rawFieldKeys, "prop") { ... }
    if v.Age <= 17 && !hasRawKey(v.rawFieldKeys, "guardian_name") {
        return &ConditionalRequiredError{
            Condition: "age <= 17",
            Missing:   []string{"guardian_name"},
        }
    }
    return nil
}
```

A warning comment is emitted in the generated code:

```go
// WARNING: This schema uses if/then/else with a complex condition that
// cannot be represented in Go's type system. Use Validate() to check
// conditional requirements at runtime.
```

### dependentRequired → Validate() Only

`dependentRequired` is always a validation-only concern. The struct includes all properties (dependent ones as optional), and `Validate()` checks the dependency:

```yaml
type: object
properties:
  credit_card: { type: string }
  billing_address: { type: string }
  cvv: { type: string }
dependentRequired:
  credit_card: [billing_address, cvv]
```

```go
type Payment struct {
    CreditCard     *string `json:"credit_card,omitzero"`
    BillingAddress *string `json:"billing_address,omitzero"`
    CVV            *string `json:"cvv,omitzero"`

    rawFieldKeys []string // populated by UnmarshalJSON when dependentRequired is present
}

func (v Payment) Validate() error {
    // dependentRequired: credit_card → [billing_address, cvv]
    // IMPORTANT: We check raw field key presence (was the key in the JSON?),
    // not pointer nil-ness. A *string field is nil for both absent and null
    // (per ADR-004, nullable fields use Nullable[T], not *T). For dependentRequired,
    // we need to know if the trigger key was present in the JSON at all.
    if hasRawKey(v.rawFieldKeys, "credit_card") {
        var missing []string
        if !hasRawKey(v.rawFieldKeys, "billing_address") {
            missing = append(missing, "billing_address")
        }
        if !hasRawKey(v.rawFieldKeys, "cvv") {
            missing = append(missing, "cvv")
        }
        if len(missing) > 0 {
            return &DependentRequiredError{
                Present: "credit_card",
                Missing: missing,
            }
        }
    }
    return nil
}
```

### dependentSchemas → Validate() + Superset Struct

`dependentSchemas` applies an **entire additional schema** when a trigger property is present. This is more general than `dependentRequired` — the dependent schema can add properties, impose type constraints, define patterns, set additionalProperties restrictions, or any other schema constraint.

We include all possible properties from all dependent schemas in the struct (as optional fields) and validate the full dependent schema constraints in `Validate()`. The generator processes each dependent schema's constraints (required, type, pattern, minLength, minimum, additionalProperties, etc.) and emits corresponding checks gated by the trigger property's presence. For constraints that cannot be expressed as simple field checks (e.g., `additionalProperties: false` on the dependent schema), the validation uses `rawFieldKeys` to evaluate the full constraint:

```yaml
type: object
properties:
  name: { type: string }
  credit_card: { type: string }
dependentSchemas:
  credit_card:
    properties:
      billing_address: { type: string }
    required: [billing_address]
```

```go
type Customer struct {
    Name           string  `json:"name"`
    CreditCard     *string `json:"credit_card,omitzero"`
    BillingAddress *string `json:"billing_address,omitzero"` // from dependentSchemas

    rawFieldKeys []string // populated by UnmarshalJSON when dependentSchemas is present
}

func (v Customer) Validate() error {
    // dependentSchemas: credit_card present → billing_address required
    // IMPORTANT: Like dependentRequired, we check raw field key presence
    // (was the key in the JSON?), not pointer nil-ness. A *string field is
    // nil for both absent and null (per ADR-004). dependentSchemas triggers
    // when the trigger KEY is present, regardless of its value (including null).
    if hasRawKey(v.rawFieldKeys, "credit_card") && !hasRawKey(v.rawFieldKeys, "billing_address") {
        return &DependentSchemaError{
            Trigger: "credit_card",
            Missing: []string{"billing_address"},
        }
    }
    return nil
}
```

### Summary of Normalization Rules

```
if/then/else:
  ├── if condition is const/enum on single property?
  │   ├── Yes → normalize to oneOf + discriminator (ADR-002/003)
  │   └── No  → superset struct + Validate()
  │
dependentRequired:
  └── always → Validate() only

dependentSchemas:
  └── always → superset struct (merge properties) + Validate()
```

## Consequences

### Positive

- **Common case is well-handled**: enum-based if/then/else (the majority of real-world usage) produces proper discriminated union types with full compile-time safety
- **Uncommon cases degrade gracefully**: complex conditions still get runtime validation
- **Consistent with ADR-001**: no implicit runtime validation — `Validate()` is opt-in
- **Reuses existing machinery**: normalization feeds into ADR-002/003's oneOf + discriminator logic

### Negative

- **Normalization adds generator complexity**: detecting the "simple const/enum" pattern and synthesizing oneOf schemas requires careful implementation
- **Superset structs for complex if/then/else lose type safety**: all conditional fields are optional, and the relationships between them are only checked by `Validate()`
- **Information loss**: after normalization, the generated code doesn't indicate that the original schema used if/then/else. The source schema should be the reference for spec-level understanding.

### Risks

- Some if/then/else schemas use `required` in the `if` condition (not just property values). Our "simple const/enum" detection must be precise to avoid incorrect normalization.
- Deeply nested if/then/else chains may produce many oneOf variants. We cap at a reasonable limit (e.g., 20 variants) and fall back to superset struct + Validate() beyond that.
