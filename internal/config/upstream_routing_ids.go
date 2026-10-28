package config

import (
	"fmt"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
	"strings"
)

type UpstreamRoutingKey struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	Name      string `json:"name"`
	MaskedKey string `json:"masked-key"`
}

func visitRoutingKeys(root *yaml.Node, visit func(provider, name, base string, key *yaml.Node)) {
	for _, family := range v8KeyFamilies {
		groups := yamlPath(root, "api-keys."+family.current)
		if groups == nil || groups.Kind != yaml.SequenceNode {
			continue
		}
		for _, group := range groups.Content {
			keys := yamlPath(group, "keys")
			if keys == nil || keys.Kind != yaml.SequenceNode {
				continue
			}
			name, base := routingScalar(group, "name"), routingScalar(group, "base-url")
			for _, key := range keys.Content {
				if key.Kind == yaml.MappingNode {
					visit(family.current, name, base, key)
				}
			}
		}
	}
}

func routingScalar(node *yaml.Node, field string) string {
	if value := yamlPath(node, field); value != nil && value.Kind == yaml.ScalarNode {
		return value.Value
	}
	return ""
}

// EnsureUpstreamRoutingIDs assigns persistent opaque identities at a write boundary.
// An old editor omitting identities may retain them only for unchanged credentials.
func EnsureUpstreamRoutingIDs(root, before *yaml.Node) error {
	old := make(map[string][]string)
	signature := func(provider, name, base string, key *yaml.Node) string {
		return provider + "\x00" + name + "\x00" + base + "\x00" + routingScalar(key, "api-key")
	}
	if before != nil {
		visitRoutingKeys(before, func(p, n, b string, k *yaml.Node) {
			sig := signature(p, n, b, k)
			old[sig] = append(old[sig], routingScalar(k, "routing-id"))
		})
	}
	seen := make(map[string]bool)
	var invalid bool
	visitRoutingKeys(root, func(p, n, b string, k *yaml.Node) {
		id := routingScalar(k, "routing-id")
		if id == "" {
			sig := signature(p, n, b, k)
			if ids := old[sig]; len(ids) > 0 {
				id = ids[0]
				old[sig] = ids[1:]
			}
			if id == "" {
				id = uuid.NewString()
			}
			setYAMLPath(k, "routing-id", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: id})
		}
		if seen[id] || strings.TrimSpace(id) == "" {
			invalid = true
		}
		seen[id] = true
	})
	if invalid {
		return fmt.Errorf("upstream routing IDs must be unique and non-empty")
	}
	return nil
}

func UpstreamRoutingCatalog(root *yaml.Node) []UpstreamRoutingKey {
	result := []UpstreamRoutingKey{}
	visitRoutingKeys(root, func(p, n, b string, k *yaml.Node) {
		secret := routingScalar(k, "api-key")
		if secret == "" {
			return
		}
		masked := "••••"
		if len(secret) > 8 {
			masked += secret[len(secret)-4:]
		}
		result = append(result, UpstreamRoutingKey{ID: routingScalar(k, "routing-id"), Provider: p, Name: n, MaskedKey: masked})
	})
	return result
}
