package warmup

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// codexFiveHourWindowMinutes identifies the short Codex window (5 hours) out of
// the primary/secondary buckets. Only a positively identified bucket is
// reported so a weekly window is never mistaken for the five-hour one.
const codexFiveHourWindowMinutes = "300"

// buildRequestBody produces the OpenAI Responses payload for one warmup turn.
//
// The shape deliberately mirrors a minimal Codex client call: the selected
// model, a single short user message, non-streaming, and the configured
// reasoning effort. Everything else (payload-override rules, inference defaults,
// protocol translation) is applied by the core request path, not here.
func buildRequestBody(model, prompt, reasoningEffort string) ([]byte, error) {
	body := map[string]any{
		"model":               model,
		"input":               []map[string]any{{"type": "message", "role": "user", "content": prompt}},
		"store":               false,
		"stream":              false,
		"include":             []string{},
		"parallel_tool_calls": false,
	}
	if effort := strings.TrimSpace(reasoningEffort); effort != "" {
		body["reasoning"] = map[string]any{"effort": effort}
	}
	return json.Marshal(body)
}

// windowUsage is the five-hour window information carried by upstream headers.
type windowUsage struct {
	UsedPercent       *float64
	ResetAfterSeconds *float64
}

// parseWindowUsage extracts the Codex five-hour bucket from response headers.
// Missing or malformed values are reported as nil rather than zero so "not
// reported" stays distinguishable from a real measurement.
func parseWindowUsage(headers http.Header) windowUsage {
	var usage windowUsage
	if headers == nil {
		return usage
	}
	for _, bucket := range []string{"primary", "secondary"} {
		prefix := "x-codex-" + bucket + "-"
		if minutes := strings.TrimSpace(headerValue(headers, prefix+"window-minutes")); minutes != codexFiveHourWindowMinutes {
			continue
		}
		usage.UsedPercent = parseHeaderFloat(headers, prefix+"used-percent")
		usage.ResetAfterSeconds = parseHeaderFloat(headers, prefix+"reset-after-seconds")
		return usage
	}
	return usage
}

func headerValue(headers http.Header, name string) string {
	return headers.Get(name)
}

func parseHeaderFloat(headers http.Header, name string) *float64 {
	raw := strings.TrimSpace(headerValue(headers, name))
	if raw == "" {
		return nil
	}
	value, errParse := strconv.ParseFloat(raw, 64)
	if errParse != nil {
		return nil
	}
	return &value
}

// responseHasContent reports whether a parsed Responses payload carries any
// model output. A 200 with no output still warms the window, so it is reported
// separately instead of being treated as a failure.
func responseHasContent(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var parsed struct {
		Output []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		} `json:"output"`
		OutputText string `json:"output_text"`
	}
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		return false
	}
	if strings.TrimSpace(parsed.OutputText) != "" {
		return true
	}
	for _, item := range parsed.Output {
		if strings.TrimSpace(item.Type) == "message" && len(item.Content) > 2 {
			return true
		}
	}
	return false
}
