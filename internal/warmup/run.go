package warmup

import (
	"context"
	"fmt"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	log "github.com/sirupsen/logrus"
)

// Provider is the only upstream channel the warmup targets. The five-hour
// window that motivates this feature is specific to Codex.
const Provider = "codex"

// requestTimeout bounds a single credential's warmup turn. A warmup must never
// hold the scheduler open indefinitely.
const requestTimeout = 3 * time.Minute

// CredentialStatus values recorded for one warmup attempt.
const (
	StatusSuccess = "success"
	StatusEmpty   = "empty"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// AuthLister is the narrow view of the auth manager the runner needs.
type AuthLister interface {
	List() []*coreauth.Auth
}

// Executor runs one internal model request. It is satisfied by
// *handlers.BaseAPIHandler, so the warmup uses exactly the inbound request path
// rather than a private upstream shortcut.
type Executor interface {
	ExecuteModel(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage)
}

// Runner performs one warmup for a resolved list of credentials.
type Runner struct {
	Auths    AuthLister
	Executor Executor
	// Now is overridable for tests.
	Now func() time.Time
}

// Run warms every eligible credential for the given slot. One credential's
// failure never stops the others. Results are returned in credential order.
func (r *Runner) Run(ctx context.Context, warmup config.WarmupConfig, slot time.Time) []CredentialResult {
	now := r.now()
	candidates, skipped := r.eligibleCredentials(warmup, now)
	results := make([]CredentialResult, 0, len(candidates)+len(skipped))
	results = append(results, skipped...)

	log.WithFields(log.Fields{
		"slot":  slot.Format(time.RFC3339),
		"model": warmup.Model,
	}).Infof("warmup: starting slot (eligible=%d skipped=%d)", len(candidates), len(skipped))

	for _, auth := range candidates {
		results = append(results, r.warmOne(ctx, auth, warmup, slot))
	}
	return results
}

// warmOne executes a single pinned warmup request and logs its outcome.
func (r *Runner) warmOne(ctx context.Context, auth *coreauth.Auth, warmup config.WarmupConfig, slot time.Time) CredentialResult {
	result := CredentialResult{AuthID: auth.ID, Account: accountLabel(auth), Status: StatusFailed}

	body, errBody := buildRequestBody(warmup.Model, warmup.EffectivePrompt(), warmup.EffectiveReasoningEffort())
	if errBody != nil {
		result.Message = "invalid request payload"
		r.logResult(slot, result, errBody)
		return result
	}

	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	// Pin the credential and force the provider so the request lands on this
	// exact auth while still passing through the selector's availability and
	// cooldown checks, payload overrides, inference and usage accounting.
	requestCtx = handlers.WithPinnedAuthID(requestCtx, auth.ID)

	resp, errMsg := r.Executor.ExecuteModel(requestCtx, handlers.ModelExecutionRequest{
		EntryProtocol:  "openai-response",
		ExitProtocol:   "openai-response",
		Model:          warmup.Model,
		Stream:         false,
		Body:           body,
		ForcedProvider: Provider,
		AuthID:         auth.ID,
		Path:           "/v1/responses",
	})
	if errMsg != nil {
		result.HTTPStatus = errMsg.StatusCode
		result.Message = describeError(errMsg)
		r.logResult(slot, result, nil)
		return result
	}

	usage := parseWindowUsage(resp.Headers)
	result.HTTPStatus = resp.StatusCode
	result.UsedPercent = usage.UsedPercent
	result.ResetAfterSeconds = usage.ResetAfterSeconds
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Message = fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode)
		r.logResult(slot, result, nil)
		return result
	}
	if responseHasContent(resp.Body) {
		result.Status = StatusSuccess
	} else {
		// The turn reached the upstream and consumed the window; only the
		// textual output is missing.
		result.Status = StatusEmpty
		result.Message = "request succeeded without model output"
	}
	r.logResult(slot, result, nil)
	return result
}

// eligibleCredentials resolves which credentials this run should warm.
//
// An empty Accounts list means "every active Codex credential". Disabled or
// cooling credentials are reported as skipped, never warmed, so a scheduled run
// cannot extend an existing cooldown.
func (r *Runner) eligibleCredentials(warmup config.WarmupConfig, now time.Time) ([]*coreauth.Auth, []CredentialResult) {
	var (
		candidates []*coreauth.Auth
		skipped    []CredentialResult
	)
	if r.Auths == nil {
		return nil, skipped
	}
	wanted := make(map[string]bool, len(warmup.Accounts))
	for _, name := range warmup.Accounts {
		wanted[name] = true
	}
	matched := make(map[string]bool, len(wanted))
	for _, auth := range r.Auths.List() {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), Provider) {
			continue
		}
		name := authFileName(auth)
		if len(wanted) > 0 {
			if !wanted[name] {
				continue
			}
			matched[name] = true
		}
		if reason, blocked := skipReason(auth, warmup.Model, now); blocked {
			skipped = append(skipped, CredentialResult{
				AuthID:  auth.ID,
				Account: accountLabel(auth),
				Status:  StatusSkipped,
				Message: reason,
			})
			continue
		}
		candidates = append(candidates, auth)
	}
	// Selecting a credential that does not exist is an operator error worth
	// surfacing rather than silently warming fewer accounts than configured.
	for _, name := range warmup.Accounts {
		if matched[name] {
			continue
		}
		skipped = append(skipped, CredentialResult{
			AuthID:  name,
			Account: name,
			Status:  StatusSkipped,
			Message: "configured account is not an active Codex credential",
		})
	}
	return candidates, skipped
}

