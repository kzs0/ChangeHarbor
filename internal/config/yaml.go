package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

// yamlToJSON converts one YAML document into the JSON the strict decoder
// validates. It is guided by the target type so text fields keep the exact
// source text of unquoted numbers (IDs, branch "1.10"), and it rejects YAML
// features that make configuration ambiguous: nulls, anchors and aliases,
// merge keys, custom tags, duplicate keys and multiple documents. JSON is a
// subset of YAML and passes through unchanged.
func yamlToJSON(raw []byte, typ reflect.Type) ([]byte, error) {
	return yamlToJSONWith(raw, typ, nil)
}

// typeHook overrides the target type at a path: it returns the type to
// convert the node with, or an error for a node of the wrong kind. ok is
// false when the path has no override.
type typeHook func(path string, n *yaml.Node) (t reflect.Type, ok bool, err error)

func yamlToJSONWith(raw []byte, typ reflect.Type, hook typeHook) ([]byte, error) {
	d := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	if e := d.Decode(&doc); e != nil {
		if errors.Is(e, io.EOF) {
			return nil, errors.New("empty document")
		}
		return nil, e
	}
	var extra yaml.Node
	if e := d.Decode(&extra); !errors.Is(e, io.EOF) {
		return nil, errors.New("expected exactly one YAML document")
	}
	v, e := converter{hook}.convert(&doc, typ, "")
	if e != nil {
		return nil, e
	}
	return json.Marshal(v)
}

func label(path string) string {
	if path == "" {
		return "config"
	}
	return path
}

var standardTags = map[string]bool{"!!str": true, "!!int": true, "!!float": true, "!!bool": true, "!!null": true, "!!timestamp": true, "!!map": true, "!!seq": true}

type converter struct{ hook typeHook }

func (c converter) convert(n *yaml.Node, t reflect.Type, path string) (any, error) {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == rawMessage {
		t = nil
	}
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return nil, fmt.Errorf("%s: anchors and aliases are not supported (line %d)", label(path), n.Line)
	}
	if n.Tag != "" && !standardTags[n.Tag] && n.Tag != "!!merge" {
		return nil, fmt.Errorf("%s: unsupported YAML tag %s (line %d)", label(path), n.Tag, n.Line)
	}
	if c.hook != nil && n.Kind != yaml.DocumentNode {
		ht, ok, e := c.hook(path, n)
		if e != nil {
			return nil, e
		}
		if ok {
			for t = ht; t != nil && t.Kind() == reflect.Pointer; {
				t = t.Elem()
			}
		}
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) != 1 {
			return nil, errors.New("empty document")
		}
		return c.convert(n.Content[0], t, path)
	case yaml.MappingNode:
		out := map[string]any{}
		lines := map[string]int{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Tag == "!!merge" {
				return nil, fmt.Errorf("%s: merge keys are not supported (line %d)", label(path), k.Line)
			}
			if k.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("%s: keys must be text (line %d)", label(path), k.Line)
			}
			child := k.Value
			if path != "" {
				child = path + "." + k.Value
			}
			if first, dup := lines[k.Value]; dup {
				return nil, fmt.Errorf("%s: duplicate key (lines %d and %d)", child, first, k.Line)
			}
			lines[k.Value] = k.Line
			var ft reflect.Type
			if t != nil {
				switch t.Kind() {
				case reflect.Struct:
					for j := 0; j < t.NumField(); j++ {
						if strings.Split(t.Field(j).Tag.Get("json"), ",")[0] == k.Value {
							ft = t.Field(j).Type
						}
					}
				case reflect.Map:
					ft = t.Elem()
				}
			}
			cv, e := c.convert(v, ft, child)
			if e != nil {
				return nil, e
			}
			out[k.Value] = cv
		}
		return out, nil
	case yaml.SequenceNode:
		var elem reflect.Type
		if t != nil && t.Kind() == reflect.Slice {
			elem = t.Elem()
		}
		out := []any{}
		for i, item := range n.Content {
			cv, e := c.convert(item, elem, fmt.Sprintf("%s[%d]", label(path), i))
			if e != nil {
				return nil, e
			}
			out = append(out, cv)
		}
		return out, nil
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			return nil, fmt.Errorf("%s: null value (line %d); omit the key to use the default", label(path), n.Line)
		}
		if t != nil && t.Kind() == reflect.String {
			switch n.Tag {
			case "!!bool":
				return nil, fmt.Errorf("%s: must be text (line %d); quote it if you meant %q", label(path), n.Line, n.Value)
			default:
				return n.Value, nil // !!str, and numbers or dates kept as written
			}
		}
		switch n.Tag {
		case "!!int", "!!float":
			return json.Number(n.Value), nil
		case "!!bool":
			var b bool
			if e := n.Decode(&b); e != nil {
				return nil, e
			}
			return b, nil
		default:
			return n.Value, nil
		}
	}
	return nil, fmt.Errorf("%s: unsupported YAML node (line %d)", label(path), n.Line)
}

// EncodeLock writes a lock file as commented YAML.
func EncodeLock(w io.Writer, l Lock) error {
	b, e := json.Marshal(l)
	if e != nil {
		return e
	}
	var tree map[string]any
	if e = json.Unmarshal(b, &tree); e != nil {
		return e
	}
	if _, e = io.WriteString(w, "# Generated by reviewd config resolve. Commit this file; do not edit it by hand.\n"); e != nil {
		return e
	}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if e = enc.Encode(tree); e != nil {
		return e
	}
	return enc.Close()
}
