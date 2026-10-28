package synthesizer

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"testing"
	"time"
)

func TestConfigRoutingIdentitiesSurviveSynthesisAndRotation(t *testing.T) {
	for _, family := range []string{"gemini", "interactions", "codex", "claude", "vertex", "xai", "meta", "openai-compatibility"} {
		t.Run(family, func(t *testing.T) {
			parse := func(secret string) []*coreauth.Auth {
				t.Helper()
				cfg, err := config.ParseConfigBytes([]byte("api-keys:\n  " + family + ":\n    - name: fixture\n      base-url: https://fixture.invalid\n      keys:\n        - api-key: " + secret + "\n          routing-id: stable-identity\n"))
				if err != nil {
					t.Fatal(err)
				}
				auths, err := NewConfigSynthesizer().Synthesize(&SynthesisContext{Config: cfg, Now: time.Unix(1700000000, 0), IDGenerator: NewStableIDGenerator()})
				if err != nil || len(auths) != 1 {
					t.Fatalf("auths=%v err=%v", auths, err)
				}
				return auths
			}
			for _, secret := range []string{"fixture-one", "fixture-rotated"} {
				if got := parse(secret)[0].Attributes[coreauth.AttributeRoutingID]; got != "stable-identity" {
					t.Fatalf("routing ID=%q", got)
				}
			}
		})
	}
}
