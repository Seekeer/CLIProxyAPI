package warmup

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestLatestMissedSlotHitsConfiguredHour(t *testing.T) {
	warmup := baseWarmupConfig()
	now := nowAt(t, "2026-10-28T10:00:00")
	slot, err := LatestMissedSlot(now, warmup, time.Time{})
	if err != nil {
		t.Fatalf("LatestMissedSlot() error = %v", err)
	}
	want := nowAt(t, "2026-10-28T06:00:00")
	if !slot.Equal(want) {
		t.Fatalf("slot = %v, want %v", slot, want)
	}
}

func TestLatestMissedSlotHonoursTimezone(t *testing.T) {
	warmup := baseWarmupConfig()
	// 03:30 UTC is 06:30 Moscow, so the 06:00 Moscow slot is already due.
	utcNow := time.Date(2026, 10, 28, 3, 30, 0, 0, time.UTC)
	slot, err := LatestMissedSlot(utcNow, warmup, time.Time{})
	if err != nil {
		t.Fatalf("LatestMissedSlot() error = %v", err)
	}
	if !slot.Equal(nowAt(t, "2026-10-28T06:00:00")) {
		t.Fatalf("slot = %v, want the 06:00 Moscow slot", slot)
	}
}

func TestLatestMissedSlotBeforeFirstSlotIsZero(t *testing.T) {
	warmup := baseWarmupConfig()
	// 05:59 on the 28th: today's slot has not arrived, and yesterday's is
	// outside the two-day look-back only after that grace; it still counts.
	slot, err := LatestMissedSlot(nowAt(t, "2026-10-28T05:59:00"), warmup, time.Time{})
	if err != nil {
		t.Fatalf("LatestMissedSlot() error = %v", err)
	}
	if !slot.Equal(nowAt(t, "2026-10-27T06:00:00")) {
		t.Fatalf("slot = %v, want the previous day's slot", slot)
	}
}

func TestLatestMissedSlotSkipsCompletedSlot(t *testing.T) {
	warmup := baseWarmupConfig()
	completed := nowAt(t, "2026-10-28T06:00:00")
	slot, err := LatestMissedSlot(nowAt(t, "2026-10-28T10:00:00"), warmup, completed)
	if err != nil {
		t.Fatalf("LatestMissedSlot() error = %v", err)
	}
	if !slot.IsZero() {
		t.Fatalf("slot = %v, want zero once the slot is completed", slot)
	}
}

func TestLatestMissedSlotPicksNewestOfSeveralSchedules(t *testing.T) {
	warmup := baseWarmupConfig()
	warmup.Schedules = []string{"06:00", "12:00", "17:30"}
	slot, err := LatestMissedSlot(nowAt(t, "2026-10-28T13:00:00"), warmup, nowAt(t, "2026-10-28T06:00:00"))
	if err != nil {
		t.Fatalf("LatestMissedSlot() error = %v", err)
	}
	if !slot.Equal(nowAt(t, "2026-10-28T12:00:00")) {
		t.Fatalf("slot = %v, want 12:00", slot)
	}
}

func TestNextSlotIsStrictlyFuture(t *testing.T) {
	warmup := baseWarmupConfig()
	warmup.Schedules = []string{"06:00", "18:00"}
	atSlot, err := NextSlot(nowAt(t, "2026-10-28T06:00:00"), warmup)
	if err != nil {
		t.Fatalf("NextSlot() error = %v", err)
	}
	if !atSlot.Equal(nowAt(t, "2026-10-28T18:00:00")) {
		t.Fatalf("next = %v, want 18:00 (next slot after the current one)", atSlot)
	}
	afterLast, err := NextSlot(nowAt(t, "2026-10-28T19:00:00"), warmup)
	if err != nil {
		t.Fatalf("NextSlot() error = %v", err)
	}
	if !afterLast.Equal(nowAt(t, "2026-10-29T06:00:00")) {
		t.Fatalf("next = %v, want next day 06:00", afterLast)
	}
}

func TestScheduleSurvivesDaylightSaving(t *testing.T) {
	warmup := config.WarmupConfig{Timezone: "Europe/Berlin", Schedules: []string{"02:30"}}
	// 2026-03-29 is the spring-forward date in Europe/Berlin; 02:30 does not
	// exist locally and must resolve instead of failing or looping.
	location, err := warmup.Location()
	if err != nil {
		t.Fatalf("Location() error = %v", err)
	}
	now := time.Date(2026, 3, 29, 0, 0, 0, 0, location)
	next, errNext := NextSlot(now, warmup)
	if errNext != nil {
		t.Fatalf("NextSlot() error = %v", errNext)
	}
	if !next.After(now) {
		t.Fatalf("next = %v, want an instant after %v", next, now)
	}
	// The fall-back day must not produce two firings for the same wall clock.
	fallNow := time.Date(2026, 10, 25, 0, 0, 0, 0, location)
	first, errFirst := NextSlot(fallNow, warmup)
	if errFirst != nil {
		t.Fatalf("NextSlot() error = %v", errFirst)
	}
	second, errSecond := NextSlot(first, warmup)
	if errSecond != nil {
		t.Fatalf("NextSlot() error = %v", errSecond)
	}
	if first.YearDay() == second.YearDay() {
		t.Fatalf("fall-back day produced two slots in one day: %v and %v", first, second)
	}
}
