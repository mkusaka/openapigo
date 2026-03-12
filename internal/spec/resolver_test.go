package spec

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
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

// remoteSpecJSON is a minimal OAS 3.1 spec served by httptest.Server.
const remoteSpecJSON = `{
  "openapi": "3.1.0",
  "info": {"title": "Remote", "version": "1.0.0"},
  "paths": {},
  "components": {
    "schemas": {
      "Widget": {
        "type": "object",
        "properties": {
          "name": {"type": "string"}
        }
      }
    }
  }
}`

func TestResolveWithExternal_RemoteHeader(t *testing.T) {
	var mu sync.Mutex
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(remoteSpecJSON))
	}))
	defer srv.Close()

	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Local": {
					Type: "object",
					Properties: map[string]*Schema{
						"w": {Ref: srv.URL + "/remote.json#/components/schemas/Widget"},
					},
				},
			},
		},
	}

	err := ResolveWithExternal(doc, ResolveConfig{
		BaseDir:   t.TempDir(),
		AllowHTTP: true,
		Headers:   map[string]string{"Authorization": "Bearer test-token"},
	})
	if err != nil {
		t.Fatalf("ResolveWithExternal: %v", err)
	}

	mu.Lock()
	auth := gotAuth
	mu.Unlock()
	if auth != "Bearer test-token" {
		t.Errorf("Authorization header = %q, want %q", auth, "Bearer test-token")
	}

	w := doc.Components.Schemas["Local"].Properties["w"].Resolved()
	if w.Type != "object" {
		t.Errorf("resolved Widget type = %q, want object", w.Type)
	}
}

