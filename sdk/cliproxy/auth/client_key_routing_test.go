package auth

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"testing"
)

func TestClientKeyRoutingFiltersPluginCandidates(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	for _, id := range []string{"allowed", "denied"} {
		_, err := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Attributes: map[string]string{AttributeSource: "config:codex", AttributeAPIKey: "fixture", AttributeRoutingID: id}})
		if err != nil {
			t.Fatal(err)
		}
	}
	scheduler := &fakePluginScheduler{resp: pluginapi.SchedulerPickResponse{Handled: true, AuthID: "denied"}, handled: true}
	manager.SetPluginScheduler(scheduler)
	ctx := config.WithClientKeyRouting(context.Background(), config.ClientKeyRouting{Mode: "selected", UpstreamIDs: []string{"allowed"}})
	selected, err := manager.SelectAuth(ctx, "codex", "", cliproxyexecutor.Options{})
	if err != nil || selected == nil || selected.ID != "allowed" {
		t.Fatalf("selected=%v err=%v", selected, err)
	}
	if len(scheduler.requests) != 1 || len(scheduler.requests[0].Candidates) != 1 || scheduler.requests[0].Candidates[0].ID != "allowed" {
		t.Fatalf("plugin received forbidden candidates: %#v", scheduler.requests)
	}
	// A deleted ID and an unknown mode both fail closed.
	for _, policy := range []config.ClientKeyRouting{{Mode: "selected", UpstreamIDs: []string{"deleted"}}, {Mode: "unknown"}} {
		ctx = config.WithClientKeyRouting(context.Background(), policy)
		if chosen, err := manager.SelectAuth(ctx, "codex", "", cliproxyexecutor.Options{}); err == nil || chosen != nil {
			t.Fatal("invalid or exhausted policy widened access")
		}
	}
}

func TestClientKeyRoutingHomeFailsClosed(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := config.WithClientKeyRouting(context.Background(), config.ClientKeyRouting{Mode: "api-keys"})
	_, err := manager.pickHomeDispatchSelection(ctx, "fixture", cliproxyexecutor.Options{})
	if err == nil || err.Error() == "" {
		t.Fatal("restricted Home dispatch did not fail")
	}
}

func TestClientKeyRoutingRequiresConfigAPIKey(t *testing.T) {
	policy := &config.ClientKeyRouting{Mode: "api-keys"}
	for _, candidate := range []*Auth{
		{Attributes: map[string]string{AttributeSource: "file", AttributeAPIKey: "fixture"}},
		{Attributes: map[string]string{AttributeSource: "config:codex"}, Metadata: map[string]any{"access_token": "fixture"}},
		{Attributes: map[string]string{AttributeSourceBackend: "memory", AttributeAPIKey: "fixture"}},
	} {
		if clientRoutingAllows(policy, candidate) {
			t.Fatal("source/kind filter allowed an ineligible credential")
		}
	}
}

func TestClientKeyRoutingRechecksSessionBindingAndPinnedAuth(t *testing.T) {
	manager := NewManager(nil, NewSessionAffinitySelector(&FillFirstSelector{}), nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	for _, candidate := range []*Auth{
		{ID: "file", Provider: "codex", Attributes: map[string]string{AttributeSource: "file", "priority": "100"}, Metadata: map[string]any{"access_token": "fixture"}},
		{ID: "key", Provider: "codex", Attributes: map[string]string{AttributeSource: "config:codex", AttributeAPIKey: "fixture", AttributeRoutingID: "allowed"}},
	} {
		if _, err := manager.Register(context.Background(), candidate); err != nil {
			t.Fatal(err)
		}
	}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "same-session"}}
	selected, err := manager.SelectAuth(context.Background(), "codex", "", opts)
	if err != nil || selected.ID != "file" {
		t.Fatalf("initial binding=%v err=%v", selected, err)
	}
	ctx := config.WithClientKeyRouting(context.Background(), config.ClientKeyRouting{Mode: "selected", UpstreamIDs: []string{"allowed"}})
	selected, err = manager.SelectAuth(ctx, "codex", "", opts)
	if err != nil || selected.ID != "key" {
		t.Fatalf("restricted binding=%v err=%v", selected, err)
	}
	opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = "file"
	if selected, err = manager.SelectAuth(ctx, "codex", "", opts); err == nil || selected != nil {
		t.Fatal("pin bypassed routing policy")
	}
}
