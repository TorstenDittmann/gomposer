package main

import (
	"strings"
	"testing"
	"time"
)

func TestRenderMarkdownIncludesHeader(t *testing.T) {
	out := RenderMarkdown(nil)
	if !strings.Contains(out, "| Fixture | Scenario | composer-go | composer | speed-up |") {
		t.Errorf("missing header in output:\n%s", out)
	}
}

func TestRenderMarkdownPairsToolsAndComputesSpeedup(t *testing.T) {
	results := []Result{
		{Fixture: "tiny", Scenario: ScenarioCold, Tool: ToolComposerGo, Median: 200 * time.Millisecond},
		{Fixture: "tiny", Scenario: ScenarioCold, Tool: ToolComposer, Median: 1000 * time.Millisecond},
	}
	out := RenderMarkdown(results)
	if !strings.Contains(out, "| tiny | cold |") {
		t.Errorf("missing fixture row: %s", out)
	}
	if !strings.Contains(out, "200ms") || !strings.Contains(out, "1s") {
		t.Errorf("expected formatted durations: %s", out)
	}
	if !strings.Contains(out, "5.0x") {
		t.Errorf("expected speedup 5.0x: %s", out)
	}
}

func TestRenderMarkdownSortsFixtureThenScenario(t *testing.T) {
	results := []Result{
		{Fixture: "zeta", Scenario: ScenarioWarm, Tool: ToolComposerGo, Median: 10 * time.Millisecond},
		{Fixture: "zeta", Scenario: ScenarioWarm, Tool: ToolComposer, Median: 100 * time.Millisecond},
		{Fixture: "alpha", Scenario: ScenarioLockUnchanged, Tool: ToolComposerGo, Median: 5 * time.Millisecond},
		{Fixture: "alpha", Scenario: ScenarioLockUnchanged, Tool: ToolComposer, Median: 50 * time.Millisecond},
		{Fixture: "alpha", Scenario: ScenarioCold, Tool: ToolComposerGo, Median: 100 * time.Millisecond},
		{Fixture: "alpha", Scenario: ScenarioCold, Tool: ToolComposer, Median: 200 * time.Millisecond},
	}
	out := RenderMarkdown(results)
	// alpha/cold must appear before alpha/lock-unchanged, which must appear
	// before zeta/warm.
	idxAlphaCold := strings.Index(out, "| alpha | cold |")
	idxAlphaLock := strings.Index(out, "| alpha | lock-unchanged |")
	idxZetaWarm := strings.Index(out, "| zeta | warm |")
	if idxAlphaCold < 0 || idxAlphaLock < 0 || idxZetaWarm < 0 {
		t.Fatalf("rows missing:\n%s", out)
	}
	if !(idxAlphaCold < idxAlphaLock && idxAlphaLock < idxZetaWarm) {
		t.Errorf("rows out of order: alphaCold=%d alphaLock=%d zetaWarm=%d", idxAlphaCold, idxAlphaLock, idxZetaWarm)
	}
}

func TestRenderMarkdownHandlesMissingTool(t *testing.T) {
	// composer-go ran but composer was skipped.
	results := []Result{
		{Fixture: "tiny", Scenario: ScenarioCold, Tool: ToolComposerGo, Median: 200 * time.Millisecond},
	}
	out := RenderMarkdown(results)
	if !strings.Contains(out, "n/a") {
		t.Errorf("missing 'n/a' marker for absent tool: %s", out)
	}
}
