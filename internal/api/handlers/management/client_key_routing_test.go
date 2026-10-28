package management

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientKeyRoutingManagement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := `access: {api-keys: [client]}
api-keys:
  codex:
    - name: fixture
      base-url: https://fixture.invalid
      keys:
        - api-key: fixture-upstream-one
        - api-key: fixture-upstream-two
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.POST("/prepare", h.PrepareUpstreamRoutingKeys)
	router.PUT("/config/*path", h.ConfigV8)
	router.GET("/config/*path", h.ConfigV8)
	request := func(method, url, body string, status int) string {
		t.Helper()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, url, strings.NewReader(body)))
		if rec.Code != status {
			t.Fatalf("%s %s: %d %s", method, url, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	first := request("POST", "/prepare", "", 200)
	if strings.Contains(first, "fixture-upstream") {
		t.Fatal("catalog leaked upstream secret")
	}
	var catalog struct {
		Keys []config.UpstreamRoutingKey `json:"keys"`
	}
	if err = json.Unmarshal([]byte(first), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Keys) != 2 || catalog.Keys[0].ID == "" || catalog.Keys[0].ID == catalog.Keys[1].ID {
		t.Fatalf("invalid catalog: %s", first)
	}
	if again := request("POST", "/prepare", "", 200); again != first {
		t.Fatalf("IDs changed: %s", again)
	}
	id := catalog.Keys[0].ID
	policy := `[{"id":"policy","api-key":"client","mode":"selected","upstream-ids":["` + id + `"]}]`
	request("PUT", "/config/access/key-routing", policy, 200)
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.ClientKeyRouting) != 1 || loaded.ClientKeyRouting[0].UpstreamIDs[0] != id {
		t.Fatal("policy did not survive reload")
	}
	if loaded.CodexKey[0].RoutingID != id {
		t.Fatal("upstream ID did not survive reload")
	}
	request("PUT", "/config/access/key-routing", `[]`, 409)
	request("PUT", "/config/access/key-routing", `[{"id":"policy","api-key":"client","mode":"selected"}]`, 422)
	request("PUT", "/config/access/key-routing", `[{"id":"policy","api-key":"unknown","mode":"api-keys"}]`, 422)
	// An older client replacing unchanged upstream keys retains their identities.
	request("PUT", "/config/api-keys/codex", `[{"name":"fixture","base-url":"https://fixture.invalid","keys":[{"api-key":"fixture-upstream-two"},{"api-key":"fixture-upstream-one"}]}]`, 200)
	loaded, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CodexKey[1].RoutingID != id {
		t.Fatal("reordering transferred an identity")
	}
	// Explicit rotation retains ID; deletion leaves the allowlist fail-closed.
	request("PUT", "/config/api-keys/codex", `[{"name":"renamed","base-url":"https://fixture.invalid","keys":[{"api-key":"rotated-fixture","routing-id":"`+id+`"}]}]`, 200)
	loaded, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CodexKey[0].RoutingID != id {
		t.Fatal("rotation lost identity")
	}
	request("PUT", "/config/api-keys/codex", `[]`, 200)
	if body := request("GET", "/config/access/key-routing", "", 200); !strings.Contains(body, id) {
		t.Fatal("deletion widened policy")
	}
	request("PUT", "/config/access/key-routing", `[{"id":"policy","api-key":"client","mode":"all"}]`, 200)
}
