package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	// Embed the IANA database so time zone validation and schedule resolution do
	// not depend on the host image shipping zoneinfo.
	_ "time/tzdata"
)

// Warmup limits mirror the documented contract: an everyday schedule list of at
// most one entry per hour, in 24-hour HH:MM form.
const (
	WarmupMaxSchedules           = 24
	WarmupDefaultPrompt          = "Reply with exactly: ok"
	WarmupDefaultReasoningEffort = "low"
	warmupClockFormat            = "15:04"
)

// WarmupConfig schedules a small, account-pinned request per selected Codex
// credential so the five-hour window starts earlier than the first real use.
// The block is inert unless Enabled is true.
type WarmupConfig struct {
	// Enabled turns the scheduler on. Everything else is ignored while false.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Timezone is an IANA zone name used to interpret Schedules (default UTC).
	Timezone string `yaml:"timezone" json:"timezone"`
	// Model is the model slug sent by the warmup request.
	Model string `yaml:"model" json:"model"`
	// ReasoningEffort is forwarded as the request's reasoning effort.
	ReasoningEffort string `yaml:"reasoning-effort" json:"reasoning-effort"`
	// Prompt is the short user message used for the warmup turn.
	Prompt string `yaml:"prompt" json:"prompt"`
	// Accounts lists auth file names to warm. Empty means every active Codex
	// credential.
	Accounts []string `yaml:"accounts" json:"accounts"`
	// Schedules holds daily local trigger times in HH:MM (24-hour) form.
	Schedules []string `yaml:"schedules" json:"schedules"`
	// RunOnStart performs a catch-up run when a slot was missed while the
	// process was down. It never re-runs an already completed slot.
	RunOnStart bool `yaml:"run-on-start" json:"run-on-start"`
}

var warmupClockPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)
var looseWarmupClockPattern = regexp.MustCompile(`^([0-9]{1,2}):([0-9]{1,2})$`)

// IsZero reports whether the block carries no settings at all.
func (w WarmupConfig) IsZero() bool {
	return !w.Enabled && w.Timezone == "" && w.Model == "" && w.ReasoningEffort == "" &&
		w.Prompt == "" && len(w.Accounts) == 0 && len(w.Schedules) == 0 && !w.RunOnStart
}

// EffectivePrompt returns the configured prompt or the built-in default.
func (w WarmupConfig) EffectivePrompt() string {
	if prompt := strings.TrimSpace(w.Prompt); prompt != "" {
		return prompt
	}
	return WarmupDefaultPrompt
}

// EffectiveReasoningEffort returns the configured effort or the low default.
func (w WarmupConfig) EffectiveReasoningEffort() string {
	if effort := strings.TrimSpace(w.ReasoningEffort); effort != "" {
		return effort
	}
	return WarmupDefaultReasoningEffort
}

// Location resolves the configured time zone. An empty Timezone means UTC, and
// "Local" is accepted so operators can opt into the host zone explicitly.
func (w WarmupConfig) Location() (*time.Location, error) {
	name := strings.TrimSpace(w.Timezone)
	switch name {
	case "":
		return time.UTC, nil
	case "Local":
		return time.Local, nil
	}
	location, errParse := time.LoadLocation(name)
	if errParse != nil {
		return nil, fmt.Errorf("warmup.timezone %q is not a valid IANA time zone", name)
	}
	return location, nil
}

// ScheduleTimes parses Schedules into wall-clock offsets from midnight, sorted
// ascending. Entries must already be validated by Validate.
func (w WarmupConfig) ScheduleTimes() ([]time.Duration, error) {
	times := make([]time.Duration, 0, len(w.Schedules))
	for _, raw := range w.Schedules {
		parsed, errParse := ParseWarmupClock(raw)
		if errParse != nil {
			return nil, errParse
		}
		times = append(times, parsed)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times, nil
}

// ParseWarmupClock parses a single HH:MM value into an offset from midnight.
func ParseWarmupClock(raw string) (time.Duration, error) {
	value := strings.TrimSpace(raw)
	match := warmupClockPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, fmt.Errorf("warmup.schedules entry %q must use 24-hour HH:MM", raw)
	}
	hour := int(match[1][0]-'0')*10 + int(match[1][1]-'0')
	minute := int(match[2][0]-'0')*10 + int(match[2][1]-'0')
	return time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute, nil
}

