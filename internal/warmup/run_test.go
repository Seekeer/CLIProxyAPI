package warmup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

var moscow = mustLoadLocation("Europe/Moscow")

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}

// recordingExecutor records every internal request and answers with a canned
// response, so no test ever reaches an upstream host.
type recordingExecutor struct {
	mu       sync.Mutex
	requests []handlers.ModelExecutionRequest
	contexts []context.Context
	respond  func(req handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage)
}

func (e *recordingExecutor) ExecuteModel(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage) {
	e.mu.Lock()
	e.requests = append(e.requests, req)
	e.contexts = append(e.contexts, ctx)
	respond := e.respond
	e.mu.Unlock()
	if respond != nil {
		return respond(req)
	}
	return handlers.ModelExecutionResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`),
	}, nil
}

func (e *recordingExecutor) recorded() []handlers.ModelExecutionRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]handlers.ModelExecutionRequest(nil), e.requests...)
}

// staticAuths is a fixed credential list.
type staticAuths struct {
	mu    sync.Mutex
	auths []*coreauth.Auth
}

func (s *staticAuths) List() []*coreauth.Auth {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*coreauth.Auth(nil), s.auths...)
}

func codexAuth(id, email string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:         id,
		Provider:   Provider,
		Status:     coreauth.StatusActive,
		Label:      email,
		Attributes: map[string]string{"email": email},
	}
}

func baseWarmupConfig() config.WarmupConfig {
	return config.WarmupConfig{
		Enabled:         true,
		Timezone:        "Europe/Moscow",
		Model:           "gpt-6-luna",
		ReasoningEffort: "low",
		Prompt:          "Reply with exactly: ok",
		Schedules:       []string{"06:00"},
	}
}

func TestBuildRequestBodyCarriesModelAndEffort(t *testing.T) {
	body, err := buildRequestBody("gpt-6-luna", "Reply with exactly: ok", "low")
	if err != nil {
		t.Fatalf("buildRequestBody() error = %v", err)
	}
	var decoded struct {
		Model     string `json:"model"`
		Stream    bool   `json:"stream"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Input []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"input"`
	}
	if errUnmarshal := json.Unmarshal(body, &decoded); errUnmarshal != nil {
		t.Fatalf("payload is not valid JSON: %v", errUnmarshal)
	}
	if decoded.Model != "gpt-6-luna" {
		t.Fatalf("payload model = %q, want gpt-6-luna", decoded.Model)
	}
	if decoded.Reasoning.Effort != "low" {
		t.Fatalf("payload reasoning.effort = %q, want low", decoded.Reasoning.Effort)
	}
	if decoded.Stream {
		t.Fatalf("payload stream = true, want non-streaming")
	}
	if len(decoded.Input) != 1 || decoded.Input[0].Role != "user" || decoded.Input[0].Content != "Reply with exactly: ok" {
		t.Fatalf("payload input = %+v, want a single short user message", decoded.Input)
	}
}

func TestBuildRequestBodyOmitsEmptyReasoning(t *testing.T) {
	body, err := buildRequestBody("gpt-6-luna", "ok", "")
	if err != nil {
		t.Fatalf("buildRequestBody() error = %v", err)
	}
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(body, &decoded); errUnmarshal != nil {
		t.Fatalf("payload is not valid JSON: %v", errUnmarshal)
	}
	if _, exists := decoded["reasoning"]; exists {
		t.Fatalf("payload must omit reasoning when no effort is configured")
	}
}

func TestRunSendsOnePinnedRequestPerActiveCredential(t *testing.T) {
	auths := &staticAuths{auths: []*coreauth.Auth{
		codexAuth("codex-a.json", "a@example.com"),
		codexAuth("codex-b.json", "b@example.com"),
	}}
	executor := &recordingExecutor{}
	runner := &Runner{Auths: auths, Executor: executor}

	results := runner.Run(context.Background(), baseWarmupConfig(), nowAt(t, "2026-10-28T06:00:00"))

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	requests := executor.recorded()
	if len(requests) != 2 {
		t.Fatalf("executor saw %d requests, want exactly one per credential", len(requests))
	}
	seen := map[string]bool{}
	for i, req := range requests {
		if req.AuthID == "" {
			t.Fatalf("request %d has no pinned auth id", i)
		}
		if req.ForcedProvider != Provider {
			t.Fatalf("request %d forced provider = %q, want %q", i, req.ForcedProvider, Provider)
		}
		if req.Model != "gpt-6-luna" {
			t.Fatalf("request %d model = %q, want gpt-6-luna", i, req.Model)
		}
		var payload map[string]any
		if errUnmarshal := json.Unmarshal(req.Body, &payload); errUnmarshal != nil {
			t.Fatalf("request %d body is not JSON: %v", i, errUnmarshal)
		}
		if payload["model"] != "gpt-6-luna" {
			t.Fatalf("request %d payload model = %v, want gpt-6-luna", i, payload["model"])
		}
		reasoning, ok := payload["reasoning"].(map[string]any)
		if !ok || reasoning["effort"] != "low" {
			t.Fatalf("request %d payload reasoning = %v, want effort low", i, payload["reasoning"])
		}
		seen[req.AuthID] = true
	}
	if !seen["codex-a.json"] || !seen["codex-b.json"] {
		t.Fatalf("pinned auth ids = %v, want both credentials", seen)
	}
	for _, result := range results {
		if result.Status != StatusSuccess {
			t.Fatalf("result %+v, want success", result)
		}
	}
}

func TestRunPinsAuthOnEveryRequest(t *testing.T) {
	auths := &staticAuths{auths: []*coreauth.Auth{codexAuth("codex-a.json", "a@example.com")}}
	executor := &recordingExecutor{}
	runner := &Runner{Auths: auths, Executor: executor}

	runner.Run(context.Background(), baseWarmupConfig(), nowAt(t, "2026-10-28T06:00:00"))

	requests := executor.recorded()
	if len(requests) != 1 {
		t.Fatalf("executor saw %d requests, want 1", len(requests))
	}
	// ModelExecutionRequest.AuthID is what ExecuteModel turns into the pinned
	// selector metadata; the core owns that translation and tests it itself.
	if requests[0].AuthID != "codex-a.json" {
		t.Fatalf("request auth id = %q, want codex-a.json", requests[0].AuthID)
	}
	if requests[0].Path != "/v1/responses" {
		t.Fatalf("request path = %q, want /v1/responses", requests[0].Path)
	}
	if requests[0].EntryProtocol != "openai-response" || requests[0].ExitProtocol != "openai-response" {
		t.Fatalf("protocols = %q/%q, want openai-response", requests[0].EntryProtocol, requests[0].ExitProtocol)
	}
}

func TestRunSkipsDisabledAndCoolingCredentials(t *testing.T) {
	disabled := codexAuth("codex-disabled.json", "disabled@example.com")
	disabled.Disabled = true

	cooling := codexAuth("codex-cool.json", "cool@example.com")
	cooling.Quota = coreauth.QuotaState{
		Exceeded:      true,
		Reason:        "quota",
		NextRecoverAt: time.Now().Add(20 * time.Minute).UTC(),
	}

	healthy := codexAuth("codex-ok.json", "ok@example.com")

	auths := &staticAuths{auths: []*coreauth.Auth{disabled, cooling, healthy}}
	executor := &recordingExecutor{}
	runner := &Runner{Auths: auths, Executor: executor}

	results := runner.Run(context.Background(), baseWarmupConfig(), time.Now())

	requests := executor.recorded()
	if len(requests) != 1 || requests[0].AuthID != "codex-ok.json" {
		t.Fatalf("requests = %+v, want only codex-ok.json", requests)
	}
	statuses := map[string]string{}
	for _, result := range results {
		statuses[result.AuthID] = result.Status
	}
	if statuses["codex-disabled.json"] != StatusSkipped {
		t.Fatalf("disabled credential status = %q, want skipped", statuses["codex-disabled.json"])
	}
	if statuses["codex-cool.json"] != StatusSkipped {
		t.Fatalf("cooling credential status = %q, want skipped", statuses["codex-cool.json"])
	}
	if statuses["codex-ok.json"] != StatusSuccess {
		t.Fatalf("healthy credential status = %q, want success", statuses["codex-ok.json"])
	}
}

func TestRunAccountsFilter(t *testing.T) {
	auths := &staticAuths{auths: []*coreauth.Auth{
		codexAuth("codex-a.json", "a@example.com"),
		codexAuth("codex-b.json", "b@example.com"),
	}}
	executor := &recordingExecutor{}
	runner := &Runner{Auths: auths, Executor: executor}

	warmup := baseWarmupConfig()
	warmup.Accounts = []string{"codex-b.json", "codex-missing.json"}

	results := runner.Run(context.Background(), warmup, time.Now())

	requests := executor.recorded()
	if len(requests) != 1 || requests[0].AuthID != "codex-b.json" {
		t.Fatalf("requests = %+v, want only codex-b.json", requests)
	}
	var sawMissing bool
	for _, result := range results {
		if result.AuthID == "codex-missing.json" && result.Status == StatusSkipped {
			sawMissing = true
		}
	}
	if !sawMissing {
		t.Fatalf("a configured-but-absent account must be reported as skipped: %+v", results)
	}
}

func TestRunEmptyAccountsSelectsOnlyCodex(t *testing.T) {
	claude := &coreauth.Auth{ID: "claude.json", Provider: "claude", Status: coreauth.StatusActive}
	codex := codexAuth("codex-a.json", "a@example.com")
	auths := &staticAuths{auths: []*coreauth.Auth{claude, codex}}
	executor := &recordingExecutor{}
	runner := &Runner{Auths: auths, Executor: executor}

	runner.Run(context.Background(), baseWarmupConfig(), time.Now())

	requests := executor.recorded()
	if len(requests) != 1 || requests[0].AuthID != "codex-a.json" {
		t.Fatalf("requests = %+v, want only the codex credential", requests)
	}
}

func TestRunContinuesAfterOneCredentialFails(t *testing.T) {
	auths := &staticAuths{auths: []*coreauth.Auth{
		codexAuth("codex-fail.json", "fail@example.com"),
		codexAuth("codex-ok.json", "ok@example.com"),
	}}
	executor := &recordingExecutor{respond: func(req handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage) {
		if req.AuthID == "codex-fail.json" {
			return handlers.ModelExecutionResponse{}, &interfaces.ErrorMessage{
				StatusCode: http.StatusTooManyRequests,
				Error:      errors.New("rate limited"),
			}
		}
		return handlers.ModelExecutionResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`),
		}, nil
	}}
	runner := &Runner{Auths: auths, Executor: executor}

	results := runner.Run(context.Background(), baseWarmupConfig(), time.Now())

	if len(executor.recorded()) != 2 {
		t.Fatalf("executor saw %d requests, want both credentials attempted", len(executor.recorded()))
	}
	statuses := map[string]CredentialResult{}
	for _, result := range results {
		statuses[result.AuthID] = result
	}
	if statuses["codex-fail.json"].Status != StatusFailed || statuses["codex-fail.json"].HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("failing credential result = %+v, want failed with 429", statuses["codex-fail.json"])
	}
	if statuses["codex-ok.json"].Status != StatusSuccess {
		t.Fatalf("second credential result = %+v, want success", statuses["codex-ok.json"])
	}
}

