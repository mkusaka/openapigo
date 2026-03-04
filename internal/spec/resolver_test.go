package spec

import (
	"testing"
)

func TestResolve_AnchorRef(t *testing.T) {
	// OAS 3.1 doc with $anchor and a $ref that uses it.
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Address": {
					Type:   "object",
					Anchor: "address",
					Properties: map[string]*Schema{
						"street": {Type: "string"},
						"city":   {Type: "string"},
					},
				},
				"Person": {
					Type: "object",
					Properties: map[string]*Schema{
						"name":    {Type: "string"},
						"address": {Ref: "#address"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Verify that Person.address resolved to the Address schema.
	person := doc.Components.Schemas["Person"]
	addrProp := person.Properties["address"]
	resolved := addrProp.Resolved()
	if resolved.Type != "object" {
		t.Errorf("resolved address type = %q, want object", resolved.Type)
	}
	if _, ok := resolved.Properties["street"]; !ok {
		t.Error("resolved address missing 'street' property")
	}
}

func TestResolve_AnchorNotOAS31(t *testing.T) {
	// OAS 3.0 doc: $anchor refs should fail since anchors aren't indexed.
	doc := &Document{
		OpenAPI: "3.0.3",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Address": {
					Type:   "object",
					Anchor: "address",
				},
				"Person": {
					Type: "object",
					Properties: map[string]*Schema{
						"address": {Ref: "#address"},
					},
				},
			},
		},
	}

	err := Resolve(doc)
	if err == nil {
		t.Fatal("expected error for anchor ref in OAS 3.0")
	}
}

func TestResolve_AnchorNestedInProperties(t *testing.T) {
	// $anchor defined inside a nested property, referenced from elsewhere.
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Outer": {
					Type: "object",
					Properties: map[string]*Schema{
						"inner": {
							Type:   "object",
							Anchor: "inner-def",
							Properties: map[string]*Schema{
								"value": {Type: "integer"},
							},
						},
					},
				},
				"User": {
					Type: "object",
					Properties: map[string]*Schema{
						"data": {Ref: "#inner-def"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	user := doc.Components.Schemas["User"]
	data := user.Properties["data"].Resolved()
	if data.Type != "object" {
		t.Errorf("resolved data type = %q, want object", data.Type)
	}
	if _, ok := data.Properties["value"]; !ok {
		t.Error("resolved data missing 'value' property")
	}
}

func TestResolve_AnchorMissing(t *testing.T) {
	// Reference to non-existent anchor.
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Person": {
					Type: "object",
					Properties: map[string]*Schema{
						"data": {Ref: "#nonexistent"},
					},
				},
			},
		},
	}

	err := Resolve(doc)
	if err == nil {
		t.Fatal("expected error for missing anchor ref")
	}
}

func TestResolve_AnchorDuplicate(t *testing.T) {
	// Two schemas with the same $anchor should produce an error.
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"A": {
					Type:   "object",
					Anchor: "dup",
				},
				"B": {
					Type:   "object",
					Anchor: "dup",
				},
			},
		},
	}

	err := Resolve(doc)
	if err == nil {
		t.Fatal("expected error for duplicate $anchor")
	}
}

func TestResolve_AnchorInDependentSchemas(t *testing.T) {
	// $anchor defined inside dependentSchemas should be indexed.
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Config": {
					Type: "object",
					DependentSchemas: map[string]*Schema{
						"debug": {
							Type:   "object",
							Anchor: "debug-opts",
							Properties: map[string]*Schema{
								"level": {Type: "integer"},
							},
						},
					},
				},
				"Settings": {
					Type: "object",
					Properties: map[string]*Schema{
						"opts": {Ref: "#debug-opts"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	settings := doc.Components.Schemas["Settings"]
	opts := settings.Properties["opts"].Resolved()
	if opts.Type != "object" {
		t.Errorf("resolved opts type = %q, want object", opts.Type)
	}
	if _, ok := opts.Properties["level"]; !ok {
		t.Error("resolved opts missing 'level' property")
	}
}

func TestResolve_AnchorInPatternProperties(t *testing.T) {
	// $anchor defined inside patternProperties should be indexed.
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Map": {
					Type: "object",
					PatternProperties: map[string]*Schema{
						"^x-": {
							Type:   "object",
							Anchor: "ext-val",
							Properties: map[string]*Schema{
								"key": {Type: "string"},
							},
						},
					},
				},
				"Consumer": {
					Type: "object",
					Properties: map[string]*Schema{
						"ext": {Ref: "#ext-val"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	consumer := doc.Components.Schemas["Consumer"]
	ext := consumer.Properties["ext"].Resolved()
	if ext.Type != "object" {
		t.Errorf("resolved ext type = %q, want object", ext.Type)
	}
}

func TestResolve_AnchorInUnevaluatedProperties(t *testing.T) {
	// $anchor defined inside unevaluatedProperties schema should be indexed.
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Base": {
					Type: "object",
					UnevaluatedProperties: &AdditionalProperties{
						Schema: &Schema{
							Type:   "object",
							Anchor: "uneval-schema",
							Properties: map[string]*Schema{
								"extra": {Type: "string"},
							},
						},
					},
				},
				"Ref": {
					Type: "object",
					Properties: map[string]*Schema{
						"data": {Ref: "#uneval-schema"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	ref := doc.Components.Schemas["Ref"]
	data := ref.Properties["data"].Resolved()
	if data.Type != "object" {
		t.Errorf("resolved data type = %q, want object", data.Type)
	}
	if _, ok := data.Properties["extra"]; !ok {
		t.Error("resolved data missing 'extra' property")
	}
}
