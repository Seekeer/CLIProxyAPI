package config

import (
	"strings"
	"testing"
	"time"
)

const warmupBaseConfig = `
config-version: 8
server:
  port: 8317
`

func parseWarmupConfig(t *testing.T, warmup string) (*Config, error) {
	t.Helper()
	return ParseConfigBytes([]byte(warmupBaseConfig + warmup))
}

func TestWarmupBlockDefaultsToDisabled(t *testing.T) {
	cfg, err := parseWarmupConfig(t, "")
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.Warmup.Enabled {
		t.Fatalf("warmup must default to disabled")
	}
	if !cfg.Warmup.IsZero() {
		t.Fatalf("warmup must default to the zero value, got %#v", cfg.Warmup)
	}
}

func TestWarmupParseEnabledBlock(t *testing.T) {
	cfg, err := parseWarmupConfig(t, `
warmup:
  enabled: true
  timezone: "Europe/Moscow"
  model: "gpt-6-luna"
  reasoning-effort: "low"
  prompt: "Reply with exactly: ok"
  accounts: []
  schedules:
    - "06:00"
    - "17:30"
  run-on-start: false
`)
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	w := cfg.Warmup
	if !w.Enabled {
		t.Fatalf("warmup.enabled = false, want true")
	}
	if w.Timezone != "Europe/Moscow" || w.Model != "gpt-6-luna" || w.ReasoningEffort != "low" {
		t.Fatalf("warmup scalars not parsed: %#v", w)
	}
	if len(w.Accounts) != 0 {
		t.Fatalf("warmup.accounts = %v, want empty", w.Accounts)
	}
	if len(w.Schedules) != 2 || w.Schedules[0] != "06:00" || w.Schedules[1] != "17:30" {
		t.Fatalf("warmup.schedules = %v, want [06:00 17:30]", w.Schedules)
	}
	location, errLocation := w.Location()
	if errLocation != nil {
		t.Fatalf("Location() error = %v", errLocation)
	}
	if location.String() != "Europe/Moscow" {
		t.Fatalf("Location() = %q, want Europe/Moscow", location)
	}
}

func TestWarmupValidationErrors(t *testing.T) {
	cases := []struct {
		name   string
		block  string
		expect string
	}{
		{
			name: "invalid timezone",
			block: `
warmup:
  enabled: true
  timezone: "Mars/Olympus_Mons"
  model: "gpt-6-luna"
  schedules: ["06:00"]
`,
			expect: "not a valid IANA time zone",
		},
		{
			name: "invalid time format",
			block: `
warmup:
  enabled: true
  model: "gpt-6-luna"
  schedules: ["6:00"]
`,
			expect: "must use 24-hour HH:MM",
		},
		{
			name: "out of range hour",
			block: `
warmup:
  enabled: true
  model: "gpt-6-luna"
  schedules: ["24:00"]
`,
			expect: "must use 24-hour HH:MM",
		},
		{
			name: "out of range minute",
			block: `
warmup:
  enabled: true
  model: "gpt-6-luna"
  schedules: ["06:60"]
`,
			expect: "must use 24-hour HH:MM",
		},
		{
			name: "duplicate schedules",
			block: `
warmup:
  enabled: true
  model: "gpt-6-luna"
  schedules: ["06:00", "06:00"]
`,
			expect: "duplicate time 06:00",
		},
		{
			name: "duplicate schedules via whitespace",
			block: `
warmup:
  enabled: true
  model: "gpt-6-luna"
  schedules: ["06:00", " 06:00 "]
`,
			expect: "duplicate time 06:00",
		},
		{
			name: "unpadded hour is rejected rather than silently repaired",
			block: `
warmup:
  enabled: true
  model: "gpt-6-luna"
  schedules: ["06:00", "6:00"]
`,
			expect: "must use 24-hour HH:MM",
		},
		{
			name: "missing model",
			block: `
warmup:
  enabled: true
  schedules: ["06:00"]
`,
			expect: "warmup.model is required",
		},
		{
			name: "enabled without schedules",
			block: `
warmup:
  enabled: true
  model: "gpt-6-luna"
`,
			expect: "warmup.schedules must contain at least one",
		},
		{
			name:   "blank account entry",
			block:  "warmup:\n  accounts: [\"\"]\n",
			expect: "warmup.accounts entry 0 is empty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseWarmupConfig(t, tc.block)
			if err == nil {
				t.Fatalf("ParseConfigBytes() error = nil, want %q", tc.expect)
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.expect)
			}
		})
	}
}

func TestWarmupTooManySchedules(t *testing.T) {
	entries := make([]string, 0, WarmupMaxSchedules+1)
	for hour := 0; hour <= WarmupMaxSchedules; hour++ {
		entries = append(entries, `"`+FormatWarmupClock(time.Duration(hour)*time.Hour)+`"`)
	}
	block := "warmup:\n  enabled: true\n  model: \"gpt-6-luna\"\n  schedules: [" + strings.Join(entries, ", ") + "]\n"
	_, err := parseWarmupConfig(t, block)
	if err == nil {
		t.Fatalf("ParseConfigBytes() error = nil, want schedule limit error")
	}
	if !strings.Contains(err.Error(), "at most 24 entries") {
		t.Fatalf("error = %q, want schedule limit", err.Error())
	}
}

