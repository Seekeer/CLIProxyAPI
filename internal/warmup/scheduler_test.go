package warmup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func newTestScheduler(t *testing.T, dir string) (*Scheduler, *recordingExecutor, *staticAuths) {
	t.Helper()
	auths := &staticAuths{auths: []*coreauth.Auth{
		codexAuth("codex-a.json", "a@example.com"),
		codexAuth("codex-b.json", "b@example.com"),
	}}
	executor := &recordingExecutor{}
	scheduler := NewScheduler(filepath.Join(dir, "config.yaml"), auths, executor)
	return scheduler, executor, auths
}

func TestSchedulerRecordsSlotOnce(t *testing.T) {
	dir := t.TempDir()
	scheduler, executor, _ := newTestScheduler(t, dir)
	scheduler.runner.Now = func() time.Time { return nowAt(t, "2026-10-28T06:00:00") }
	warmup := baseWarmupConfig()
	slot := nowAt(t, "2026-10-28T06:00:00")

	scheduler.runSlot(context.Background(), warmup, slot)
	firstRun := len(executor.recorded())
	if firstRun != 2 {
		t.Fatalf("first run executed %d requests, want 2", firstRun)
	}

	// A second tick for the same slot, e.g. a double timer fire.
	scheduler.runSlot(context.Background(), warmup, slot)
	if got := len(executor.recorded()); got != firstRun {
		t.Fatalf("second tick executed %d extra requests, want none", got-firstRun)
	}

	last := scheduler.lastCompleted()
	if !last.Equal(slot) {
		t.Fatalf("lastCompleted = %v, want %v", last, slot)
	}
}

func TestSchedulerIdempotentAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	warmup := baseWarmupConfig()
	slot := nowAt(t, "2026-10-28T06:00:00")

	first, firstExecutor, _ := newTestScheduler(t, dir)
	first.runner.Now = func() time.Time { return nowAt(t, "2026-10-28T06:00:00") }
	first.runSlot(context.Background(), warmup, slot)
	if len(firstExecutor.recorded()) != 2 {
		t.Fatalf("first process executed %d requests, want 2", len(firstExecutor.recorded()))
	}

	// A brand new scheduler over the same state directory models a restart.
	second, secondExecutor, _ := newTestScheduler(t, dir)
	second.runner.Now = func() time.Time { return nowAt(t, "2026-10-28T07:00:00") }
	second.runSlot(context.Background(), warmup, slot)
	if got := len(secondExecutor.recorded()); got != 0 {
		t.Fatalf("restart executed %d requests, want none", got)
	}
}

func TestSchedulerRunOnStartRespectsFlag(t *testing.T) {
	dir := t.TempDir()

	t.Run("disabled does not catch up", func(t *testing.T) {
		scheduler, executor, _ := newTestScheduler(t, dir)
		scheduler.runner.Now = func() time.Time { return nowAt(t, "2026-10-28T09:00:00") }
		warmup := baseWarmupConfig()
		scheduler.warmup = warmup
		scheduler.catchUpOnStart(context.Background())
		if got := len(executor.recorded()); got != 0 {
			t.Fatalf("executed %d requests with run-on-start disabled, want none", got)
		}
	})

	t.Run("enabled catches up the missed slot", func(t *testing.T) {
		scheduler, executor, _ := newTestScheduler(t, t.TempDir())
		scheduler.runner.Now = func() time.Time { return nowAt(t, "2026-10-28T09:00:00") }
		warmup := baseWarmupConfig()
		warmup.RunOnStart = true
		scheduler.warmup = warmup
		scheduler.catchUpOnStart(context.Background())
		if got := len(executor.recorded()); got != 2 {
			t.Fatalf("executed %d requests with run-on-start enabled, want 2", got)
		}
		if last := scheduler.lastCompleted(); !last.Equal(nowAt(t, "2026-10-28T06:00:00")) {
			t.Fatalf("lastCompleted = %v, want the caught-up slot", last)
		}
	})
}

func TestSchedulerRunOnStartSkipsCompletedSlot(t *testing.T) {
	dir := t.TempDir()
	slot := nowAt(t, "2026-10-28T06:00:00")

	first, firstExecutor, _ := newTestScheduler(t, dir)
	first.runner.Now = func() time.Time { return slot }
	first.runSlot(context.Background(), baseWarmupConfig(), slot)
	if len(firstExecutor.recorded()) != 2 {
		t.Fatalf("first run executed %d requests, want 2", len(firstExecutor.recorded()))
	}

	restarted, restartedExecutor, _ := newTestScheduler(t, dir)
	restarted.runner.Now = func() time.Time { return nowAt(t, "2026-10-28T09:00:00") }
	warmup := baseWarmupConfig()
	warmup.RunOnStart = true
	restarted.warmup = warmup
	restarted.catchUpOnStart(context.Background())
	if got := len(restartedExecutor.recorded()); got != 0 {
		t.Fatalf("restart re-ran the completed slot (%d requests), want none", got)
	}
}