// skipReason reports why a credential must not be warmed right now.
func skipReason(auth *coreauth.Auth, model string, now time.Time) (string, bool) {
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return "credential is disabled", true
	}
	if coreauth.HasUnauthorizedAuthFailure(auth) {
		return "credential token was rejected and awaits re-authentication", true
	}
	if views := coreauth.CooldownSnapshotForAuth(auth, now); len(views) > 0 {
		for _, view := range views {
			if view.Scope == "credential" || strings.EqualFold(view.ModelKey, canonicalModel(model)) {
				return fmt.Sprintf("credential is cooling down (%s), retry in %ds", view.Reason, view.RemainingSeconds), true
			}
		}
	}
	return "", false
}

func canonicalModel(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// authFileName reproduces the auth file name used by the management API so
// warmup.accounts can be written with the same identifiers operators see.
func authFileName(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if name := strings.TrimSpace(auth.ID); name != "" {
		return name
	}
	return strings.TrimSpace(auth.Label)
}

// accountLabel is the human-readable identity used in logs.
func accountLabel(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	for _, key := range []string{"email", "account"} {
		if value, ok := auth.Attributes[key]; ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if label := strings.TrimSpace(auth.Label); label != "" {
		return label
	}
	return auth.ID
}

func describeError(errMsg *interfaces.ErrorMessage) string {
	if errMsg == nil {
		return ""
	}
	if errMsg.Error == nil {
		return fmt.Sprintf("HTTP %d", errMsg.StatusCode)
	}
	return fmt.Sprintf("HTTP %d: %s", errMsg.StatusCode, sanitizeSummary(errMsg.Error.Error()))
}

// sanitizeSummary reduces an upstream error to a short, single-line summary.
// Error bodies can be a whole HTML challenge page; neither the state file nor
// the log should carry that.
func sanitizeSummary(raw string) string {
	trimmed := strings.Join(strings.Fields(raw), " ")
	if idx := strings.Index(trimmed, "<"); idx >= 0 {
		// Drop an HTML payload but keep any prefix that is plain text.
		trimmed = strings.TrimSpace(trimmed[:idx])
		if trimmed == "" {
			return "upstream returned an HTML error page"
		}
	}
	const limit = 200
	if len(trimmed) > limit {
		trimmed = trimmed[:limit] + "..."
	}
	return trimmed
}

// logResult emits the per-credential record required by the warmup contract:
// auth index, status, HTTP status and — when the upstream reports them — the
// Codex five-hour window percent and reset countdown.
//
// The shared log formatter only renders an allow-listed set of fields, so the
// outcome is also folded into the message to stay readable in `docker logs`.
func (r *Runner) logResult(slot time.Time, result CredentialResult, err error) {
	fields := log.Fields{
		"provider":   Provider,
		"slot":       slot.Format(time.RFC3339),
		"auth_index": result.AuthID,
		"account":    result.Account,
		"status":     result.Status,
	}
	if result.HTTPStatus != 0 {
		fields["http_status"] = result.HTTPStatus
	}
	if result.UsedPercent != nil {
		fields["used_percent"] = *result.UsedPercent
	}
	if result.ResetAfterSeconds != nil {
		fields["reset_after_seconds"] = *result.ResetAfterSeconds
	}
	message := result.Message
	if err != nil {
		message = err.Error()
	}
	// Collapse newlines so one attempt is always one log line.
	message = strings.Join(strings.Fields(message), " ")
	if message != "" {
		fields["message"] = message
	}

	// The structured fields already carry auth_index, status, HTTP status and
	// the five-hour window headers, so the message stays short.
	var verb string
	switch result.Status {
	case StatusSuccess:
		verb = "warmup: credential warmed"
	case StatusEmpty:
		verb = "warmup: credential warmed without output"
	case StatusSkipped:
		verb = "warmup: credential skipped"
	default:
		verb = "warmup: credential failed"
	}
	switch result.Status {
	case StatusSuccess, StatusEmpty, StatusSkipped:
		log.WithFields(fields).Info(verb)
	default:
		log.WithFields(fields).Warn(verb)
	}
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
