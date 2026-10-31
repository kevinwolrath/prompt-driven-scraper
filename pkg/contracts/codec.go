package contracts

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Transforms are written in configuration as either a bare name or a
// single-key object, e.g. `- decimal` or `- regex: "(\d+) kWh"`.

func (t *Transform) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		t.Name = node.Value
		t.Arg = nil
		return nil
	case yaml.MappingNode:
		if len(node.Content) != 2 {
			return fmt.Errorf("line %d: a transform object must have exactly one key (the transform name)", node.Line)
		}
		t.Name = node.Content[0].Value
		var arg any
		if err := node.Content[1].Decode(&arg); err != nil {
			return err
		}
		t.Arg = normalise(arg)
		return nil
	}
	return fmt.Errorf("line %d: a transform must be a name or a single-key object", node.Line)
}

func (t Transform) MarshalYAML() (any, error) {
	if t.Arg == nil {
		return t.Name, nil
	}
	return map[string]any{t.Name: t.Arg}, nil
}

func (t Transform) MarshalJSON() ([]byte, error) {
	if t.Arg == nil {
		return json.Marshal(t.Name)
	}
	return json.Marshal(map[string]any{t.Name: t.Arg})
}

func (t *Transform) UnmarshalJSON(data []byte) error {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return err
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		return t.UnmarshalYAML(node.Content[0])
	}
	return t.UnmarshalYAML(&node)
}

// Output fields accept either a list of field definitions or a compact
// ordered object of name -> type ("decimal?" marks an optional field).

func (f *OutputFields) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		*f = nil
		return nil
	}
	switch node.Kind {
	case yaml.SequenceNode:
		var list []FieldDefinition
		if err := decodeStrict(node, &list); err != nil {
			return err
		}
		*f = list
		return nil
	case yaml.MappingNode:
		out := make(OutputFields, 0, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			name := node.Content[i].Value
			typ := strings.TrimSpace(node.Content[i+1].Value)
			if node.Content[i+1].Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: compact output field %q must map to a type string", node.Content[i+1].Line, name)
			}
			required := true
			if strings.HasSuffix(typ, "?") {
				required = false
				typ = strings.TrimSuffix(typ, "?")
			}
			out = append(out, FieldDefinition{Name: name, Type: typ, Required: required})
		}
		*f = out
		return nil
	}
	return fmt.Errorf("line %d: output.fields must be a list or an object", node.Line)
}

func (f *OutputFields) UnmarshalJSON(data []byte) error {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return err
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		return f.UnmarshalYAML(node.Content[0])
	}
	return f.UnmarshalYAML(&node)
}

func decodeStrict(node *yaml.Node, out any) error {
	// Re-encode the node and decode it with KnownFields so unknown keys inside
	// custom-unmarshalled values are still rejected.
	b, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	return dec.Decode(out)
}

// normalise converts YAML-decoded values into JSON-compatible shapes.
func normalise(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			x[k] = normalise(val)
		}
		return x
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, val := range x {
			m[fmt.Sprint(k)] = normalise(val)
		}
		return m
	case []any:
		for i := range x {
			x[i] = normalise(x[i])
		}
		return x
	}
	return v
}
