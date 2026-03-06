package generate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mkusaka/openapigo/internal/spec"
)

// hasConstraints reports whether a schema has any validation constraints.
func hasConstraints(s *spec.Schema) bool {
	if s == nil {
		return false
	}
	if s.MinLength != nil || s.MaxLength != nil || s.Pattern != "" {
		return true
	}
	if s.Minimum != nil || s.Maximum != nil || s.ExclusiveMinimum != nil || s.ExclusiveMaximum != nil || s.MultipleOf != nil {
		return true
	}
	if s.MinItems != nil || s.MaxItems != nil {
		return true
	}
	if len(s.Enum) > 0 {
		return true
	}
	if len(s.DependentRequired) > 0 {
		return true
	}
	return false
}

// hasValidatableFields reports whether a struct schema has any fields that need validation
// (either the field itself has constraints, or the field type has a Validate() method).
func hasValidatableFields(s *spec.Schema) bool {
	for _, prop := range s.Properties {
		if prop == nil {
			continue
		}
		resolved := prop.Resolved()
		if hasConstraints(resolved) {
			return true
		}
		// Nested objects with constraints need recursive validation.
		if (resolved.Type == "object" || len(resolved.Properties) > 0) && hasValidatableFields(resolved) {
			return true
		}
		// Array items with constraints.
		if resolved.Type == "array" && resolved.Items != nil && hasConstraints(resolved.Items.Resolved()) {
			return true
		}
	}
	return false
}

// needsValidation reports whether a schema needs a Validate() method.
// Only struct types (objects with properties) get Validate().
// Named enum types do NOT — their validation is done at the field level.
func needsValidation(s *spec.Schema) bool {
	if s == nil {
		return false
	}
	// Only generate Validate() for struct types.
	if s.Type != "object" && len(s.Properties) == 0 {
		return false
	}
	if len(s.DependentRequired) > 0 {
		return true
	}
	if hasDependentSchemasConstraints(s) {
		return true
	}
	return hasValidatableFields(s)
}

// hasDependentSchemasConstraints reports whether dependentSchemas has any
// entries with required fields or property constraints worth validating.
func hasDependentSchemasConstraints(s *spec.Schema) bool {
	for _, ds := range s.DependentSchemas {
		if ds == nil {
			continue
		}
		if len(ds.Required) > 0 {
			return true
		}
		for _, prop := range ds.Properties {
			if prop != nil && hasConstraints(prop.Resolved()) {
				return true
			}
		}
	}
	return false
}

// hasDependentSchemasFieldConstraints reports whether dependentSchemas has
// property constraints that use fmt.Sprintf in the generated code.
// Required-only and pattern-only entries don't need fmt.
func hasDependentSchemasFieldConstraints(s *spec.Schema) bool {
	for _, ds := range s.DependentSchemas {
		if ds == nil {
			continue
		}
		for _, prop := range ds.Properties {
			if prop == nil {
				continue
			}
			r := prop.Resolved()
			// Check for constraints that generate fmt.Sprintf (excludes pattern).
			if r.MinLength != nil || r.MaxLength != nil {
				return true
			}
			if r.Minimum != nil || r.Maximum != nil || r.ExclusiveMinimum != nil || r.ExclusiveMaximum != nil || r.MultipleOf != nil {
				return true
			}
			if r.MinItems != nil || r.MaxItems != nil {
				return true
			}
			if len(r.Enum) > 0 {
				return true
			}
		}
	}
	return false
}

