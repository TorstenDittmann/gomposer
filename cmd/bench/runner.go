package main

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Scenario is one of the three benchmark modes.
type Scenario string

const (
	ScenarioCold          Scenario = "cold"
	ScenarioWarm          Scenario = "warm"
	ScenarioLockUnchanged Scenario = "lock-unchanged"
)

// AllScenarios is the canonical order: cold, warm, lock-unchanged. Report
// rendering depends on this ordering.
var AllScenarios = []Scenario{ScenarioCold, ScenarioWarm, ScenarioLockUnchanged}

// ParseScenario converts a CLI string to a Scenario.
func ParseScenario(s string) (Scenario, error) {
	for _, sc := range AllScenarios {
		if string(sc) == s {
			return sc, nil
		}
	}
	return "", fmt.Errorf("bench: unknown scenario %q (want one of cold, warm, lock-unchanged)", s)
}

// Tool identifies which binary we are timing.
type Tool string

const (
	ToolComposerGo Tool = "composer-go"
	ToolComposer   Tool = "composer"
)

// Plan describes everything the runner needs to time.
type Plan struct {
	Fixtures       []Fixture
	Scenarios      []Scenario
	Runs           int
	ComposerGoPath string
	ComposerPath   string
}

// Result is one cell of the report: median wall time of N runs of a single
// tool against a single fixture in a single scenario.
type Result struct {
	Fixture  string
	Scenario Scenario
	Tool     Tool
	Median   time.Duration
	// Samples is the raw per-run timings, kept for --verbose / debugging.
	Samples []time.Duration
}

// CmdRunner abstracts os/exec so tests can inject a fake. Production
// implementation (execCmdRunner) shells out to the real binary.
type CmdRunner interface {
	// Run executes `bin install` in workDir and returns the wall time. Errors
	// surface up to the bench runner; a single failed sample fails the whole
	// fixture/scenario/tool tuple (we won't ship dishonest "best of N" stats).
	Run(ctx context.Context, bin, workDir string) (time.Duration, error)
}

// median returns the middle value of d. For an even-length slice it returns
// the mean of the two middle values. d is sorted in place.
func median(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	cp := append([]time.Duration(nil), d...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}
