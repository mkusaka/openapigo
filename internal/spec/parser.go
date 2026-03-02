package spec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load parses an OpenAPI spec from a file path.
// Supports YAML (.yaml, .yml) and JSON (.json) formats.
func Load(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read spec: %w", err)
	}
	return Parse(data, filepath.Ext(path))
}

// Parse parses an OpenAPI spec from raw bytes.
// ext is the file extension (e.g., ".yaml", ".json") to determine format.
// If ext is empty, YAML is assumed (YAML is a superset of JSON).
func Parse(data []byte, ext string) (*Document, error) {
	ext = strings.ToLower(ext)

	// For JSON, parse directly (preserving json.RawMessage fields).
	if ext == ".json" {
		return parseJSON(data)
	}
	// For YAML or unknown, use the YAML parser (YAML is a superset of JSON).
	return parseYAML(data)
}

func parseJSON(data []byte) (*Document, error) {
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse JSON spec: %w", err)
	}
	// Extract path order from raw JSON.
	doc.PathOrder = extractJSONKeyOrder(data, "paths")
	// Extract property order for each schema.
	extractSchemaPropertyOrders(&doc, data)
	return &doc, nil
}

func parseYAML(data []byte) (*Document, error) {
	// First parse into yaml.Node to extract key ordering.
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, fmt.Errorf("parse YAML spec: %w", err)
	}

	// Then unmarshal into our typed structure.
	var doc Document
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse YAML spec: %w", err)
	}

	// Extract path order from YAML node.
	doc.PathOrder = extractYAMLKeyOrder(&node, "paths")
	// Extract property order for schemas.
	extractSchemaPropertyOrdersFromYAML(&doc, &node)

	return &doc, nil
}

// extractJSONKeyOrder extracts the key order of a top-level object field.
func extractJSONKeyOrder(data []byte, field string) []string {
	// Quick and dirty: decode into map with ordered keys using json.Decoder.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	fieldData, ok := raw[field]
	if !ok {
		return nil
	}
	return orderedKeysFromJSON(fieldData)
}

func orderedKeysFromJSON(data json.RawMessage) []string {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return nil
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			break
		}
		key, ok := t.(string)
		if !ok {
			break
		}
		keys = append(keys, key)
		// Skip the value.
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			break
		}
	}
	return keys
}

// extractYAMLKeyOrder extracts the key order of a top-level mapping field.
func extractYAMLKeyOrder(root *yaml.Node, field string) []string {
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil
	}
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == field {
			return yamlMappingKeys(mapping.Content[i+1])
		}
	}
	return nil
}

func yamlMappingKeys(node *yaml.Node) []string {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	var keys []string
	for i := 0; i+1 < len(node.Content); i += 2 {
		keys = append(keys, node.Content[i].Value)
	}
	return keys
}

// extractSchemaPropertyOrders is a placeholder for JSON property order extraction.
// Full implementation would walk all schemas and extract property key orders.
func extractSchemaPropertyOrders(doc *Document, data []byte) {
	if doc.Components == nil {
		return
	}
	// For components/schemas, extract property order from raw JSON.
	var raw struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	for name, schemaData := range raw.Components.Schemas {
		s, ok := doc.Components.Schemas[name]
		if !ok || s == nil {
			continue
		}
		s.PropertyOrder = extractPropertyOrder(schemaData)
		extractNestedPropertyOrders(s, schemaData)
	}
}

func extractPropertyOrder(data json.RawMessage) []string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	propsData, ok := raw["properties"]
	if !ok {
		return nil
	}
	return orderedKeysFromJSON(propsData)
}

