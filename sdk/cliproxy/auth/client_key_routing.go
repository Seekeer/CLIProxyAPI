package auth

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const AttributeRoutingID = "routing_id"

// ClientKeyRoutingAllows also guards credentials reused by long-lived protocols.
func ClientKeyRoutingAllows(ctx context.Context, auth *Auth) bool {
	policy, ok := config.ClientKeyRoutingFromContext(ctx)
	if !ok {
		return true
	}
	return clientRoutingAllows(&policy, auth)
}

func clientRoutingAllows(policy *config.ClientKeyRouting, auth *Auth) bool {
	if policy == nil || policy.Mode == "all" {
		return true
	}
	if auth == nil || auth.AuthSourceKind() != AuthSourceConfig || auth.AuthKind() != AuthKindAPIKey {
		return false
	}
	if policy.Mode == "api-keys" {
		return true
	}
	if policy.Mode != "selected" {
		return false
	}
	id := auth.Attributes[AttributeRoutingID]
	if id == "" {
		return false
	}
	for _, allowed := range policy.UpstreamIDs {
		if allowed == id {
			return true
		}
	}
	return false
}