// emitValidateMethod generates a Validate() method for a struct type.
func (g *Generator) emitValidateMethod(w *strings.Builder, typeName string, s *spec.Schema) {
	_, hasUnevaluated := g.unevaluatedEvalKeys[typeName]
	if s == nil || (!needsValidation(s) && !hasUnevaluated) {
		return
	}

	g.imports["github.com/mkusaka/openapigo"] = true
	if hasValidatableFields(s) || hasDependentSchemasFieldConstraints(s) || hasUnevaluated {
		g.imports["fmt"] = true
	}

	fmt.Fprintf(w, "// Validate checks all constraints on %s.\nfunc (v %s) Validate() error {\n", typeName, typeName)
	fmt.Fprintf(w, "\tvar errs openapigo.ValidationErrors\n")

	propOrder := s.PropertyOrder
	if len(propOrder) == 0 {
		for name := range s.Properties {
			propOrder = append(propOrder, name)
		}
		// sort for deterministic output
		sortStrings(propOrder)
	}

	for _, propName := range propOrder {
		prop := s.Properties[propName]
		if prop == nil {
			continue
		}
		resolved := prop.Resolved()
		fieldName := g.resolveFieldName(resolved, propName)
		req := isRequired(propName, s.Required)
		nullable := isNullable(resolved)

		if !hasConstraints(resolved) {
			continue
		}

		// For optional/nullable fields, skip validation if nil and dereference.
		isPtr := !req && !nullable // optional non-nullable → *T
		if nullable {
			// Nullable[T]: extract value via Get() into a temp variable.
			tmpVar := "val" + fieldName
			fmt.Fprintf(w, "\tif %s, ok := v.%s.Get(); ok {\n", tmpVar, fieldName)
			emitFieldConstraints(w, fieldName, tmpVar, resolved, "\t\t", g.config.StrictEnums)
			emitPatternCheck(w, typeName, fieldName, tmpVar, resolved, "\t\t")
			fmt.Fprintf(w, "\t}\n")
		} else if isPtr {
			fmt.Fprintf(w, "\tif v.%s != nil {\n", fieldName)
			accessor := "*v." + fieldName
			emitFieldConstraints(w, fieldName, accessor, resolved, "\t\t", g.config.StrictEnums)
			emitPatternCheck(w, typeName, fieldName, accessor, resolved, "\t\t")
			fmt.Fprintf(w, "\t}\n")
		} else {
			accessor := "v." + fieldName
			emitFieldConstraints(w, fieldName, accessor, resolved, "\t", g.config.StrictEnums)
			emitPatternCheck(w, typeName, fieldName, accessor, resolved, "\t")
		}
	}

	// dependentRequired: if property X is non-zero, check that dependent properties are non-zero.
	if len(s.DependentRequired) > 0 {
		// Sort keys for deterministic output.
		depKeys := make([]string, 0, len(s.DependentRequired))
		for k := range s.DependentRequired {
			depKeys = append(depKeys, k)
		}
		sortStrings(depKeys)
		for _, key := range depKeys {
			deps := s.DependentRequired[key]
			if len(deps) == 0 {
				continue
			}
			keyField := ToFieldName(key)
			// Check if the trigger property is present (non-zero).
			fmt.Fprintf(w, "\tif !openapigo.IsZero(v.%s) {\n", keyField)
			for _, dep := range deps {
				depField := ToFieldName(dep)
				fmt.Fprintf(w, "\t\tif openapigo.IsZero(v.%s) {\n", depField)
				fmt.Fprintf(w, "\t\t\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"dependentRequired\", Message: %q})\n",
					dep, fmt.Sprintf("required when %s is present", key))
				fmt.Fprintf(w, "\t\t}\n")
			}
			fmt.Fprintf(w, "\t}\n")
		}
	}

	// dependentSchemas: if trigger property is non-zero, apply the dependent schema's
	// required fields and property constraints.
	if hasDependentSchemasConstraints(s) {
		dsKeys := make([]string, 0, len(s.DependentSchemas))
		for k := range s.DependentSchemas {
			dsKeys = append(dsKeys, k)
		}
		sortStrings(dsKeys)
		for _, key := range dsKeys {
			ds := s.DependentSchemas[key]
			if ds == nil {
				continue
			}
			// Skip if no useful constraints.
			if len(ds.Required) == 0 {
				hasProps := false
				for _, prop := range ds.Properties {
					if prop != nil && hasConstraints(prop.Resolved()) {
						hasProps = true
						break
					}
				}
				if !hasProps {
					continue
				}
			}
			keyField := g.resolveParentFieldName(s, key)
			fmt.Fprintf(w, "\tif !openapigo.IsZero(v.%s) {\n", keyField)
			// Required fields in the dependent schema.
			for _, req := range ds.Required {
				reqField := g.resolveParentFieldName(s, req)
				fmt.Fprintf(w, "\t\tif openapigo.IsZero(v.%s) {\n", reqField)
				fmt.Fprintf(w, "\t\t\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"dependentSchemas\", Message: %q})\n",
					req, fmt.Sprintf("required when %s is present", key))
				fmt.Fprintf(w, "\t\t}\n")
			}
			// Property constraints in the dependent schema.
			// Use parent schema's required/nullable to determine accessor.
			dsPropOrder := make([]string, 0, len(ds.Properties))
			for pn := range ds.Properties {
				dsPropOrder = append(dsPropOrder, pn)
			}
			sortStrings(dsPropOrder)
			for _, pn := range dsPropOrder {
				prop := ds.Properties[pn]
				if prop == nil {
					continue
				}
				resolved := prop.Resolved()
				if !hasConstraints(resolved) {
					continue
				}
				// Look up the field in the parent schema to determine pointer-ness and name.
				parentProp := s.Properties[pn]
				var parentResolved *spec.Schema
				if parentProp != nil {
					parentResolved = parentProp.Resolved()
				}
				fieldName := g.resolveParentFieldName(s, pn)
				req := isRequired(pn, s.Required)
				nullable := parentResolved != nil && isNullable(parentResolved)
				isPtr := !req && !nullable
				if nullable {
					// Nullable[T]: extract value via Get().
					tmpVar := "val" + fieldName
					fmt.Fprintf(w, "\t\tif %s, ok := v.%s.Get(); ok {\n", tmpVar, fieldName)
					emitFieldConstraints(w, fieldName, tmpVar, resolved, "\t\t\t", g.config.StrictEnums)
					emitPatternCheck(w, typeName, fieldName, tmpVar, resolved, "\t\t\t")
					fmt.Fprintf(w, "\t\t}\n")
				} else if isPtr {
					// Optional non-nullable: pointer dereference.
					fmt.Fprintf(w, "\t\tif v.%s != nil {\n", fieldName)
					accessor := "*v." + fieldName
					emitFieldConstraints(w, fieldName, accessor, resolved, "\t\t\t", g.config.StrictEnums)
					emitPatternCheck(w, typeName, fieldName, accessor, resolved, "\t\t\t")
					fmt.Fprintf(w, "\t\t}\n")
				} else {
					accessor := "v." + fieldName
					emitFieldConstraints(w, fieldName, accessor, resolved, "\t\t", g.config.StrictEnums)
					emitPatternCheck(w, typeName, fieldName, accessor, resolved, "\t\t")
				}
			}
			fmt.Fprintf(w, "\t}\n")
		}
	}

	// unevaluatedProperties: false → check for keys not in the evaluated set.
	if evalKeys, ok := g.unevaluatedEvalKeys[typeName]; ok {
		fmt.Fprintf(w, "\tevaluated := map[string]bool{\n")
		for _, k := range evalKeys {
			fmt.Fprintf(w, "\t\t%q: true,\n", k)
		}
		fmt.Fprintf(w, "\t}\n")

		// oneOf/anyOf branch matching: add branch properties (and patterns) if required fields are present.
		if bs, ok := g.unevaluatedBranches[typeName]; ok {
			for i, b := range bs.branches {
				if len(b.required) == 0 {
					// No required fields → conservatively include all branch properties.
					for _, p := range b.props {
						fmt.Fprintf(w, "\tevaluated[%q] = true\n", p)
					}
					// Also include branch patterns unconditionally.
					for j := range b.patterns {
						varName := fmt.Sprintf("patternUnevalBranch%s_%d_%d", typeName, i, j)
						fmt.Fprintf(w, "\tfor k := range v.rawFieldKeys {\n")
						fmt.Fprintf(w, "\t\tif !evaluated[k] && %s.MatchString(k) {\n", varName)
						fmt.Fprintf(w, "\t\t\tevaluated[k] = true\n")
						fmt.Fprintf(w, "\t\t}\n")
						fmt.Fprintf(w, "\t}\n")
					}
				} else {
					fmt.Fprintf(w, "\t// %s branch %d\n", bs.kind, i)
					fmt.Fprintf(w, "\tif ")
					for j, r := range b.required {
						if j > 0 {
							fmt.Fprintf(w, " && ")
						}
						fmt.Fprintf(w, "v.rawFieldKeys[%q]", r)
					}
					fmt.Fprintf(w, " {\n")
					for _, p := range b.props {
						fmt.Fprintf(w, "\t\tevaluated[%q] = true\n", p)
					}
					// Matched-branch-only pattern evaluation (strict).
					for j := range b.patterns {
						varName := fmt.Sprintf("patternUnevalBranch%s_%d_%d", typeName, i, j)
						fmt.Fprintf(w, "\t\tfor k := range v.rawFieldKeys {\n")
						fmt.Fprintf(w, "\t\t\tif !evaluated[k] && %s.MatchString(k) {\n", varName)
						fmt.Fprintf(w, "\t\t\t\tevaluated[k] = true\n")
						fmt.Fprintf(w, "\t\t\t}\n")
						fmt.Fprintf(w, "\t\t}\n")
					}
					fmt.Fprintf(w, "\t}\n")
				}
			}
		}

		patterns := g.unevaluatedPatterns[typeName]
		fmt.Fprintf(w, "\tfor k := range v.rawFieldKeys {\n")
		if len(patterns) > 0 {
			fmt.Fprintf(w, "\t\tif evaluated[k] {\n\t\t\tcontinue\n\t\t}\n")
			for i := range patterns {
				varName := fmt.Sprintf("patternUneval%s%d", typeName, i)
				fmt.Fprintf(w, "\t\tif %s.MatchString(k) {\n\t\t\tcontinue\n\t\t}\n", varName)
			}
			fmt.Fprintf(w, "\t\terrs = append(errs, openapigo.ValidationError{Field: k, Constraint: \"unevaluatedProperties\", Message: fmt.Sprintf(\"unevaluated property %%q\", k)})\n")
		} else {
			fmt.Fprintf(w, "\t\tif !evaluated[k] {\n")
			fmt.Fprintf(w, "\t\t\terrs = append(errs, openapigo.ValidationError{Field: k, Constraint: \"unevaluatedProperties\", Message: fmt.Sprintf(\"unevaluated property %%q\", k)})\n")
			fmt.Fprintf(w, "\t\t}\n")
		}
		fmt.Fprintf(w, "\t}\n")
	}

	fmt.Fprintf(w, "\tif len(errs) > 0 {\n\t\treturn errs\n\t}\n")
	fmt.Fprintf(w, "\treturn nil\n}\n\n")

	// Generate UnmarshalJSON that calls Validate() after unmarshalling.
	// Skip if a custom UnmarshalJSON was already emitted for unevaluatedProperties.
	if g.config.ValidateOnUnmarshal && !hasUnevaluated {
		g.imports["encoding/json"] = true
		fmt.Fprintf(w, "// UnmarshalJSON unmarshals JSON and validates the result.\n")
		fmt.Fprintf(w, "func (v *%s) UnmarshalJSON(data []byte) error {\n", typeName)
		fmt.Fprintf(w, "\ttype alias %s\n", typeName)
		fmt.Fprintf(w, "\tvar a alias\n")
		fmt.Fprintf(w, "\tif err := json.Unmarshal(data, &a); err != nil {\n")
		fmt.Fprintf(w, "\t\treturn err\n\t}\n")
		fmt.Fprintf(w, "\t*v = %s(a)\n", typeName)
		fmt.Fprintf(w, "\treturn v.Validate()\n")
		fmt.Fprintf(w, "}\n\n")
	}
}

