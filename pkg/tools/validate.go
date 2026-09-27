package tools

import (
	"fmt"
	"math"
)

// validateToolArgs validates args against the bounded JSON Schema subset used
// by runtime tools. In addition to object properties, required fields, and
// enums, it supports oneOf for operation-specific argument contracts.
func validateToolArgs(schema map[string]any, args map[string]any) error {
	if len(schema) == 0 {
		return nil
	}

	if args == nil {
		args = map[string]any{}
	}

	if err := checkRequired(schema, args); err != nil {
		return err
	}

	propsRaw, ok := schema["properties"]
	if ok {
		props, valid := propsRaw.(map[string]any)
		if valid {
			additional := allowsAdditional(schema)
			for key, val := range args {
				propSchemaRaw, known := props[key]
				if !known {
					if !additional {
						return fmt.Errorf("unexpected property %q", key)
					}
					continue
				}
				propSchema, valid := propSchemaRaw.(map[string]any)
				if !valid {
					continue
				}
				if err := checkType(key, val, propSchema); err != nil {
					return err
				}
			}
		}
	}
	return checkOneOf(schema, args)
}

func checkOneOf(schema map[string]any, args map[string]any) error {
	raw, ok := schema["oneOf"]
	if !ok {
		return nil
	}
	candidates, ok := raw.([]any)
	if !ok || len(candidates) == 0 {
		return fmt.Errorf("invalid oneOf schema")
	}
	matches := 0
	for _, candidate := range candidates {
		branch, ok := candidate.(map[string]any)
		if ok && validateToolArgs(branch, args) == nil {
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf("arguments must match exactly one oneOf schema (matched %d)", matches)
	}
	return nil
}

// checkRequired verifies that every field listed in schema["required"] is present in args.
func checkRequired(schema map[string]any, args map[string]any) error {
	reqRaw, ok := schema["required"]
	if !ok {
		return nil
	}

	var required []string

	switch r := reqRaw.(type) {
	case []string:
		required = r
	case []any:
		for _, v := range r {
			s, ok := v.(string)
			if ok {
				required = append(required, s)
			}
		}
	default:
		return nil
	}

	for _, field := range required {
		if _, present := args[field]; !present {
			return fmt.Errorf("missing required property %q", field)
		}
	}
	return nil
}

// allowsAdditional returns true when the schema explicitly sets
// "additionalProperties" to true, or when the key is absent (default: reject extras).
func allowsAdditional(schema map[string]any) bool {
	v, ok := schema["additionalProperties"]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// checkType validates that val matches the JSON Schema type declared in propSchema.
func checkType(key string, val any, propSchema map[string]any) error {
	typeRaw, ok := propSchema["type"]
	if !ok {
		return nil // no type constraint
	}
	typeName, ok := typeRaw.(string)
	if !ok {
		return nil
	}

	switch typeName {
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Errorf("property %q: expected string, got %T", key, val)
		}
	case "integer":
		switch v := val.(type) {
		case float64:
			if v != math.Trunc(v) {
				return fmt.Errorf("property %q: expected integer, got float64 with fractional part", key)
			}
		case int:
			// ok
		case int64:
			// ok
		default:
			return fmt.Errorf("property %q: expected integer, got %T", key, val)
		}
	case "number":
		switch val.(type) {
		case float64, int, int64:
			// ok
		default:
			return fmt.Errorf("property %q: expected number, got %T", key, val)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("property %q: expected boolean, got %T", key, val)
		}
	case "array":
		arr, ok := val.([]any)
		if !ok {
			return fmt.Errorf("property %q: expected array, got %T", key, val)
		}
		if err := checkArrayItems(key, arr, propSchema); err != nil {
			return err
		}
	case "object":
		obj, ok := val.(map[string]any)
		if !ok {
			return fmt.Errorf("property %q: expected object, got %T", key, val)
		}
		if err := validateToolArgs(propSchema, obj); err != nil {
			return fmt.Errorf("property %q: %w", key, err)
		}
	}

	if err := checkEnum(key, val, propSchema); err != nil {
		return err
	}

	return nil
}

// checkArrayItems validates each element of arr against the "items" sub-schema.
func checkArrayItems(key string, arr []any, propSchema map[string]any) error {
	itemsRaw, ok := propSchema["items"]
	if !ok {
		return nil
	}
	itemSchema, ok := itemsRaw.(map[string]any)
	if !ok {
		return nil
	}
	for i, elem := range arr {
		elemKey := fmt.Sprintf("%s[%d]", key, i)
		if err := checkType(elemKey, elem, itemSchema); err != nil {
			return err
		}
	}
	return nil
}

// checkEnum validates that val is one of the allowed enum values in propSchema.
func checkEnum(key string, val any, propSchema map[string]any) error {
	enumRaw, ok := propSchema["enum"]
	if !ok {
		return nil
	}

	switch ev := enumRaw.(type) {
	case []any:
		for _, allowed := range ev {
			if val == allowed {
				return nil
			}
		}
	case []string:
		s, ok := val.(string)
		if ok {
			for _, allowed := range ev {
				if s == allowed {
					return nil
				}
			}
		}
	default:
		return nil // unknown enum format, skip
	}

	return fmt.Errorf("property %q: value %v is not in enum", key, val)
}