func extractNestedPropertyOrders(s *Schema, data json.RawMessage) {
	if s == nil {
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	// Handle properties' nested schemas.
	if propsData, ok := raw["properties"]; ok {
		var props map[string]json.RawMessage
		if err := json.Unmarshal(propsData, &props); err == nil {
			for name, propData := range props {
				if prop, ok := s.Properties[name]; ok && prop != nil {
					prop.PropertyOrder = extractPropertyOrder(propData)
					extractNestedPropertyOrders(prop, propData)
				}
			}
		}
	}
	// Handle items.
	if itemsData, ok := raw["items"]; ok && s.Items != nil {
		s.Items.PropertyOrder = extractPropertyOrder(itemsData)
		extractNestedPropertyOrders(s.Items, itemsData)
	}
	// Handle allOf, oneOf, anyOf.
	for _, key := range []string{"allOf", "oneOf", "anyOf"} {
		arrData, ok := raw[key]
		if !ok {
			continue
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(arrData, &arr); err != nil {
			continue
		}
		var schemas []*Schema
		switch key {
		case "allOf":
			schemas = s.AllOf
		case "oneOf":
			schemas = s.OneOf
		case "anyOf":
			schemas = s.AnyOf
		}
		for i, elemData := range arr {
			if i < len(schemas) && schemas[i] != nil {
				schemas[i].PropertyOrder = extractPropertyOrder(elemData)
				extractNestedPropertyOrders(schemas[i], elemData)
			}
		}
	}
}

// extractSchemaPropertyOrdersFromYAML is a placeholder for YAML property order extraction.
func extractSchemaPropertyOrdersFromYAML(doc *Document, root *yaml.Node) {
	if doc.Components == nil {
		return
	}
	// Find components/schemas node.
	schemasNode := findYAMLPath(root, "components", "schemas")
	if schemasNode == nil || schemasNode.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(schemasNode.Content); i += 2 {
		name := schemasNode.Content[i].Value
		schemaNode := schemasNode.Content[i+1]
		s, ok := doc.Components.Schemas[name]
		if !ok || s == nil {
			continue
		}
		s.PropertyOrder = extractYAMLPropertyOrder(schemaNode)
		extractNestedYAMLPropertyOrders(s, schemaNode)
	}
}

func extractYAMLPropertyOrder(node *yaml.Node) []string {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == "properties" {
			return yamlMappingKeys(node.Content[i+1])
		}
	}
	return nil
}

func extractNestedYAMLPropertyOrders(s *Schema, node *yaml.Node) {
	if s == nil || node == nil || node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		switch key {
		case "properties":
			if val.Kind == yaml.MappingNode {
				for j := 0; j+1 < len(val.Content); j += 2 {
					propName := val.Content[j].Value
					propNode := val.Content[j+1]
					if prop, ok := s.Properties[propName]; ok && prop != nil {
						prop.PropertyOrder = extractYAMLPropertyOrder(propNode)
						extractNestedYAMLPropertyOrders(prop, propNode)
					}
				}
			}
		case "items":
			if s.Items != nil {
				s.Items.PropertyOrder = extractYAMLPropertyOrder(val)
				extractNestedYAMLPropertyOrders(s.Items, val)
			}
		case "allOf", "oneOf", "anyOf":
			if val.Kind != yaml.SequenceNode {
				continue
			}
			var schemas []*Schema
			switch key {
			case "allOf":
				schemas = s.AllOf
			case "oneOf":
				schemas = s.OneOf
			case "anyOf":
				schemas = s.AnyOf
			}
			for idx, elemNode := range val.Content {
				if idx < len(schemas) && schemas[idx] != nil {
					schemas[idx].PropertyOrder = extractYAMLPropertyOrder(elemNode)
					extractNestedYAMLPropertyOrders(schemas[idx], elemNode)
				}
			}
		}
	}
}

func findYAMLPath(root *yaml.Node, keys ...string) *yaml.Node {
	if root == nil {
		return nil
	}
	node := root
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	for _, key := range keys {
		if node.Kind != yaml.MappingNode {
			return nil
		}
		found := false
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				node = node.Content[i+1]
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return node
}

// DetectVersion returns the OpenAPI version string from the document.
// Returns an error if the version cannot be determined or is unsupported.
func DetectVersion(doc *Document) (string, error) {
	v := doc.OpenAPI
	if v == "" {
		return "", fmt.Errorf("cannot detect OpenAPI version: ensure the spec has an \"openapi\" field")
	}
	if strings.HasPrefix(v, "3.0") || strings.HasPrefix(v, "3.1") {
		return v, nil
	}
	return "", fmt.Errorf("unsupported OpenAPI version %q: only 3.0.x and 3.1.x are supported", v)
}
