package cliproxy

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/warmup"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

// startWarmup creates the warmup scheduler once the HTTP server exists, so the
// scheduler can reuse the server's request handler instead of reaching upstream
// directly. It applies the current settings and starts the scheduling loop.
func (s *Service) startWarmup(ctx context.Context) {
	if s == nil {
		return
	}
	var executor warmup.Executor
	if s.server != nil {
		executor = s.server.RequestExecutor()
	}
	if executor == nil {
		return
	}
	scheduler := warmup.NewScheduler(s.configPath, s.coreManager, executor)
	s.warmupMu.Lock()
	s.warmup = scheduler
	s.warmupMu.Unlock()

	enabled := false
	if cfg := s.currentConfig(); cfg != nil {
		enabled = cfg.Warmup.Enabled
		scheduler.Apply(cfg.Warmup)
	}
	log.WithField("enabled", enabled).Info("warmup scheduler started")
	scheduler.Start(ctx)
}

// applyWarmupConfig forwards configuration changes to a running scheduler.
func (s *Service) applyWarmupConfig(cfg *config.Config) {
	if s == nil || cfg == nil {
		return
	}
	s.warmupMu.Lock()
	scheduler := s.warmup
	s.warmupMu.Unlock()
	if scheduler == nil {
		return
	}
	scheduler.Apply(cfg.Warmup)
}

// stopWarmup ends the scheduling loop during shutdown.
func (s *Service) stopWarmup() {
	if s == nil {
		return
	}
	s.warmupMu.Lock()
	scheduler := s.warmup
	s.warmup = nil
	s.warmupMu.Unlock()
	if scheduler != nil {
		scheduler.Stop()
	}
}
