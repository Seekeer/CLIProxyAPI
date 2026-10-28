package config

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

// ClientKeyRouting restricts one authenticated client's upstream credentials.
type ClientKeyRouting struct {
	ID          string   `yaml:"id" json:"id"`
	APIKey      string   `yaml:"api-key" json:"api-key"`
	Mode        string   `yaml:"mode" json:"mode"`
	UpstreamIDs []string `yaml:"upstream-ids,omitempty" json:"upstream-ids,omitempty"`
}

type clientKeyRoutingContextKey struct{}

// ValidateUpstreamRoutingIDs rejects ambiguous identities on direct YAML load.
func (cfg *Config) ValidateUpstreamRoutingIDs() error {
	seen := make(map[string]bool)
	check := func(entries any) error {
		items := reflect.ValueOf(entries)
		for i := 0; i < items.Len(); i++ {
			id := items.Index(i).FieldByName("RoutingID").String()
			if id == "" {
				continue
			}
			if strings.TrimSpace(id) != id || seen[id] {
				return fmt.Errorf("upstream routing IDs must be unique")
			}
			seen[id] = true
		}
		return nil
	}
	for _, entries := range []any{cfg.GeminiKey, cfg.InteractionsKey, cfg.ClaudeKey, cfg.CodexKey, cfg.XAIKey, cfg.MetaKey, cfg.VertexCompatAPIKey} {
		if err := check(entries); err != nil {
			return err
		}
	}
	for _, group := range cfg.OpenAICompatibility {
		if err := check(group.APIKeyEntries); err != nil {
			return err
		}
	}
	return nil
}

// WithClientKeyRouting snapshots a trusted, server-owned policy for a request.
func WithClientKeyRouting(ctx context.Context, policy ClientKeyRouting) context.Context {
	policy.UpstreamIDs = append([]string(nil), policy.UpstreamIDs...)
	return context.WithValue(ctx, clientKeyRoutingContextKey{}, policy)
}

func ClientKeyRoutingFromContext(ctx context.Context) (ClientKeyRouting, bool) {
	if ctx == nil {
		return ClientKeyRouting{}, false
	}
	policy, ok := ctx.Value(clientKeyRoutingContextKey{}).(ClientKeyRouting)
	return policy, ok
}

// InheritClientKeyRouting keeps authorization when a handler starts its own context.
func InheritClientKeyRouting(dst, src context.Context) context.Context {
	if policy, ok := ClientKeyRoutingFromContext(src); ok {
		return WithClientKeyRouting(dst, policy)
	}
	return dst
}

func (cfg *SDKConfig) ValidateClientKeyRouting() error {
	keys := make(map[string]bool)
	for _, key := range cfg.APIKeys {
		keys[strings.TrimSpace(key)] = true
	}
	seenKeys, seenIDs := make(map[string]bool), make(map[string]bool)
	for _, policy := range cfg.ClientKeyRouting {
		if strings.TrimSpace(policy.ID) == "" || seenIDs[policy.ID] {
			return fmt.Errorf("client key routing requires unique non-empty IDs")
		}
		if !keys[policy.APIKey] || seenKeys[policy.APIKey] {
			return fmt.Errorf("client key routing must reference a unique configured client key")
		}
		seenKeys[policy.APIKey], seenIDs[policy.ID] = true, true
		switch policy.Mode {
		case "all", "api-keys":
			if len(policy.UpstreamIDs) != 0 {
				return fmt.Errorf("upstream IDs are only allowed in selected mode")
			}
		case "selected":
			if len(policy.UpstreamIDs) == 0 {
				return fmt.Errorf("selected client key routing requires at least one upstream ID")
			}
			seen := make(map[string]bool)
			for _, id := range policy.UpstreamIDs {
				if strings.TrimSpace(id) == "" || seen[id] {
					return fmt.Errorf("selected upstream IDs must be non-empty and unique")
				}
				seen[id] = true
			}
		default:
			return fmt.Errorf("unknown client key routing mode")
		}
	}
	return nil
}