func TestWarmupAcceptsMaximumSchedules(t *testing.T) {
	entries := make([]string, 0, WarmupMaxSchedules)
	for hour := 0; hour < WarmupMaxSchedules; hour++ {
		entries = append(entries, `"`+FormatWarmupClock(time.Duration(hour)*time.Hour)+`"`)
	}
	block := "warmup:\n  enabled: true\n  model: \"gpt-6-luna\"\n  schedules: [" + strings.Join(entries, ", ") + "]\n"
	cfg, err := parseWarmupConfig(t, block)
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.Warmup.Schedules) != WarmupMaxSchedules {
		t.Fatalf("len(schedules) = %d, want %d", len(cfg.Warmup.Schedules), WarmupMaxSchedules)
	}
}

// A staged block must be storable while disabled so an operator can prepare the
// schedule first and flip enabled last.
func TestWarmupDisabledBlockStillValidatesContents(t *testing.T) {
	if _, err := parseWarmupConfig(t, "warmup:\n  enabled: false\n  schedules: [\"06:00\"]\n  timezone: Europe/Moscow\n"); err != nil {
		t.Fatalf("ParseConfigBytes() error = %v, want disabled staged block accepted", err)
	}
	if _, err := parseWarmupConfig(t, "warmup:\n  enabled: false\n  schedules: [\"06:00\", \"06:00\"]\n"); err == nil {
		t.Fatalf("disabled block with duplicate schedules must still be rejected")
	}
}

func TestWarmupDefaultsAndNormalization(t *testing.T) {
	w := NormalizedWarmupConfig(WarmupConfig{
		Model:     "  gpt-6-luna  ",
		Accounts:  []string{" a.json ", "a.json", "", "b.json"},
		Schedules: []string{" 6:00 ", "17:30"},
	})
	if w.Model != "gpt-6-luna" {
		t.Fatalf("model = %q, want trimmed", w.Model)
	}
	if len(w.Accounts) != 2 || w.Accounts[0] != "a.json" || w.Accounts[1] != "b.json" {
		t.Fatalf("accounts = %v, want [a.json b.json]", w.Accounts)
	}
	if len(w.Schedules) != 2 || w.Schedules[0] != "06:00" || w.Schedules[1] != "17:30" {
		t.Fatalf("schedules = %v, want canonical [06:00 17:30]", w.Schedules)
	}
	if got := w.EffectivePrompt(); got != WarmupDefaultPrompt {
		t.Fatalf("EffectivePrompt() = %q, want default", got)
	}
	if got := w.EffectiveReasoningEffort(); got != WarmupDefaultReasoningEffort {
		t.Fatalf("EffectiveReasoningEffort() = %q, want default", got)
	}
}

func TestWarmupLocationDefaultsToUTC(t *testing.T) {
	location, err := WarmupConfig{}.Location()
	if err != nil {
		t.Fatalf("Location() error = %v", err)
	}
	if location != time.UTC {
		t.Fatalf("Location() = %v, want UTC", location)
	}
}

func TestWarmupScheduleTimesSorted(t *testing.T) {
	times, err := WarmupConfig{Schedules: []string{"23:59", "00:00", "12:30"}}.ScheduleTimes()
	if err != nil {
		t.Fatalf("ScheduleTimes() error = %v", err)
	}
	want := []time.Duration{0, 12*time.Hour + 30*time.Minute, 23*time.Hour + 59*time.Minute}
	if len(times) != len(want) {
		t.Fatalf("len(times) = %d, want %d", len(times), len(want))
	}
	for i := range want {
		if times[i] != want[i] {
			t.Fatalf("times[%d] = %v, want %v", i, times[i], want[i])
		}
	}
}

// The warmup block is a top-level v8 section that is not rewritten on save; it
// must survive a normalization round trip unchanged.
func TestWarmupSurvivesV8Normalization(t *testing.T) {
	raw := warmupBaseConfig + `
warmup:
  enabled: true
  timezone: "Europe/Moscow"
  model: "gpt-6-luna"
  reasoning-effort: "low"
  accounts: ["codex-a.json"]
  schedules: ["06:00"]
  run-on-start: true
`
	normalized, _, err := NormalizeConfigLayout([]byte(raw), true)
	if err != nil {
		t.Fatalf("NormalizeConfigLayout() error = %v", err)
	}
	cfg, err := ParseConfigBytes(normalized)
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if !cfg.Warmup.Enabled || cfg.Warmup.Model != "gpt-6-luna" || !cfg.Warmup.RunOnStart {
		t.Fatalf("warmup lost during normalization: %#v", cfg.Warmup)
	}
	if errValidate := ValidateV8Config(normalized); errValidate != nil {
		t.Fatalf("ValidateV8Config() error = %v", errValidate)
	}
	if !strings.Contains(string(normalized), "warmup:") {
		t.Fatalf("normalized config dropped the warmup section:\n%s", normalized)
	}
}

func TestWarmupCloneIsIndependent(t *testing.T) {
	cfg := &Config{Warmup: WarmupConfig{Enabled: true, Schedules: []string{"06:00"}, Accounts: []string{"a.json"}}}
	cloned := cfg.CloneForRuntime()
	cloned.Warmup.Schedules[0] = "07:00"
	cloned.Warmup.Accounts[0] = "b.json"
	if cfg.Warmup.Schedules[0] != "06:00" || cfg.Warmup.Accounts[0] != "a.json" {
		t.Fatalf("CloneForRuntime() shared warmup slices with the source")
	}
}