func TestSchedulerRunOnStartIgnoresStaleSlot(t *testing.T) {
	dir := t.TempDir()
	scheduler, executor, _ := newTestScheduler(t, dir)
	// More than runOnStartWindow after 06:00, so the catch-up must wait.
	scheduler.runner.Now = func() time.Time { return nowAt(t, "2026-10-28T20:00:00") }
	warmup := baseWarmupConfig()
	warmup.RunOnStart = true
	scheduler.warmup = warmup
	scheduler.catchUpOnStart(context.Background())
	if got := len(executor.recorded()); got != 0 {
		t.Fatalf("executed %d requests for a stale slot, want none", got)
	}
}

func TestSchedulerSkipsCoolingCredentials(t *testing.T) {
	dir := t.TempDir()
	cooling := codexAuth("codex-cool.json", "cool@example.com")
	cooling.Quota = coreauth.QuotaState{
		Exceeded:      true,
		Reason:        "quota",
		NextRecoverAt: time.Now().Add(30 * time.Minute).UTC(),
	}
	auths := &staticAuths{auths: []*coreauth.Auth{
		cooling,
		codexAuth("codex-ok.json", "ok@example.com"),
	}}
	executor := &recordingExecutor{}
	scheduler := NewScheduler(filepath.Join(dir, "config.yaml"), auths, executor)
	scheduler.runSlot(context.Background(), baseWarmupConfig(), time.Now())

	requests := executor.recorded()
	if len(requests) != 1 || requests[0].AuthID != "codex-ok.json" {
		t.Fatalf("requests = %+v, want only codex-ok.json", requests)
	}
}

func TestSchedulerApplyHotReloads(t *testing.T) {
	dir := t.TempDir()
	scheduler, _, _ := newTestScheduler(t, dir)
	scheduler.Apply(config.WarmupConfig{Enabled: false})
	if scheduler.Current().Enabled {
		t.Fatalf("Apply() did not store disabled state")
	}
	enabled := baseWarmupConfig()
	scheduler.Apply(enabled)
	current := scheduler.Current()
	if !current.Enabled || current.Model != "gpt-6-luna" || len(current.Schedules) != 1 {
		t.Fatalf("Apply() did not store new settings: %#v", current)
	}
}

func TestSchedulerDisabledLoopDoesNotExecute(t *testing.T) {
	dir := t.TempDir()
	scheduler, executor, _ := newTestScheduler(t, dir)
	scheduler.Apply(config.WarmupConfig{Enabled: false})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	// Give the loop a moment to decide it has nothing to do.
	time.Sleep(50 * time.Millisecond)
	scheduler.Stop()
	if got := len(executor.recorded()); got != 0 {
		t.Fatalf("disabled scheduler executed %d requests, want none", got)
	}
}

func TestSchedulerStartStopIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	scheduler, _, _ := newTestScheduler(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	scheduler.Start(ctx)
	scheduler.Stop()
	scheduler.Stop()
}

func TestStoreRejectsCorruptState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "warmup-state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if _, err := newStore(path); err == nil {
		t.Fatalf("newStore() accepted corrupt state")
	}
}

func TestStoreRecordSlotIsMonotonic(t *testing.T) {
	dir := t.TempDir()
	store, err := newStore(filepath.Join(dir, "warmup-state.json"))
	if err != nil {
		t.Fatalf("newStore() error = %v", err)
	}
	newer := nowAt(t, "2026-10-28T06:00:00")
	older := nowAt(t, "2026-10-27T06:00:00")
	if _, errRecord := store.RecordSlot(runRecord{Slot: newer}); errRecord != nil {
		t.Fatalf("RecordSlot(newer) error = %v", errRecord)
	}
	created, errRecord := store.RecordSlot(runRecord{Slot: older})
	if errRecord != nil {
		t.Fatalf("RecordSlot(older) error = %v", errRecord)
	}
	if created {
		t.Fatalf("RecordSlot(older) created a record, want no-op")
	}
	if last := store.LastCompleted(); !last.Equal(newer) {
		t.Fatalf("LastCompleted = %v, want %v", last, newer)
	}
}
