package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	accessconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/access"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type routingCaptureExecutor struct {
	codexSearchCaptureExecutor
	calls []string
	fail  map[string]bool
}

func TestClientKeyRoutingRealtimeSecretUsesCurrentIssuerPolicy(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"fixture-client"}}}
	server := newTestServerWithConfig(t, cfg)
	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/client_secrets", strings.NewReader(`{"session":{"type":"realtime","model":"gpt-realtime"}}`))
	request.Header.Set("Authorization", "Bearer fixture-client")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, request)
	if rec.Code != 200 {
		t.Fatalf("issue fixture secret: %d %s", rec.Code, rec.Body.String())
	}
	var secret struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &secret); err != nil {
		t.Fatal(err)
	}
	next := cfg.CloneForRuntime()
	next.ClientKeyRouting = []config.ClientKeyRouting{{ID: "policy", APIKey: "fixture-client", Mode: "api-keys"}}
	if _, err := accessconfig.ApplyAccessProviders(server.accessManager, cfg, next); err != nil {
		t.Fatal(err)
	}
	server.engine.GET("/routing-fixture-probe", realtimeAuthMiddleware(server.accessManager, server.codexLiveHandler), func(c *gin.Context) {
		policy, ok := config.ClientKeyRoutingFromContext(c.Request.Context())
		if !ok || policy.Mode != "api-keys" {
			c.Status(500)
			return
		}
		c.Status(204)
	})
	probe := httptest.NewRequest(http.MethodGet, "/routing-fixture-probe", nil)
	probe.Header.Set("Authorization", "Bearer "+secret.Value)
	rec = httptest.NewRecorder()
	server.engine.ServeHTTP(rec, probe)
	if rec.Code != 204 {
		t.Fatalf("ephemeral credential lost current policy: %d %s", rec.Code, rec.Body.String())
	}
}

func (e *routingCaptureExecutor) Execute(_ context.Context, a *auth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls = append(e.calls, a.ID)
	if e.fail[a.ID] {
		return coreexecutor.Response{}, &auth.Error{Code: "upstream_failed", Message: "fixture unavailable", HTTPStatus: 503}
	}
	return coreexecutor.Response{Payload: []byte(`{"id":"fixture","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)}, nil
}

func (e *routingCaptureExecutor) ExecuteStream(ctx context.Context, a *auth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	if _, err := e.Execute(ctx, a, req, opts); err != nil {
		return nil, err
	}
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"id\":\"fixture\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func TestClientKeyRoutingHTTP(t *testing.T) {
	for _, strategy := range []string{"round-robin", "fill-first", "weighted-round-robin"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", strategy, stream), func(t *testing.T) {
				cfg := &config.Config{SDKConfig: config.SDKConfig{
					APIKeys: []string{"client-a", "client-b", "client-all", "client-keys", "client-pool"},
					ClientKeyRouting: []config.ClientKeyRouting{
						{ID: "a", APIKey: "client-a", Mode: "selected", UpstreamIDs: []string{"routing-a"}},
						{ID: "b", APIKey: "client-b", Mode: "selected", UpstreamIDs: []string{"routing-b"}},
						{ID: "keys", APIKey: "client-keys", Mode: "api-keys"},
						{ID: "pool", APIKey: "client-pool", Mode: "selected", UpstreamIDs: []string{"routing-a", "routing-b"}},
					},
				}}
				cfg.Routing.Strategy = strategy
				server := newTestServerWithConfig(t, cfg)
				executor := &routingCaptureExecutor{fail: make(map[string]bool)}
				server.handlers.AuthManager.RegisterExecutor(executor)
				for _, id := range []string{"a", "b", "file"} {
					attrs := map[string]string{auth.AttributeSource: "config:codex", auth.AttributeAPIKey: "fixture-upstream", auth.AttributeRoutingID: "routing-" + id}
					metadata := map[string]any{}
					if id == "a" {
						attrs["priority"] = "10"
					}
					if id == "file" {
						attrs = map[string]string{auth.AttributeSource: "file", "priority": "100"}
						metadata["access_token"] = "fixture-oauth"
					}
					credential := &auth.Auth{ID: id, Provider: "codex", Status: auth.StatusActive, Attributes: attrs, Metadata: metadata}
					if _, err := server.handlers.AuthManager.Register(context.Background(), credential); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "routing-fixture-model", Object: "model", OwnedBy: "codex"}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				}
				request := func(client string) *httptest.ResponseRecorder {
					t.Helper()
					req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"routing-fixture-model","messages":[{"role":"user","content":"fixture"}],"stream":%v}`, stream)))
					req.Header.Set("Authorization", "Bearer "+client)
					req.Header.Set("Content-Type", "application/json")
					// A client-supplied override must never widen the trusted policy.
					req.Header.Set("X-Client-Key-Routing", "all")
					rec := httptest.NewRecorder()
					server.engine.ServeHTTP(rec, req)
					return rec
				}
				for client, want := range map[string]string{"client-a": "a", "client-b": "b", "client-all": "file"} {
					executor.calls = nil
					rec := request(client)
					if rec.Code != 200 || len(executor.calls) != 1 || executor.calls[0] != want {
						t.Fatalf("%s status=%d calls=%v body=%s", client, rec.Code, executor.calls, rec.Body.String())
					}
				}
				executor.calls = nil
				if rec := request("client-keys"); rec.Code != 200 {
					t.Fatalf("keys: %d %s", rec.Code, rec.Body.String())
				}
				for _, id := range executor.calls {
					if id == "file" {
						t.Fatal("API key mode used an auth file")
					}
				}
				executor.calls = nil
				executor.fail["a"] = true
				if rec := request("client-pool"); rec.Code != 200 || len(executor.calls) != 2 || executor.calls[0] != "a" || executor.calls[1] != "b" {
					t.Fatalf("allowed failover: status=%d calls=%v body=%s", rec.Code, executor.calls, rec.Body.String())
				}
				executor.calls = nil
				rec := request("client-a")
				if rec.Code == 200 {
					t.Fatalf("expected upstream failure, status=%d calls=%v", rec.Code, executor.calls)
				}
				for _, id := range executor.calls {
					if id != "a" {
						t.Fatalf("failover escaped allowlist: %v", executor.calls)
					}
				}
			})
		}
	}
}
