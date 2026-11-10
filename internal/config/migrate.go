package config

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// authAliases are the deprecated repository keys that move into auth, by
// their name inside auth.
var authAliases = map[string]string{"token_env": "token_env", "token_scheme": "token_scheme", "bot_user_id": "bot_user_id", "bot_username": "bot_username"}

// MigrateLayout rewrites a changeharbor.yaml from the deprecated spellings
// to the current layout: credential and bot keys move into auth, github.app
// becomes auth.github_app and review.auto becomes triggers. It edits the
// YAML node tree, so comments travel with their keys; the output uses block
// style with two-space indentation. It does not hoist values into defaults.
// Input without deprecated keys is returned unchanged with no changes.
// Changes are worded "repositories.app.token_env ->
// repositories.app.auth.token_env". Input that does not decode is refused.
func MigrateLayout(raw []byte) ([]byte, []string, error) {
	if _, _, e := DecodeSettings(bytes.NewReader(raw), "/"); e != nil {
		return nil, nil, e
	}
	var doc yaml.Node
	if e := yaml.Unmarshal(raw, &doc); e != nil {
		return nil, nil, e
	}
	var changes []string
	if repos := mappingValue(doc.Content[0], "repositories"); repos != nil && repos.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(repos.Content); i += 2 {
			changes = append(changes, migrateRepository("repositories."+repos.Content[i].Value, repos.Content[i+1])...)
		}
	}
	if len(changes) == 0 {
		return raw, nil, nil
	}
	blockStyle(&doc)
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if e := enc.Encode(&doc); e != nil {
		return nil, nil, e
	}
	if e := enc.Close(); e != nil {
		return nil, nil, e
	}
	s, _, e := DecodeSettings(bytes.NewReader(b.Bytes()), "/")
	if e != nil {
		return nil, nil, fmt.Errorf("migrated configuration is invalid: %w", e)
	}
	if len(s.Warnings) > 0 {
		return nil, nil, errors.New("migrated configuration still uses deprecated keys: " + strings.Join(s.Warnings, "; "))
	}
	return b.Bytes(), changes, nil
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// removeKey removes key from mapping m and returns its key and value nodes.
func removeKey(m *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			k, v := m.Content[i], m.Content[i+1]
			m.Content = slices.Delete(m.Content, i, i+2)
			return k, v
		}
	}
	return nil, nil
}

func joinComments(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + "\n" + b
}

// migrateRepository moves one repository's deprecated keys and returns the
// changes made.
func migrateRepository(p string, repo *yaml.Node) []string {
	if repo.Kind != yaml.MappingNode {
		return nil
	}
	var changes []string
	auth := mappingValue(repo, "auth")
	newAuth := auth == nil
	if newAuth {
		auth = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	authKey := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "auth"}
	placed := !newAuth
	var out []*yaml.Node
	// placeAuth puts a new auth block at the current position.
	placeAuth := func(head string) {
		if !placed {
			authKey.HeadComment = joinComments(head, authKey.HeadComment)
			out = append(out, authKey, auth)
			placed = true
		}
	}
	for i := 0; i+1 < len(repo.Content); i += 2 {
		k, v := repo.Content[i], repo.Content[i+1]
		switch {
		case authAliases[k.Value] != "":
			placeAuth("")
			auth.Content = append(auth.Content, k, v)
			changes = append(changes, fmt.Sprintf("%s.%s -> %s.auth.%s", p, k.Value, p, k.Value))
		case k.Value == "github" && mappingValue(v, "app") != nil:
			ak, av := removeKey(v, "app")
			ak.Value = "github_app"
			auth.Content = append(auth.Content, ak, av)
			changes = append(changes, fmt.Sprintf("%s.github.app -> %s.auth.github_app", p, p))
			if len(v.Content) == 0 {
				placeAuth(k.HeadComment)
				continue
			}
			out = append(out, k, v)
			placeAuth("")
		case k.Value == "review" && mappingValue(v, "auto") != nil:
			tk, tv := removeKey(v, "auto")
			tk.Value = "triggers"
			changes = append(changes, fmt.Sprintf("%s.review.auto -> %s.triggers", p, p))
			if len(v.Content) == 0 {
				tk.HeadComment = joinComments(k.HeadComment, tk.HeadComment)
				out = append(out, tk, tv)
				continue
			}
			out = append(out, k, v, tk, tv)
		default:
			out = append(out, k, v)
		}
	}
	repo.Content = out
	return changes
}

// blockStyle turns flow mappings and sequences into block style.
func blockStyle(n *yaml.Node) {
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		n.Style &^= yaml.FlowStyle
	}
	for _, c := range n.Content {
		blockStyle(c)
	}
}