func TestResolveWithExternal_RemoteTimeout(t *testing.T) {
	// Server that sleeps longer than the timeout.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(remoteSpecJSON))
	}))
	defer srv.Close()

	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Local": {
					Type: "object",
					Properties: map[string]*Schema{
						"w": {Ref: srv.URL + "/remote.json#/components/schemas/Widget"},
					},
				},
			},
		},
	}

	err := ResolveWithExternal(doc, ResolveConfig{
		BaseDir:   t.TempDir(),
		AllowHTTP: true,
		Timeout:   50 * time.Millisecond, // shorter than server sleep
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	// Check for timeout using type assertion (more robust than string matching).
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		// Expected timeout error.
	} else if strings.Contains(err.Error(), "Client.Timeout") || strings.Contains(err.Error(), "context deadline") {
		// Fallback string match.
	} else {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

func TestResolve_IDRelativeChain(t *testing.T) {
	// Parent has absolute $id, child has relative $id.
	// Child's $id should resolve against parent's base URI.
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Root": {
					ID:   "https://example.com/schemas/root",
					Type: "object",
					Properties: map[string]*Schema{
						"child": {
							ID:   "child", // resolves to https://example.com/schemas/child
							Type: "object",
							Properties: map[string]*Schema{
								"value": {Type: "string"},
							},
						},
					},
				},
				"Consumer": {
					Type: "object",
					Properties: map[string]*Schema{
						"data": {Ref: "https://example.com/schemas/child"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	consumer := doc.Components.Schemas["Consumer"]
	data := consumer.Properties["data"].Resolved()
	if data == nil {
		t.Fatal("resolved data is nil")
	}
	if data.Type != "object" {
		t.Errorf("resolved data type = %q, want object", data.Type)
	}
	if _, ok := data.Properties["value"]; !ok {
		t.Error("resolved data missing 'value' property")
	}
}

func TestResolve_IDRelativeChainThreeLevel(t *testing.T) {
	// Three-level chain: absolute → relative → relative.
	// grandparent: https://example.com/a
	// parent:      b → https://example.com/b
	// child:       c → https://example.com/c
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Top": {
					ID:   "https://example.com/a",
					Type: "object",
					Properties: map[string]*Schema{
						"mid": {
							ID:   "b", // resolves to https://example.com/b
							Type: "object",
							Properties: map[string]*Schema{
								"inner": {
									ID:   "c", // resolves to https://example.com/c
									Type: "object",
									Properties: map[string]*Schema{
										"deep": {Type: "integer"},
									},
								},
							},
						},
					},
				},
				"RefMid": {
					Type: "object",
					Properties: map[string]*Schema{
						"x": {Ref: "https://example.com/b"},
					},
				},
				"RefDeep": {
					Type: "object",
					Properties: map[string]*Schema{
						"y": {Ref: "https://example.com/c"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Verify mid-level resolution.
	refMid := doc.Components.Schemas["RefMid"]
	x := refMid.Properties["x"].Resolved()
	if x == nil {
		t.Fatal("resolved x is nil")
	}
	if x.Type != "object" {
		t.Errorf("resolved x type = %q, want object", x.Type)
	}

	// Verify deep-level resolution.
	refDeep := doc.Components.Schemas["RefDeep"]
	y := refDeep.Properties["y"].Resolved()
	if y == nil {
		t.Fatal("resolved y is nil")
	}
	if y.Type != "object" {
		t.Errorf("resolved y type = %q, want object", y.Type)
	}
	if _, ok := y.Properties["deep"]; !ok {
		t.Error("resolved y missing 'deep' property")
	}
}

func TestResolve_IDRelativeSubdirectory(t *testing.T) {
	// Relative $id with path segments: "sub/schema" under "https://example.com/api/"
	// resolves to "https://example.com/api/sub/schema".
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Parent": {
					ID:   "https://example.com/api/",
					Type: "object",
					Properties: map[string]*Schema{
						"nested": {
							ID:   "sub/schema", // resolves to https://example.com/api/sub/schema
							Type: "object",
							Properties: map[string]*Schema{
								"name": {Type: "string"},
							},
						},
					},
				},
				"Ref": {
					Type: "object",
					Properties: map[string]*Schema{
						"s": {Ref: "https://example.com/api/sub/schema"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	ref := doc.Components.Schemas["Ref"]
	s := ref.Properties["s"].Resolved()
	if s == nil {
		t.Fatal("resolved s is nil")
	}
	if _, ok := s.Properties["name"]; !ok {
		t.Error("resolved s missing 'name' property")
	}
}

func TestResolve_IDRelativeDuplicateAfterResolution(t *testing.T) {
	// Relative $id resolves to same absolute URI as another schema → duplicate error.
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Existing": {
					ID:   "https://example.com/target",
					Type: "object",
				},
				"Parent": {
					ID:   "https://example.com/base",
					Type: "object",
					Properties: map[string]*Schema{
						"child": {
							ID:   "target", // resolves to https://example.com/target → duplicate
							Type: "object",
						},
					},
				},
			},
		},
	}

	err := Resolve(doc)
	if err == nil {
		t.Fatal("expected duplicate $id error after relative resolution")
	}
	if !strings.Contains(err.Error(), "duplicate $id") {
		t.Errorf("expected 'duplicate $id' error, got: %v", err)
	}
}

func TestResolve_IDRelativeWithNoParentBase(t *testing.T) {
	// Relative $id with no parent base URI — stored as-is (no resolution possible).
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Orphan": {
					ID:   "relative-only",
					Type: "object",
					Properties: map[string]*Schema{
						"val": {Type: "string"},
					},
				},
				"Ref": {
					Type: "object",
					Properties: map[string]*Schema{
						"o": {Ref: "relative-only"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	ref := doc.Components.Schemas["Ref"]
	o := ref.Properties["o"].Resolved()
	if o == nil {
		t.Fatal("resolved o is nil")
	}
	if o.Type != "object" {
		t.Errorf("resolved o type = %q, want object", o.Type)
	}
}

func TestResolveWithExternal_HTTPBlockedByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(remoteSpecJSON))
	}))
	defer srv.Close()

	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Local": {
					Type: "object",
					Properties: map[string]*Schema{
						"w": {Ref: srv.URL + "/remote.json#/components/schemas/Widget"},
					},
				},
			},
		},
	}

	// AllowHTTP = false (default) should block HTTP URLs.
	err := ResolveWithExternal(doc, ResolveConfig{
		BaseDir:   t.TempDir(),
		AllowHTTP: false,
	})
	if err == nil {
		t.Fatal("expected HTTP blocked error")
	}
	if !strings.Contains(err.Error(), "HTTP not allowed") {
		t.Errorf("expected 'HTTP not allowed' error, got: %v", err)
	}
}

