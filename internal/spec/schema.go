package spec

import "encoding/json"

// Schema represents an OpenAPI Schema Object.
// It supports both OAS 3.0 and OAS 3.1 keywords.
type Schema struct {
	// $ref
	Ref string `json:"$ref,omitempty" yaml:"$ref,omitempty"`

	// Type information
	Type   string `json:"type,omitempty" yaml:"type,omitempty"`
	Format string `json:"format,omitempty" yaml:"format,omitempty"`

	// Object properties
	Properties           map[string]*Schema `json:"properties,omitempty" yaml:"properties,omitempty"`
	Required             []string           `json:"required,omitempty" yaml:"required,omitempty"`
	AdditionalProperties *AdditionalProperties `json:"additionalProperties,omitempty" yaml:"additionalProperties,omitempty"`

	// Array items
	Items       *Schema   `json:"items,omitempty" yaml:"items,omitempty"`
	PrefixItems []*Schema `json:"prefixItems,omitempty" yaml:"prefixItems,omitempty"`
	Contains    *Schema   `json:"contains,omitempty" yaml:"contains,omitempty"`
	MinItems    *int      `json:"minItems,omitempty" yaml:"minItems,omitempty"`
	MaxItems    *int      `json:"maxItems,omitempty" yaml:"maxItems,omitempty"`

	// Composition
	AllOf []*Schema `json:"allOf,omitempty" yaml:"allOf,omitempty"`
	OneOf []*Schema `json:"oneOf,omitempty" yaml:"oneOf,omitempty"`
	AnyOf []*Schema `json:"anyOf,omitempty" yaml:"anyOf,omitempty"`

	// Enum/Const/Default
	Enum    []json.RawMessage `json:"enum,omitempty" yaml:"enum,omitempty"`
	Const   json.RawMessage   `json:"const,omitempty" yaml:"const,omitempty"`
	Default json.RawMessage   `json:"default,omitempty" yaml:"default,omitempty"`

	// Numeric constraints
	Minimum          *float64 `json:"minimum,omitempty" yaml:"minimum,omitempty"`
	Maximum          *float64 `json:"maximum,omitempty" yaml:"maximum,omitempty"`
	ExclusiveMinimum *float64 `json:"exclusiveMinimum,omitempty" yaml:"exclusiveMinimum,omitempty"`
	ExclusiveMaximum *float64 `json:"exclusiveMaximum,omitempty" yaml:"exclusiveMaximum,omitempty"`
	MultipleOf       *float64 `json:"multipleOf,omitempty" yaml:"multipleOf,omitempty"`

	// String constraints
	MinLength *int   `json:"minLength,omitempty" yaml:"minLength,omitempty"`
	MaxLength *int   `json:"maxLength,omitempty" yaml:"maxLength,omitempty"`
	Pattern   string `json:"pattern,omitempty" yaml:"pattern,omitempty"`

	// Conditional (OAS 3.1 / JSON Schema 2020-12)
	If   *Schema `json:"if,omitempty" yaml:"if,omitempty"`
	Then *Schema `json:"then,omitempty" yaml:"then,omitempty"`
	Else *Schema `json:"else,omitempty" yaml:"else,omitempty"`

	// Dependent keywords (OAS 3.1 / JSON Schema 2020-12)
	DependentRequired map[string][]string  `json:"dependentRequired,omitempty" yaml:"dependentRequired,omitempty"`
	DependentSchemas  map[string]*Schema   `json:"dependentSchemas,omitempty" yaml:"dependentSchemas,omitempty"`

	// Pattern properties (OAS 3.1 / JSON Schema 2020-12)
	PatternProperties map[string]*Schema `json:"patternProperties,omitempty" yaml:"patternProperties,omitempty"`

	// Unevaluated keywords (OAS 3.1 / JSON Schema 2020-12)
	UnevaluatedProperties *AdditionalProperties `json:"unevaluatedProperties,omitempty" yaml:"unevaluatedProperties,omitempty"`
	UnevaluatedItems      *AdditionalProperties `json:"unevaluatedItems,omitempty" yaml:"unevaluatedItems,omitempty"`

	// JSON Schema identity (OAS 3.1)
	ID     string `json:"$id,omitempty" yaml:"$id,omitempty"`
	Anchor string `json:"$anchor,omitempty" yaml:"$anchor,omitempty"`

	// OAS 3.0 nullable
	Nullable bool `json:"nullable,omitempty" yaml:"nullable,omitempty"`

	// Metadata
	Title       string `json:"title,omitempty" yaml:"title,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Deprecated  bool   `json:"deprecated,omitempty" yaml:"deprecated,omitempty"`
	Example     json.RawMessage `json:"example,omitempty" yaml:"example,omitempty"`

	// ReadOnly/WriteOnly
	ReadOnly  bool `json:"readOnly,omitempty" yaml:"readOnly,omitempty"`
	WriteOnly bool `json:"writeOnly,omitempty" yaml:"writeOnly,omitempty"`

	// Vendor extensions
	Extensions map[string]json.RawMessage `json:"-" yaml:"-"`

	// PropertyOrder preserves the iteration order of properties.
	PropertyOrder []string `json:"-" yaml:"-"`

	// resolvedRef points to the resolved schema if this was a $ref.
	// Set by the resolver, nil if not a $ref or not yet resolved.
	resolvedRef *Schema
}

// Resolved returns the resolved schema. If this schema is a $ref that has
// been resolved, it returns the target. Otherwise returns itself.
func (s *Schema) Resolved() *Schema {
	if s == nil {
		return nil
	}
	if s.resolvedRef != nil {
		return s.resolvedRef
	}
	return s
}

// IsRef reports whether this schema is a $ref.
func (s *Schema) IsRef() bool {
	return s != nil && s.Ref != ""
}

// AdditionalProperties represents the additionalProperties keyword,
// which can be either a boolean or a schema.
type AdditionalProperties struct {
	Bool   *bool
	Schema *Schema
}

// IsFalse reports whether additionalProperties is explicitly false.
func (ap *AdditionalProperties) IsFalse() bool {
	return ap != nil && ap.Bool != nil && !*ap.Bool
}

// IsTrue reports whether additionalProperties is explicitly true.
func (ap *AdditionalProperties) IsTrue() bool {
	return ap != nil && ap.Bool != nil && *ap.Bool
}

// UnmarshalJSON handles both boolean and schema forms.
func (ap *AdditionalProperties) UnmarshalJSON(data []byte) error {
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		ap.Bool = &b
		return nil
	}
	ap.Schema = &Schema{}
	return json.Unmarshal(data, ap.Schema)
}

// UnmarshalYAML handles both boolean and schema forms for YAML.
func (ap *AdditionalProperties) UnmarshalYAML(unmarshal func(any) error) error {
	var b bool
	if err := unmarshal(&b); err == nil {
		ap.Bool = &b
		return nil
	}
	ap.Schema = &Schema{}
	return unmarshal(ap.Schema)
}

// MarshalJSON implements json.Marshaler.
func (ap AdditionalProperties) MarshalJSON() ([]byte, error) {
	if ap.Bool != nil {
		return json.Marshal(*ap.Bool)
	}
	return json.Marshal(ap.Schema)
}
