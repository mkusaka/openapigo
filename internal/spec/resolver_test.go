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

func TestResolve_IDRef(t *testing.T) {
	// $ref using an absolute URI that matches a $id.
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Address": {
					ID:   "https://example.com/schemas/address",
					Type: "object",
					Properties: map[string]*Schema{
						"street": {Type: "string"},
					},
				},
				"Person": {
					Type: "object",
					Properties: map[string]*Schema{
						"home": {Ref: "https://example.com/schemas/address"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	person := doc.Components.Schemas["Person"]
	home := person.Properties["home"].Resolved()
	if home.Type != "object" {
		t.Errorf("resolved home type = %q, want object", home.Type)
	}
	if _, ok := home.Properties["street"]; !ok {
		t.Error("resolved home missing 'street' property")
	}
}

func TestResolve_IDWithAnchorFragment(t *testing.T) {
	// $ref using $id URI + $anchor fragment: "https://example.com/geo#coord"
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Geo": {
					ID:   "https://example.com/geo",
					Type: "object",
					Properties: map[string]*Schema{
						"coord": {
							Anchor: "coord",
							Type:   "object",
							Properties: map[string]*Schema{
								"lat": {Type: "number"},
								"lng": {Type: "number"},
							},
						},
					},
				},
				"Location": {
					Type: "object",
					Properties: map[string]*Schema{
						"point": {Ref: "https://example.com/geo#coord"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	loc := doc.Components.Schemas["Location"]
	point := loc.Properties["point"].Resolved()
	if point.Type != "object" {
		t.Errorf("resolved point type = %q, want object", point.Type)
	}
	if _, ok := point.Properties["lat"]; !ok {
		t.Error("resolved point missing 'lat' property")
	}
}

func TestResolve_IDNestedInProperties(t *testing.T) {
	// $id defined inside a nested property.
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Root": {
					Type: "object",
					Properties: map[string]*Schema{
						"nested": {
							ID:   "https://example.com/nested",
							Type: "object",
							Properties: map[string]*Schema{
								"value": {Type: "integer"},
							},
						},
					},
				},
				"Other": {
					Type: "object",
					Properties: map[string]*Schema{
						"data": {Ref: "https://example.com/nested"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	other := doc.Components.Schemas["Other"]
	data := other.Properties["data"].Resolved()
	if data.Type != "object" {
		t.Errorf("resolved data type = %q, want object", data.Type)
	}
	if _, ok := data.Properties["value"]; !ok {
		t.Error("resolved data missing 'value' property")
	}
}

func TestResolve_IDDuplicate(t *testing.T) {
	// Two schemas with the same $id should produce an error.
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"A": {
					ID:   "https://example.com/dup",
					Type: "object",
				},
				"B": {
					ID:   "https://example.com/dup",
					Type: "object",
				},
			},
		},
	}

	err := Resolve(doc)
	if err == nil {
		t.Fatal("expected error for duplicate $id")
	}
}

func TestResolve_IDNotOAS31(t *testing.T) {
	// OAS 3.0: $id-based refs should not be resolved (falls through to external).
	doc := &Document{
		OpenAPI: "3.0.3",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Address": {
					ID:   "https://example.com/addr",
					Type: "object",
				},
				"Person": {
					Type: "object",
					Properties: map[string]*Schema{
						"home": {Ref: "https://example.com/addr"},
					},
				},
			},
		},
	}

	// Should fail because OAS 3.0 doesn't support $id lookup and external resolution is disabled.
	err := Resolve(doc)
	if err == nil {
		t.Fatal("expected error for $id ref in OAS 3.0")
	}
}

func TestResolve_IDWithJSONPointerFragment(t *testing.T) {
	// $ref using $id URI + JSON Pointer fragment: "https://example.com/geo#/properties/coord"
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Geo": {
					ID:   "https://example.com/geo",
					Type: "object",
					Properties: map[string]*Schema{
						"coord": {
							Type: "object",
							Properties: map[string]*Schema{
								"lat": {Type: "number"},
								"lng": {Type: "number"},
							},
						},
					},
				},
				"Waypoint": {
					Type: "object",
					Properties: map[string]*Schema{
						"position": {Ref: "https://example.com/geo#/properties/coord"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	wp := doc.Components.Schemas["Waypoint"]
	pos := wp.Properties["position"].Resolved()
	if pos.Type != "object" {
		t.Errorf("resolved position type = %q, want object", pos.Type)
	}
	if _, ok := pos.Properties["lat"]; !ok {
		t.Error("resolved position missing 'lat' property")
	}
}

func TestResolve_IDAndAnchorCombination(t *testing.T) {
	// Schema with both $id and $anchor: both should be indexed.
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Contact": {
					ID:     "https://example.com/contact",
					Anchor: "contact-info",
					Type:   "object",
					Properties: map[string]*Schema{
						"email": {Type: "string"},
					},
				},
				"ByID": {
					Type: "object",
					Properties: map[string]*Schema{
						"c": {Ref: "https://example.com/contact"},
					},
				},
				"ByAnchor": {
					Type: "object",
					Properties: map[string]*Schema{
						"c": {Ref: "#contact-info"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Both should resolve to the same schema.
	byID := doc.Components.Schemas["ByID"].Properties["c"].Resolved()
	byAnchor := doc.Components.Schemas["ByAnchor"].Properties["c"].Resolved()

	if byID.Type != "object" {
		t.Errorf("byID type = %q, want object", byID.Type)
	}
	if byAnchor.Type != "object" {
		t.Errorf("byAnchor type = %q, want object", byAnchor.Type)
	}
	if _, ok := byID.Properties["email"]; !ok {
		t.Error("byID missing 'email'")
	}
	if _, ok := byAnchor.Properties["email"]; !ok {
		t.Error("byAnchor missing 'email'")
	}
}