func TestResolve_DeepLocalRef(t *testing.T) {
	doc := &Document{
		OpenAPI: "3.0.3",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Transaction": {
					Type: "object",
					Properties: map[string]*Schema{
						"amount":   {Type: "number", Format: "double"},
						"currency": {Type: "string"},
					},
				},
				"Payment": {
					Type: "object",
					Properties: map[string]*Schema{
						"txAmount": {Ref: "#/components/schemas/Transaction/properties/amount"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	resolved := doc.Components.Schemas["Payment"].Properties["txAmount"].Resolved()
	if resolved.Type != "number" {
		t.Errorf("resolved type = %q, want number", resolved.Type)
	}
	if resolved.Format != "double" {
		t.Errorf("resolved format = %q, want double", resolved.Format)
	}
}

func TestResolve_DeepLocalRef_Items(t *testing.T) {
	doc := &Document{
		OpenAPI: "3.0.3",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Base": {
					Type: "object",
					Properties: map[string]*Schema{
						"tags": {
							Type:  "array",
							Items: &Schema{Type: "string"},
						},
					},
				},
				"Consumer": {
					Type: "object",
					Properties: map[string]*Schema{
						"tag": {Ref: "#/components/schemas/Base/properties/tags/items"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	resolved := doc.Components.Schemas["Consumer"].Properties["tag"].Resolved()
	if resolved.Type != "string" {
		t.Errorf("resolved type = %q, want string", resolved.Type)
	}
}

func TestResolve_DeepLocalRef_AllOf(t *testing.T) {
	doc := &Document{
		OpenAPI: "3.0.3",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Composed": {
					AllOf: []*Schema{
						{Type: "object", Properties: map[string]*Schema{"a": {Type: "integer"}}},
						{Type: "object", Properties: map[string]*Schema{"b": {Type: "string"}}},
					},
				},
				"Ref": {
					Type: "object",
					Properties: map[string]*Schema{
						"x": {Ref: "#/components/schemas/Composed/allOf/0"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	resolved := doc.Components.Schemas["Ref"].Properties["x"].Resolved()
	if resolved.Type != "object" {
		t.Errorf("resolved type = %q, want object", resolved.Type)
	}
	if _, ok := resolved.Properties["a"]; !ok {
		t.Error("resolved schema missing 'a' property")
	}
}

func TestResolve_DeepLocalRef_NotFound(t *testing.T) {
	doc := &Document{
		OpenAPI: "3.0.3",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Base": {
					Type:       "object",
					Properties: map[string]*Schema{"name": {Type: "string"}},
				},
				"Bad": {
					Type: "object",
					Properties: map[string]*Schema{
						"x": {Ref: "#/components/schemas/Base/properties/nonexistent"},
					},
				},
			},
		},
	}

	err := Resolve(doc)
	if err == nil {
		t.Fatal("expected error for nonexistent deep ref")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention 'nonexistent', got: %v", err)
	}
}

func TestResolve_DeepLocalRef_TildeEscape(t *testing.T) {
	// Property name containing "/" → must be escaped as ~1 in JSON Pointer.
	doc := &Document{
		OpenAPI: "3.0.3",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Base": {
					Type: "object",
					Properties: map[string]*Schema{
						"a/b": {Type: "integer"},
						"c~d": {Type: "string"},
					},
				},
				"Ref1": {
					Type: "object",
					Properties: map[string]*Schema{
						"x": {Ref: "#/components/schemas/Base/properties/a~1b"},
					},
				},
				"Ref2": {
					Type: "object",
					Properties: map[string]*Schema{
						"y": {Ref: "#/components/schemas/Base/properties/c~0d"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	r1 := doc.Components.Schemas["Ref1"].Properties["x"].Resolved()
	if r1.Type != "integer" {
		t.Errorf("resolved a/b type = %q, want integer", r1.Type)
	}

	r2 := doc.Components.Schemas["Ref2"].Properties["y"].Resolved()
	if r2.Type != "string" {
		t.Errorf("resolved c~d type = %q, want string", r2.Type)
	}
}

func TestResolve_DeepLocalRef_PercentEncoded(t *testing.T) {
	doc := &Document{
		OpenAPI: "3.0.3",
		Components: &Components{
			Schemas: map[string]*Schema{
				"Base": {
					Type: "object",
					Properties: map[string]*Schema{
						"my field": {Type: "boolean"},
					},
				},
				"Consumer": {
					Type: "object",
					Properties: map[string]*Schema{
						"val": {Ref: "#/components/schemas/Base/properties/my%20field"},
					},
				},
			},
		},
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	resolved := doc.Components.Schemas["Consumer"].Properties["val"].Resolved()
	if resolved.Type != "boolean" {
		t.Errorf("resolved type = %q, want boolean", resolved.Type)
	}
}

func TestResolve_DocPointerResponse(t *testing.T) {
	doc := &Document{
		OpenAPI: "3.0.3",
		Paths: map[string]*PathItem{
			"/v1/auth": {
				Post: &Operation{
					Responses: map[string]*ResponseOrRef{
						"4XX": {Response: &Response{Description: "client error"}},
						"200": {Response: &Response{Description: "ok"}},
					},
				},
			},
		},
	}
	resp, err := resolveDocPointerResponse(doc, "#/paths/~1v1~1auth/post/responses/4XX")
	if err != nil {
		t.Fatalf("resolveDocPointerResponse: %v", err)
	}
	if resp.Description != "client error" {
		t.Errorf("description = %q, want %q", resp.Description, "client error")
	}
}

func TestResolve_DocPointerResponse_PercentEncoded(t *testing.T) {
	doc := &Document{
		OpenAPI: "3.0.3",
		Paths: map[string]*PathItem{
			"/v1/transactions/{transactionId}:advice": {
				Post: &Operation{
					Responses: map[string]*ResponseOrRef{
						"200": {Response: &Response{Description: "ok from advice"}},
					},
				},
			},
		},
	}
	// Path key contains {transactionId} → %7BtransactionId%7D and : → %3A in percent-encoding.
	resp, err := resolveDocPointerResponse(doc, "#/paths/~1v1~1transactions~1%7BtransactionId%7D%3Aadvice/post/responses/200")
	if err != nil {
		t.Fatalf("resolveDocPointerResponse: %v", err)
	}
	if resp.Description != "ok from advice" {
		t.Errorf("description = %q, want %q", resp.Description, "ok from advice")
	}
}

func TestResolve_DocPointerParameter(t *testing.T) {
	doc := &Document{
		OpenAPI: "3.0.3",
		Paths: map[string]*PathItem{
			"/v1/items/{id}": {
				Delete: &Operation{
					Parameters: []*ParameterOrRef{
						{Parameter: &Parameter{Name: "id", In: "path"}},
						{Parameter: &Parameter{Name: "force", In: "query"}},
					},
				},
			},
		},
	}
	p, err := resolveDocPointerParameter(doc, "#/paths/~1v1~1items~1%7Bid%7D/delete/parameters/1")
	if err != nil {
		t.Fatalf("resolveDocPointerParameter: %v", err)
	}
	if p.Name != "force" {
		t.Errorf("parameter name = %q, want %q", p.Name, "force")
	}
}

func TestResolve_DocPointerSchema_RequestBody(t *testing.T) {
	doc := &Document{
		OpenAPI: "3.0.3",
		Paths: map[string]*PathItem{
			"/v1/users": {
				Post: &Operation{
					RequestBody: &RequestBodyOrRef{
						RequestBody: &RequestBody{
							Content: map[string]*MediaType{
								"application/json": {
									Schema: &Schema{Type: "object", Properties: map[string]*Schema{
										"name": {Type: "string"},
									}},
								},
							},
						},
					},
				},
			},
		},
	}
	s, err := resolveDocPointerSchema(doc, "#/paths/~1v1~1users/post/requestBody/content/application~1json/schema")
	if err != nil {
		t.Fatalf("resolveDocPointerSchema: %v", err)
	}
	if s.Type != "object" {
		t.Errorf("schema type = %q, want %q", s.Type, "object")
	}
	if _, ok := s.Properties["name"]; !ok {
		t.Error("expected property 'name' in schema")
	}
}

func TestResolve_DocPointerSchema_Callback(t *testing.T) {
	doc := &Document{
		OpenAPI: "3.0.3",
		Paths: map[string]*PathItem{
			"/v1/links": {
				Post: &Operation{
					Callbacks: map[string]Callback{
						"onData": {
							"callbackUrl": &PathItem{
								Post: &Operation{
									RequestBody: &RequestBodyOrRef{
										RequestBody: &RequestBody{
											Content: map[string]*MediaType{
												"application/json": {
													Schema: &Schema{Type: "object", Properties: map[string]*Schema{
														"status": {Type: "string"},
													}},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	s, err := resolveDocPointerSchema(doc, "#/paths/~1v1~1links/post/callbacks/onData/callbackUrl/post/requestBody/content/application~1json/schema")
	if err != nil {
		t.Fatalf("resolveDocPointerSchema: %v", err)
	}
	if s.Type != "object" {
		t.Errorf("schema type = %q, want %q", s.Type, "object")
	}
	if _, ok := s.Properties["status"]; !ok {
		t.Error("expected property 'status' in schema")
	}
}

func TestResolve_DocPointerSchema_ResponseContent(t *testing.T) {
	doc := &Document{
		OpenAPI: "3.0.3",
		Paths: map[string]*PathItem{
			"/v1/auth": {
				Post: &Operation{
					Responses: map[string]*ResponseOrRef{
						"200": {Response: &Response{
							Description: "ok",
							Content: map[string]*MediaType{
								"application/json": {
									Schema: &Schema{Type: "object", Properties: map[string]*Schema{
										"token": {Type: "string"},
									}},
								},
							},
						}},
					},
				},
			},
		},
	}
	s, err := resolveDocPointerSchema(doc, "#/paths/~1v1~1auth/post/responses/200/content/application~1json/schema")
	if err != nil {
		t.Fatalf("resolveDocPointerSchema: %v", err)
	}
	if s.Type != "object" {
		t.Errorf("schema type = %q, want %q", s.Type, "object")
	}
	if _, ok := s.Properties["token"]; !ok {
		t.Error("expected property 'token' in schema")
	}
}

func TestResolve_DocPointerSchema_CallbackRuntimeExpr(t *testing.T) {
	// Callback key is a runtime expression containing special characters.
	doc := &Document{
		OpenAPI: "3.0.3",
		Paths: map[string]*PathItem{
			"/v1/hooks": {
				Post: &Operation{
					Callbacks: map[string]Callback{
						"event": {
							"{$request.body#/callbackUrl}": &PathItem{
								Post: &Operation{
									RequestBody: &RequestBodyOrRef{
										RequestBody: &RequestBody{
											Content: map[string]*MediaType{
												"application/json": {
													Schema: &Schema{Type: "object", Properties: map[string]*Schema{
														"event_type": {Type: "string"},
													}},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	// The expression key {$request.body#/callbackUrl} contains # and / which
	// need ~1 escaping for / in the JSON Pointer. The # does not need escaping
	// as it is not a separator in JSON Pointer tokens.
	// Encoded: {$request.body#~1callbackUrl}
	s, err := resolveDocPointerSchema(doc,
		"#/paths/~1v1~1hooks/post/callbacks/event/{$request.body#~1callbackUrl}/post/requestBody/content/application~1json/schema")
	if err != nil {
		t.Fatalf("resolveDocPointerSchema: %v", err)
	}
	if s.Type != "object" {
		t.Errorf("schema type = %q, want %q", s.Type, "object")
	}
	if _, ok := s.Properties["event_type"]; !ok {
		t.Error("expected property 'event_type' in schema")
	}
}

func TestResolve_DocPointerNotFound(t *testing.T) {
	doc := &Document{
		OpenAPI: "3.0.3",
		Paths:   map[string]*PathItem{},
	}
	_, err := resolveDocPointerResponse(doc, "#/paths/~1missing/get/responses/200")
	if err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestResolve_DocPointerResponse_Integration(t *testing.T) {
	// Test full Resolve() with a response $ref pointing to #/paths/...
	doc := &Document{
		OpenAPI: "3.0.3",
		Paths: map[string]*PathItem{
			"/v1/auth": {
				Post: &Operation{
					Responses: map[string]*ResponseOrRef{
						"4XX": {Response: &Response{
							Description: "client error",
							Content: map[string]*MediaType{
								"application/json": {
									Schema: &Schema{Type: "object", Properties: map[string]*Schema{
										"code": {Type: "string"},
									}},
								},
							},
						}},
					},
				},
			},
			"/v1/users": {
				Get: &Operation{
					Responses: map[string]*ResponseOrRef{
						"4XX": {Ref: "#/paths/~1v1~1auth/post/responses/4XX"},
						"200": {Response: &Response{Description: "ok"}},
					},
				},
			},
		},
	}
	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	resp := doc.Paths["/v1/users"].Get.Responses["4XX"]
	if resp.Response == nil {
		t.Fatal("expected resolved response")
	}
	if resp.Response.Description != "client error" {
		t.Errorf("description = %q, want %q", resp.Response.Description, "client error")
	}
}