func emitFieldConstraints(w *strings.Builder, fieldName, accessor string, s *spec.Schema, indent string, strictEnums bool) {
	switch s.Type {
	case "string":
		emitStringConstraints(w, fieldName, accessor, s, indent)
	case "integer", "number":
		emitNumericConstraints(w, fieldName, accessor, s, indent)
	case "array":
		emitArrayConstraints(w, fieldName, accessor, s, indent)
	}

	// Enum validation: string enums by default, all types with strictEnums.
	if len(s.Enum) > 0 && (s.Type == "string" || strictEnums) {
		emitEnumValidation(w, fieldName, accessor, s, indent)
	}
}

func emitPatternCheck(w *strings.Builder, typeName, fieldName, accessor string, s *spec.Schema, indent string) {
	if s.Pattern != "" {
		fmt.Fprintf(w, "%sif !pattern%s%s.MatchString(string(%s)) {\n", indent, typeName, fieldName, accessor)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"pattern\", Message: \"does not match pattern\"})\n",
			indent, fieldName)
		fmt.Fprintf(w, "%s}\n", indent)
	}
}

func emitStringConstraints(w *strings.Builder, fieldName, accessor string, s *spec.Schema, indent string) {
	if s.MinLength != nil {
		fmt.Fprintf(w, "%sif len(%s) < %d {\n", indent, accessor, *s.MinLength)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"minLength\", Message: fmt.Sprintf(\"length %%d is less than minimum %d\", len(%s))})\n",
			indent, fieldName, *s.MinLength, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
	if s.MaxLength != nil {
		fmt.Fprintf(w, "%sif len(%s) > %d {\n", indent, accessor, *s.MaxLength)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"maxLength\", Message: fmt.Sprintf(\"length %%d exceeds maximum %d\", len(%s))})\n",
			indent, fieldName, *s.MaxLength, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
	// Note: pattern validation is handled separately in emitValidateMethod
	// because we need the type name for the pattern variable reference.
}

func emitNumericConstraints(w *strings.Builder, fieldName, accessor string, s *spec.Schema, indent string) {
	if s.Minimum != nil {
		fmt.Fprintf(w, "%sif %s < %v {\n", indent, accessor, *s.Minimum)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"minimum\", Message: fmt.Sprintf(\"value %%v is less than minimum %v\", %s)})\n",
			indent, fieldName, *s.Minimum, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
	if s.Maximum != nil {
		fmt.Fprintf(w, "%sif %s > %v {\n", indent, accessor, *s.Maximum)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"maximum\", Message: fmt.Sprintf(\"value %%v exceeds maximum %v\", %s)})\n",
			indent, fieldName, *s.Maximum, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
	if s.ExclusiveMinimum != nil {
		fmt.Fprintf(w, "%sif %s <= %v {\n", indent, accessor, *s.ExclusiveMinimum)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"exclusiveMinimum\", Message: fmt.Sprintf(\"value %%v must be greater than %v\", %s)})\n",
			indent, fieldName, *s.ExclusiveMinimum, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
	if s.ExclusiveMaximum != nil {
		fmt.Fprintf(w, "%sif %s >= %v {\n", indent, accessor, *s.ExclusiveMaximum)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"exclusiveMaximum\", Message: fmt.Sprintf(\"value %%v must be less than %v\", %s)})\n",
			indent, fieldName, *s.ExclusiveMaximum, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
}

func emitArrayConstraints(w *strings.Builder, fieldName, accessor string, s *spec.Schema, indent string) {
	if s.MinItems != nil {
		fmt.Fprintf(w, "%sif len(%s) < %d {\n", indent, accessor, *s.MinItems)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"minItems\", Message: fmt.Sprintf(\"length %%d is less than minimum %d\", len(%s))})\n",
			indent, fieldName, *s.MinItems, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
	if s.MaxItems != nil {
		fmt.Fprintf(w, "%sif len(%s) > %d {\n", indent, accessor, *s.MaxItems)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"maxItems\", Message: fmt.Sprintf(\"length %%d exceeds maximum %d\", len(%s))})\n",
			indent, fieldName, *s.MaxItems, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
}

func emitEnumValidation(w *strings.Builder, fieldName, accessor string, s *spec.Schema, indent string) {
	switch s.Type {
	case "string":
		var vals []string
		for _, raw := range s.Enum {
			var v string
			if json.Unmarshal(raw, &v) == nil {
				vals = append(vals, fmt.Sprintf("%q", v))
			}
		}
		if len(vals) == 0 {
			return
		}
		// Use string() cast to handle named string types (e.g., PetStatus).
		fmt.Fprintf(w, "%sswitch string(%s) {\n", indent, accessor)
		fmt.Fprintf(w, "%scase %s:\n%s\t// valid\n", indent, strings.Join(vals, ", "), indent)
		fmt.Fprintf(w, "%sdefault:\n", indent)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"enum\", Message: fmt.Sprintf(\"invalid value %%q\", %s)})\n",
			indent, fieldName, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	case "integer":
		var vals []string
		for _, raw := range s.Enum {
			var v int64
			if json.Unmarshal(raw, &v) == nil {
				vals = append(vals, fmt.Sprintf("%d", v))
			}
		}
		if len(vals) == 0 {
			return
		}
		fmt.Fprintf(w, "%sswitch int64(%s) {\n", indent, accessor)
		fmt.Fprintf(w, "%scase %s:\n%s\t// valid\n", indent, strings.Join(vals, ", "), indent)
		fmt.Fprintf(w, "%sdefault:\n", indent)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"enum\", Message: fmt.Sprintf(\"invalid value %%v\", %s)})\n",
			indent, fieldName, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	case "number":
		var vals []string
		for _, raw := range s.Enum {
			var v float64
			if json.Unmarshal(raw, &v) == nil {
				vals = append(vals, fmt.Sprintf("%v", v))
			}
		}
		if len(vals) == 0 {
			return
		}
		fmt.Fprintf(w, "%sswitch float64(%s) {\n", indent, accessor)
		fmt.Fprintf(w, "%scase %s:\n%s\t// valid\n", indent, strings.Join(vals, ", "), indent)
		fmt.Fprintf(w, "%sdefault:\n", indent)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"enum\", Message: fmt.Sprintf(\"invalid value %%v\", %s)})\n",
			indent, fieldName, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
}

// sortStrings sorts a string slice in place.
func sortStrings(s []string) {
	// Use slices.Sort if available, otherwise manual sort.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// emitPatternVars emits package-level compiled regexp variables for schema patterns.
// Variable names include the type name to avoid collisions across schemas.
func (g *Generator) emitPatternVars(w *strings.Builder, typeName string, s *spec.Schema) {
	if s == nil || !needsValidation(s) {
		return
	}
	emitted := make(map[string]string) // varName → pattern value (for dedup + collision detection)
	for _, propName := range s.PropertyOrder {
		prop := s.Properties[propName]
		if prop == nil {
			continue
		}
		resolved := prop.Resolved()
		if resolved.Pattern != "" {
			fieldName := g.resolveFieldName(resolved, propName)
			varName := fmt.Sprintf("pattern%s%s", typeName, fieldName)
			if _, ok := emitted[varName]; !ok {
				g.imports["regexp"] = true
				fmt.Fprintf(w, "var %s = regexp.MustCompile(%q)\n", varName, resolved.Pattern)
				emitted[varName] = resolved.Pattern
			}
		}
	}
	// Also emit pattern variables for dependentSchemas properties.
	// Use parent's field name for consistency with emitPatternCheck.
	for _, ds := range s.DependentSchemas {
		if ds == nil {
			continue
		}
		for pn, prop := range ds.Properties {
			if prop == nil {
				continue
			}
			resolved := prop.Resolved()
			if resolved.Pattern != "" {
				fieldName := g.resolveParentFieldName(s, pn)
				varName := fmt.Sprintf("pattern%s%s", typeName, fieldName)
				if existing, ok := emitted[varName]; ok && existing == resolved.Pattern {
					continue // same pattern, already emitted
				} else if ok {
					// Different pattern on same field — disambiguate.
					varName = fmt.Sprintf("patternDS%s%s", typeName, fieldName)
				}
				if _, ok := emitted[varName]; !ok {
					g.imports["regexp"] = true
					fmt.Fprintf(w, "var %s = regexp.MustCompile(%q)\n", varName, resolved.Pattern)
					emitted[varName] = resolved.Pattern
				}
			}
		}
	}
}

// unevalBranch holds property info for a single oneOf/anyOf branch.
type unevalBranch struct {
	props    []string // JSON property names declared in this branch
	required []string // required fields in this branch
	patterns []string // patternProperties patterns in this branch
}

// unevalBranchSet holds oneOf/anyOf branch info for unevaluated property checking.
type unevalBranchSet struct {
	kind     string // "oneOf" or "anyOf"
	branches []unevalBranch
}

// registerUnevaluatedEvalKeys collects evaluated property names (JSON keys) from a merged
// schema and stores them for use by emitValidateMethod and emitStructType.
// Must be called before emitStructType so rawFieldKeys field is generated.
func (g *Generator) registerUnevaluatedEvalKeys(typeName string, merged *spec.Schema) {
	evalKeys := make([]string, 0, len(merged.Properties))
	for propName := range merged.Properties {
		evalKeys = append(evalKeys, propName)
	}
	sortStrings(evalKeys)
	g.unevaluatedEvalKeys[typeName] = evalKeys
}

// emitUnevaluatedUnmarshalJSON generates an UnmarshalJSON that records raw JSON keys
// for unevaluatedProperties checking. The Validate() check is emitted by emitValidateMethod.
// Used for allOf + unevaluatedProperties: false.
func (g *Generator) emitUnevaluatedUnmarshalJSON(w *strings.Builder, typeName string, merged *spec.Schema) {
	g.imports["encoding/json"] = true

	// Generate UnmarshalJSON that records raw keys.
	fmt.Fprintf(w, "// UnmarshalJSON unmarshals JSON and records raw field keys for unevaluatedProperties checking.\n")
	fmt.Fprintf(w, "func (v *%s) UnmarshalJSON(data []byte) error {\n", typeName)
	fmt.Fprintf(w, "\ttype alias %s\n", typeName)
	fmt.Fprintf(w, "\tvar a alias\n")
	fmt.Fprintf(w, "\tif err := json.Unmarshal(data, &a); err != nil {\n")
	fmt.Fprintf(w, "\t\treturn err\n\t}\n")
	fmt.Fprintf(w, "\t*v = %s(a)\n", typeName)
	fmt.Fprintf(w, "\tvar raw map[string]json.RawMessage\n")
	fmt.Fprintf(w, "\tif err := json.Unmarshal(data, &raw); err != nil {\n")
	fmt.Fprintf(w, "\t\treturn err\n\t}\n")
	fmt.Fprintf(w, "\tv.rawFieldKeys = make(map[string]bool, len(raw))\n")
	fmt.Fprintf(w, "\tfor k := range raw {\n")
	fmt.Fprintf(w, "\t\tv.rawFieldKeys[k] = true\n")
	fmt.Fprintf(w, "\t}\n")
	if g.config.ValidateOnUnmarshal {
		fmt.Fprintf(w, "\tif err := v.Validate(); err != nil {\n")
		fmt.Fprintf(w, "\t\treturn err\n\t}\n")
	}
	fmt.Fprintf(w, "\treturn nil\n}\n\n")
}

// emitUnevaluatedPatternVars emits package-level compiled regexp variables
// for patternProperties patterns used in unevaluated property checking.
// Includes base patterns (always-apply) and branch-specific patterns (matched-branch-only).
func (g *Generator) emitUnevaluatedPatternVars(w *strings.Builder, typeName string) {
	// Base patterns (always apply).
	if patterns, ok := g.unevaluatedPatterns[typeName]; ok {
		g.imports["regexp"] = true
		for i, pattern := range patterns {
			varName := fmt.Sprintf("patternUneval%s%d", typeName, i)
			fmt.Fprintf(w, "var %s = regexp.MustCompile(%q)\n", varName, pattern)
		}
	}
	// Branch-specific patterns (matched-branch-only).
	if bs, ok := g.unevaluatedBranches[typeName]; ok {
		for i, b := range bs.branches {
			for j, pattern := range b.patterns {
				g.imports["regexp"] = true
				varName := fmt.Sprintf("patternUnevalBranch%s_%d_%d", typeName, i, j)
				fmt.Fprintf(w, "var %s = regexp.MustCompile(%q)\n", varName, pattern)
			}
		}
	}
}

// emitUnevaluatedItemsValidate generates a Validate() method for array types
// with prefixItems + unevaluatedItems: false, rejecting extra elements.
func (g *Generator) emitUnevaluatedItemsValidate(w *strings.Builder, typeName string, s *spec.Schema) {
	g.imports["fmt"] = true
	g.imports["github.com/mkusaka/openapigo"] = true

	prefixLen := len(s.PrefixItems)

	fmt.Fprintf(w, "// Validate checks that no unevaluated items are present.\n")
	fmt.Fprintf(w, "func (v %s) Validate() error {\n", typeName)
	fmt.Fprintf(w, "\tif len(v) > %d {\n", prefixLen)
	fmt.Fprintf(w, "\t\treturn openapigo.ValidationErrors{openapigo.ValidationError{Field: \"items\", Constraint: \"unevaluatedItems\", Message: fmt.Sprintf(\"array has %%d items, maximum allowed is %d\", len(v))}}\n", prefixLen)
	fmt.Fprintf(w, "\t}\n")
	fmt.Fprintf(w, "\treturn nil\n}\n\n")
}

// emitUnevaluatedItemsSchemaValidate generates a Validate() method for array types
// with prefixItems + unevaluatedItems: {schema}, validating extra items match the schema type.
// Only simple primitive types (string, number, integer, boolean) are supported;
// complex schemas are silently skipped (known limitation).
func (g *Generator) emitUnevaluatedItemsSchemaValidate(w *strings.Builder, typeName string, s *spec.Schema) {
	schema := s.UnevaluatedItems.Schema.Resolved()
	prefixLen := len(s.PrefixItems)

	typeCheck, expectedType := unevalItemsTypeCheck(schema)
	if typeCheck == "" {
		return // Complex schema — skip validation.
	}

	g.imports["fmt"] = true
	g.imports["github.com/mkusaka/openapigo"] = true

	fmt.Fprintf(w, "// Validate checks that unevaluated items match the required type.\n")
	fmt.Fprintf(w, "func (v %s) Validate() error {\n", typeName)
	fmt.Fprintf(w, "\tvar errs openapigo.ValidationErrors\n")
	fmt.Fprintf(w, "\tfor i := %d; i < len(v); i++ {\n", prefixLen)
	fmt.Fprintf(w, "%s", typeCheck)
	fmt.Fprintf(w, "\t}\n")
	fmt.Fprintf(w, "\tif len(errs) > 0 {\n\t\treturn errs\n\t}\n")
	fmt.Fprintf(w, "\treturn nil\n}\n\n")
	_ = expectedType // used for documentation purposes only
}

// unevalItemsTypeCheck returns the Go type assertion code for validating
// unevaluatedItems against a simple schema type. Returns ("", "") for
// unsupported/complex schemas.
func unevalItemsTypeCheck(schema *spec.Schema) (string, string) {
	switch schema.Type {
	case "string":
		return "\t\tif _, ok := v[i].(string); !ok {\n" +
			"\t\t\terrs = append(errs, openapigo.ValidationError{Field: fmt.Sprintf(\"[%d]\", i), Constraint: \"unevaluatedItems\", Message: fmt.Sprintf(\"item %d: expected string\", i)})\n" +
			"\t\t}\n", "string"
	case "number":
		return "\t\tif _, ok := v[i].(float64); !ok {\n" +
			"\t\t\terrs = append(errs, openapigo.ValidationError{Field: fmt.Sprintf(\"[%d]\", i), Constraint: \"unevaluatedItems\", Message: fmt.Sprintf(\"item %d: expected number\", i)})\n" +
			"\t\t}\n", "number"
	case "integer":
		return "\t\tif f, ok := v[i].(float64); !ok || f != float64(int64(f)) {\n" +
			"\t\t\terrs = append(errs, openapigo.ValidationError{Field: fmt.Sprintf(\"[%d]\", i), Constraint: \"unevaluatedItems\", Message: fmt.Sprintf(\"item %d: expected integer\", i)})\n" +
			"\t\t}\n", "integer"
	case "boolean":
		return "\t\tif _, ok := v[i].(bool); !ok {\n" +
			"\t\t\terrs = append(errs, openapigo.ValidationError{Field: fmt.Sprintf(\"[%d]\", i), Constraint: \"unevaluatedItems\", Message: fmt.Sprintf(\"item %d: expected boolean\", i)})\n" +
			"\t\t}\n", "boolean"
	default:
		return "", ""
	}
}
