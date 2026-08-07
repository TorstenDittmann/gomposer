package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	ToolGomposer Tool = "gomposer"
	ToolComposer Tool = "composer"
)

// Plan describes everything the runner needs to time.
type Plan struct {
	Fixtures     []Fixture
	Scenarios    []Scenario
	Runs         int
	GomposerPath string
	ComposerPath string
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
	// env contains tuple-isolated cache homes that must override the process
	// environment for both warmups and timed runs.
	Run(ctx context.Context, bin, workDir string, env map[string]string) (time.Duration, error)
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

// prepareScenario brings dir into the pre-run state for scenario s. It is
// called BEFORE every timed run (not just once per scenario) so that cold
// truly measures cold every time.
func prepareScenario(s Scenario, dir, cacheRoot string) error {
	switch s {
	case ScenarioCold:
		paths := []string{
			filepath.Join(dir, "vendor"),
			filepath.Join(dir, "composer.lock"),
			filepath.Join(dir, "gomposer.lock"),
			cacheRoot,
		}
		for _, path := range paths {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("bench: prepare cold: rm %s: %w", path, err)
			}
		}
	case ScenarioWarm:
		if err := os.RemoveAll(filepath.Join(dir, "vendor")); err != nil {
			return fmt.Errorf("bench: prepare warm: rm vendor: %w", err)
		}
	case ScenarioLockUnchanged:
		// Intentionally a no-op: the timed run starts from a fully populated
		// state. The runner is responsible for warming first.
	default:
		return fmt.Errorf("bench: unknown scenario %q", s)
	}
	return nil
}

// scenarioNeedsWarmup returns true when the scenario requires a single
// non-timed install before the timed loop, to populate state that
// prepareScenario assumes is already present.
func scenarioNeedsWarmup(s Scenario) bool {
	return s == ScenarioWarm || s == ScenarioLockUnchanged
}

// Run executes plan and returns one Result per (fixture, scenario, tool).
//
// For every tuple:
//  1. Copy the fixture into a fresh temp directory.
//  2. If the scenario needs a warmup, run one untimed install with the tool.
//  3. Loop plan.Runs times: prepareScenario → CmdRunner.Run → record duration.
//  4. Compute the median of the recorded durations.
//
// The function fails fast: any single CmdRunner error aborts the whole bench.
// We refuse to publish "best 2 of 3 ignoring failures" stats — every sample
// must succeed for the median to be meaningful.
func Run(ctx context.Context, plan Plan, runner CmdRunner) ([]Result, error) {
	if plan.Runs < 1 {
		return nil, fmt.Errorf("bench: Plan.Runs must be >=1, got %d", plan.Runs)
	}
	if len(plan.Fixtures) == 0 {
		return nil, fmt.Errorf("bench: Plan.Fixtures is empty")
	}
	if len(plan.Scenarios) == 0 {
		return nil, fmt.Errorf("bench: Plan.Scenarios is empty")
	}

	tools := []struct {
		tool Tool
		bin  string
	}{
		{ToolGomposer, plan.GomposerPath},
		{ToolComposer, plan.ComposerPath},
	}

	var results []Result
	for _, fx := range plan.Fixtures {
		for _, sc := range plan.Scenarios {
			for _, tl := range tools {
				r, err := runOne(ctx, runner, fx, sc, tl.tool, tl.bin, plan.Runs)
				if err != nil {
					return nil, fmt.Errorf("bench: %s/%s/%s: %w", fx.Name, sc, tl.tool, err)
				}
				results = append(results, r)
			}
		}
	}
	return results, nil
}

func runOne(ctx context.Context, runner CmdRunner, fx Fixture, sc Scenario, tool Tool, bin string, runs int) (Result, error) {
	work, err := os.MkdirTemp("", "gomposer-bench-")
	if err != nil {
		return Result{}, fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(work)

	if err := copyDir(fx.Path, work); err != nil {
		return Result{}, fmt.Errorf("seed fixture: %w", err)
	}
	cacheRoot := filepath.Join(work, ".bench-cache")
	env := benchmarkEnv(cacheRoot)

	if scenarioNeedsWarmup(sc) {
		if _, err := runner.Run(ctx, bin, work, env); err != nil {
			return Result{}, fmt.Errorf("warmup: %w", err)
		}
	}

	samples := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		if err := prepareScenario(sc, work, cacheRoot); err != nil {
			return Result{}, err
		}
		d, err := runner.Run(ctx, bin, work, env)
		if err != nil {
			return Result{}, fmt.Errorf("run %d: %w", i+1, err)
		}
		samples = append(samples, d)
	}
	return Result{
		Fixture:  fx.Name,
		Scenario: sc,
		Tool:     tool,
		Median:   median(samples),
		Samples:  samples,
	}, nil
}

func benchmarkEnv(cacheRoot string) map[string]string {
	return map[string]string{
		"XDG_CACHE_HOME":       filepath.Join(cacheRoot, "xdg"),
		"COMPOSER_CACHE_DIR":   filepath.Join(cacheRoot, "composer-cache"),
		"COMPOSER_HOME":        filepath.Join(cacheRoot, "composer-home"),
		"COMPOSER_NO_BLOCKING": "1",
	}
}

// copyDir recursively copies src into dst. Only used for benchmark fixture
// seeding; symlinks and special files are not expected.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, info.Mode())
		}
		return copyFile(path, out, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// execCmdRunner is the production CmdRunner. It runs `<bin> install` in
// workDir, discards stdout, captures stderr only for error reporting, and
// times the entire exec.Cmd.Run() call.
type execCmdRunner struct{}

func (execCmdRunner) Run(ctx context.Context, bin, workDir string, env map[string]string) (time.Duration, error) {
	if bin == "" {
		return 0, fmt.Errorf("bench: empty binary path")
	}
	cmd := exec.CommandContext(ctx, bin, "install")
	cmd.Dir = workDir
	cmd.Env = mergeEnv(os.Environ(), env)
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	if err != nil {
		return 0, fmt.Errorf("bench: %s install in %s failed: %w\nstderr:\n%s",
			bin, workDir, err, stderr.String())
	}
	return elapsed, nil
}

func mergeEnv(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		if i := bytes.IndexByte([]byte(entry), '='); i >= 0 {
			if _, replaced := overrides[entry[:i]]; replaced {
				continue
			}
		}
		out = append(out, entry)
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+overrides[key])
	}
	return out
}