// FormatWarmupClock renders an offset from midnight back to HH:MM.
func FormatWarmupClock(offset time.Duration) string {
	minutes := int(offset / time.Minute)
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

// parseLooseWarmupClock is NormalizedWarmupConfig's tolerant reader. It accepts
// unpadded hours such as "6:00" so an operator's near miss is repaired into the
// canonical HH:MM form instead of being stored as an unparsable entry.
func parseLooseWarmupClock(raw string) (time.Duration, error) {
	value := strings.TrimSpace(raw)
	if offset, errStrict := ParseWarmupClock(value); errStrict == nil {
		return offset, nil
	}
	match := looseWarmupClockPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, fmt.Errorf("warmup.schedules entry %q must use 24-hour HH:MM", raw)
	}
	hour, minute := 0, 0
	for _, digit := range match[1] {
		hour = hour*10 + int(digit-'0')
	}
	for _, digit := range match[2] {
		minute = minute*10 + int(digit-'0')
	}
	if hour > 23 || minute > 59 {
		return 0, fmt.Errorf("warmup.schedules entry %q must use 24-hour HH:MM", raw)
	}
	return time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute, nil
}

// Validate enforces the warmup contract. A disabled block is accepted as long
// as whatever it does declare is still well formed, so an operator can stage a
// schedule before enabling it. An enabled block must be complete.
func (w WarmupConfig) Validate() error {
	if len(w.Schedules) > WarmupMaxSchedules {
		return fmt.Errorf("warmup.schedules accepts at most %d entries, got %d", WarmupMaxSchedules, len(w.Schedules))
	}
	seen := make(map[time.Duration]string, len(w.Schedules))
	for _, raw := range w.Schedules {
		offset, errParse := ParseWarmupClock(raw)
		if errParse != nil {
			return errParse
		}
		canonical := FormatWarmupClock(offset)
		if previous, duplicate := seen[offset]; duplicate {
			return fmt.Errorf("warmup.schedules contains duplicate time %s (also written as %q)", canonical, previous)
		}
		seen[offset] = raw
	}
	if timezone := strings.TrimSpace(w.Timezone); timezone != "" {
		if _, errLocation := w.Location(); errLocation != nil {
			return errLocation
		}
	}
	for i, account := range w.Accounts {
		if strings.TrimSpace(account) == "" {
			return fmt.Errorf("warmup.accounts entry %d is empty", i)
		}
	}
	if !w.Enabled {
		return nil
	}
	if strings.TrimSpace(w.Model) == "" {
		return fmt.Errorf("warmup.model is required when warmup.enabled is true")
	}
	if len(w.Schedules) == 0 {
		return fmt.Errorf("warmup.schedules must contain at least one HH:MM entry when warmup.enabled is true")
	}
	return nil
}

// NormalizedWarmupConfig trims free-form values and keeps accounts de-duplicated
// in their original order.
func NormalizedWarmupConfig(w WarmupConfig) WarmupConfig {
	w.Timezone = strings.TrimSpace(w.Timezone)
	w.Model = strings.TrimSpace(w.Model)
	w.ReasoningEffort = strings.TrimSpace(w.ReasoningEffort)
	w.Prompt = strings.TrimSpace(w.Prompt)
	if len(w.Schedules) > 0 {
		schedules := make([]string, 0, len(w.Schedules))
		for _, raw := range w.Schedules {
			value := strings.TrimSpace(raw)
			if offset, errParse := parseLooseWarmupClock(value); errParse == nil {
				// Canonical spelling keeps idempotency keys stable across restarts.
				value = FormatWarmupClock(offset)
			}
			schedules = append(schedules, value)
		}
		w.Schedules = schedules
	}
	if len(w.Accounts) > 0 {
		accounts := make([]string, 0, len(w.Accounts))
		seen := make(map[string]bool, len(w.Accounts))
		for _, raw := range w.Accounts {
			value := strings.TrimSpace(raw)
			if value == "" || seen[value] {
				continue
			}
			seen[value] = true
			accounts = append(accounts, value)
		}
		w.Accounts = accounts
	}
	return w
}