func TestRunRecordsWindowHeaders(t *testing.T) {
	auths := &staticAuths{auths: []*coreauth.Auth{codexAuth("codex-a.json", "a@example.com")}}
	executor := &recordingExecutor{respond: func(handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage) {
		headers := http.Header{}
		// Secondary (weekly) bucket must be ignored in favour of the 5h one.
		headers.Set("X-Codex-Secondary-Window-Minutes", "10080")
		headers.Set("X-Codex-Secondary-Used-Percent", "99")
		headers.Set("X-Codex-Primary-Window-Minutes", "300")
		headers.Set("X-Codex-Primary-Used-Percent", "4")
		headers.Set("X-Codex-Primary-Reset-After-Seconds", "14738")
		return handlers.ModelExecutionResponse{
			StatusCode: http.StatusOK,
			Headers:    headers,
			Body:       []byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`),
		}, nil
	}}
	runner := &Runner{Auths: auths, Executor: executor}

	results := runner.Run(context.Background(), baseWarmupConfig(), time.Now())

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	result := results[0]
	if result.UsedPercent == nil || *result.UsedPercent != 4 {
		t.Fatalf("used percent = %v, want 4", result.UsedPercent)
	}
	if result.ResetAfterSeconds == nil || *result.ResetAfterSeconds != 14738 {
		t.Fatalf("reset after seconds = %v, want 14738", result.ResetAfterSeconds)
	}
}

func TestParseWindowUsageIgnoresNonFiveHourBuckets(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Codex-Primary-Window-Minutes", "10080")
	headers.Set("X-Codex-Primary-Used-Percent", "42")
	usage := parseWindowUsage(headers)
	if usage.UsedPercent != nil || usage.ResetAfterSeconds != nil {
		t.Fatalf("usage = %+v, want no five-hour measurement", usage)
	}
}

func TestParseWindowUsageIgnoresMalformedValues(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Codex-Primary-Window-Minutes", "300")
	headers.Set("X-Codex-Primary-Used-Percent", "not-a-number")
	usage := parseWindowUsage(headers)
	if usage.UsedPercent != nil {
		t.Fatalf("used percent = %v, want nil for a malformed header", *usage.UsedPercent)
	}
}

func TestRunTreatsEmptyResponseAsWarmed(t *testing.T) {
	auths := &staticAuths{auths: []*coreauth.Auth{codexAuth("codex-a.json", "a@example.com")}}
	executor := &recordingExecutor{respond: func(handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage) {
		return handlers.ModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(`{"output":[]}`)}, nil
	}}
	runner := &Runner{Auths: auths, Executor: executor}

	results := runner.Run(context.Background(), baseWarmupConfig(), time.Now())

	if len(results) != 1 || results[0].Status != StatusEmpty {
		t.Fatalf("results = %+v, want a single empty-output result", results)
	}
}

func TestRunnerNeverCallsExecutorWhenNoCredentials(t *testing.T) {
	executor := &recordingExecutor{}
	runner := &Runner{Auths: &staticAuths{}, Executor: executor}
	results := runner.Run(context.Background(), baseWarmupConfig(), time.Now())
	if len(results) != 0 {
		t.Fatalf("results = %+v, want none", results)
	}
	if len(executor.recorded()) != 0 {
		t.Fatalf("executor was called without credentials")
	}
}

func nowAt(t *testing.T, localDateTime string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02T15:04:05", localDateTime, moscow)
	if err != nil {
		t.Fatalf("parse %q: %v", localDateTime, err)
	}
	return parsed
}

func TestSanitizeSummaryDropsHTMLErrorBodies(t *testing.T) {
	if got := sanitizeSummary("<html><head>challenge</head></html>"); got != "upstream returned an HTML error page" {
		t.Fatalf("sanitizeSummary() = %q, want the HTML placeholder", got)
	}
	if got := sanitizeSummary("bad gateway\n<html>"); got != "bad gateway" {
		t.Fatalf("sanitizeSummary() = %q, want the text prefix", got)
	}
	long := strings.Repeat("x", 400)
	if got := sanitizeSummary(long); len(got) != 203 || !strings.HasSuffix(got, "...") {
		t.Fatalf("sanitizeSummary() length = %d, want truncation at 200", len(got))
	}
	if got := sanitizeSummary("plain"); got != "plain" {
		t.Fatalf("sanitizeSummary() = %q, want plain text unchanged", got)
	}
}

func TestDescribeErrorIncludesStatusAndSummary(t *testing.T) {
	got := describeError(&interfaces.ErrorMessage{StatusCode: 429, Error: errors.New("rate\nlimited")})
	if got != "HTTP 429: rate limited" {
		t.Fatalf("describeError() = %q, want a single-line summary", got)
	}
}
