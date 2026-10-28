package cliproxy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// The warmup feature is deliberately isolated so upstream merges stay simple.
// These checks pin the wiring: the scheduler must be created from the server's
// shared request handler (not a private upstream call), and it must be started
// and stopped by the service lifecycle.
func TestWarmupUsesSharedRequestHandler(t *testing.T) {
	source, err := os.ReadFile("service_warmup.go")
	if err != nil {
		t.Fatalf("read service_warmup.go: %v", err)
	}
	text := string(source)
	for _, want := range []string{
		"s.server.RequestExecutor()",
		"warmup.NewScheduler(",
		"scheduler.Apply(cfg.Warmup)",
		"scheduler.Start(ctx)",
		"scheduler.Stop()",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("service_warmup.go is missing %q", want)
		}
	}
}

func TestWarmupWiredIntoLifecycle(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "service_lifecycle.go", nil, 0)
	if err != nil {
		t.Fatalf("parse service_lifecycle.go: %v", err)
	}
	var startCalls, stopCalls int
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch selector.Sel.Name {
		case "startWarmup":
			startCalls++
		case "stopWarmup":
			stopCalls++
		}
		return true
	})
	if startCalls != 1 {
		t.Fatalf("service lifecycle calls startWarmup %d times, want exactly 1", startCalls)
	}
	if stopCalls != 1 {
		t.Fatalf("service lifecycle calls stopWarmup %d times, want exactly 1", stopCalls)
	}
}

// A hot-reload must reach the scheduler from the config commit path.
func TestWarmupAppliedOnConfigReload(t *testing.T) {
	source, err := os.ReadFile("service_config.go")
	if err != nil {
		t.Fatalf("read service_config.go: %v", err)
	}
	if !strings.Contains(string(source), "s.applyWarmupConfig(cfg)") {
		t.Fatalf("applyConfigRuntime does not forward warmup settings on reload")
	}
}
