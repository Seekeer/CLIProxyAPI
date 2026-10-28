package warmup

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

// maxSleep chunks the wait between ticks so wall-clock jumps (NTP corrections,
// laptop suspend, container host time changes) cannot silently skip a slot.
const maxSleep = 30 * time.Second

// runOnStartWindow is how far back a missed slot is still caught up at startup
// when run-on-start is enabled. A longer outage waits for the next slot instead
// of firing an unbounded number of hours late, which would shift the user's
// window far from its intended start.
const runOnStartWindow = 12 * time.Hour

// LogPath is where the idempotency state lives, next to the configuration.
const stateFileName = "warmup-state.json"

// Scheduler owns the warmup lifecycle: it re-reads its configuration on every
// change, wakes at the next scheduled slot, and records completed slots so a
// restart never repeats a warmup.
type Scheduler struct {
	mu       sync.Mutex
	warmup   config.WarmupConfig
	runner   *Runner
	store    *store
	stateDir string

	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	wake    chan struct{}
	running bool
}

// NewScheduler builds a scheduler that resolves state relative to configPath.
func NewScheduler(configPath string, auths AuthLister, executor Executor) *Scheduler {
	return &Scheduler{
		warmup:   config.WarmupConfig{},
		runner:   &Runner{Auths: auths, Executor: executor},
		stateDir: resolveStateDir(configPath),
		wake:     make(chan struct{}, 1),
	}
}

// resolveStateDir keeps warmup state beside the active configuration so it
// travels with the mounted volume and survives container replacement.
func resolveStateDir(configPath string) string {
	if dir := filepath.Dir(configPath); dir != "" && dir != "." {
		return dir
	}
	if writable := util.WritablePath(); writable != "" {
		return writable
	}
	return "."
}

// Start launches the scheduling loop. It is safe to call more than once; a
// stopped scheduler can be started again.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.started = true
	s.cancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()
	go s.loop(runCtx)
}

// Stop ends the scheduling loop and waits for any in-flight run to finish.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.started = false
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// Apply installs new settings from a config load or reload and wakes the loop.
func (s *Scheduler) Apply(warmup config.WarmupConfig) {
	warmup = config.NormalizedWarmupConfig(warmup)
	s.mu.Lock()
	previous := s.warmup
	s.warmup = warmup
	s.mu.Unlock()
	if warmup.Enabled != previous.Enabled || warmup.Model != previous.Model || len(warmup.Schedules) != len(previous.Schedules) {
		log.WithFields(log.Fields{
			"enabled":   warmup.Enabled,
			"model":     warmup.Model,
			"timezone":  warmup.Timezone,
			"schedules": warmup.Schedules,
		}).Info("warmup: configuration applied")
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Current returns the active settings.
func (s *Scheduler) Current() config.WarmupConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.warmup
}

func (s *Scheduler) loop(ctx context.Context) {
	defer s.wg.Done()
	// A fresh process is the only place run-on-start can apply.
	s.catchUpOnStart(ctx)
	for {
		if ctx.Err() != nil {
			return
		}
		warmup := s.Current()
		if !warmup.Enabled {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			}
		}
		now := s.runner.now()
		next, errNext := NextSlot(now, warmup)
		if errNext != nil {
			log.WithError(errNext).Warn("warmup: cannot resolve next slot")
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			case <-time.After(time.Minute):
				continue
			}
		}
		wait := next.Sub(now)
		if wait <= 0 {
			wait = time.Second
		}
		if wait > maxSleep {
			wait = maxSleep
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
			continue
		case <-timer.C:
		}
		// Re-evaluate rather than trusting the sleep: the slot may not be due
		// yet, or another tick may already have handled it.
		s.tick(ctx, next)
	}
}

// tick runs the slot when it is due and not yet completed.
func (s *Scheduler) tick(ctx context.Context, slot time.Time) {
	now := s.runner.now()
	if now.Before(slot) {
		return
	}
	warmup := s.Current()
	if !warmup.Enabled {
		return
	}
	s.runSlot(ctx, warmup, slot)
}

// catchUpOnStart implements run-on-start: if the process was down when a slot
// came due, warm once on boot. Idempotency state still wins, so a restart after
// a completed run stays quiet.
func (s *Scheduler) catchUpOnStart(ctx context.Context) {
	warmup := s.Current()
	if !warmup.Enabled || !warmup.RunOnStart {
		return
	}
	now := s.runner.now()
	missed, errMissed := LatestMissedSlot(now, warmup, s.lastCompleted())
	if errMissed != nil {
		log.WithError(errMissed).Warn("warmup: cannot evaluate missed slot")
		return
	}
	if missed.IsZero() || now.Sub(missed) > runOnStartWindow {
		return
	}
	log.WithField("slot", missed.Format(time.RFC3339)).Info("warmup: catching up a slot missed while stopped")
	s.runSlot(ctx, warmup, missed)
}

// runSlot performs one warmup and persists the outcome. Only one slot runs at a
// time; a concurrent tick observes the claim and returns.
func (s *Scheduler) runSlot(ctx context.Context, warmup config.WarmupConfig, slot time.Time) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		log.WithField("slot", slot.Format(time.RFC3339)).Info("warmup: previous run still in progress, skipping")
		return
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	store, errStore := s.stateStore()
	if errStore != nil {
		// Without durable state a restart could repeat this warmup. Refuse the
		// run rather than risk a duplicate.
		log.WithError(errStore).Error("warmup: state is unavailable, slot skipped")
		return
	}
	completed := store.LastCompleted()
	if !completed.IsZero() && !slot.After(completed) {
		log.WithFields(log.Fields{
			"slot":      slot.Format(time.RFC3339),
			"completed": completed.Format(time.RFC3339),
		}).Info("warmup: slot already completed, skipping")
		return
	}

	results := s.runner.Run(ctx, warmup, slot)
	record := runRecord{
		Slot:        slot,
		CompletedAt: s.runner.now(),
		Model:       warmup.Model,
		Results:     results,
	}
	// A cancelled run did not complete; leave the slot unclaimed so the next
	// startup can retry it.
	if ctx.Err() != nil {
		log.WithField("slot", slot.Format(time.RFC3339)).Warn("warmup: run interrupted, slot not recorded")
		return
	}
	if _, errRecord := store.RecordSlot(record); errRecord != nil {
		log.WithError(errRecord).Error("warmup: failed to persist slot state")
	}

	succeeded, attempted := summarise(results)
	log.WithField("slot", slot.Format(time.RFC3339)).
		Infof("warmup: slot finished (succeeded=%d attempted=%d)", succeeded, attempted)
}

func (s *Scheduler) stateStore() (*store, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil {
		return s.store, nil
	}
	path := filepath.Join(s.stateDir, stateFileName)
	created, errCreate := newStore(path)
	if errCreate != nil {
		return nil, errCreate
	}
	s.store = created
	return s.store, nil
}

func (s *Scheduler) lastCompleted() time.Time {
	created, errStore := s.stateStore()
	if errStore != nil {
		return time.Time{}
	}
	return created.LastCompleted()
}

func summarise(results []CredentialResult) (succeeded, attempted int) {
	for _, result := range results {
		switch result.Status {
		case StatusSuccess, StatusEmpty:
			attempted++
			succeeded++
		case StatusFailed:
			attempted++
		}
	}
	return succeeded, attempted
}
