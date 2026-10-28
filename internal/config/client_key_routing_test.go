package config

import "testing"

func TestClientKeyRoutingConfigValidation(t *testing.T) {
	for _, entry := range []string{
		`{id: rule, api-key: client, mode: selected, upstream-ids: []}`,
		`{id: rule, api-key: client, mode: future}`,
		`{id: rule, api-key: missing, mode: api-keys}`,
		`{id: rule, api-key: client, mode: all, upstream-ids: [one]}`,
		`{id: rule, api-key: client, mode: selected, upstream-ids: [one, one]}`,
	} {
		if _, err := ParseConfigBytes([]byte("access:\n  api-keys: [client]\n  key-routing: [" + entry + "]\n")); err == nil {
			t.Errorf("accepted invalid routing: %s", entry)
		}
	}
	if _, err := ParseConfigBytes([]byte("api-keys:\n  codex:\n    - base-url: https://fixture.invalid\n      keys:\n        - {api-key: one, routing-id: duplicate}\n        - {api-key: two, routing-id: duplicate}\n")); err == nil {
		t.Fatal("accepted duplicate upstream identities")
	}
	if cfg, err := ParseConfigBytes([]byte("access: {api-keys: [client]}\n")); err != nil || len(cfg.ClientKeyRouting) != 0 {
		t.Fatalf("legacy default: cfg=%v err=%v", cfg, err)
	}
}
