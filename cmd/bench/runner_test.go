package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestScenarioStringRoundtrips(t *testing.T) {
	for _, s := range []Scenario{ScenarioCold, ScenarioWarm, ScenarioLockUnchanged} {
		got, err := ParseScenario(string(s))
		if err != nil {
			t.Fatalf("ParseScenario(%q): %v", s, err)
		}
		if got != s {
			t.Errorf("ParseScenario(%q) = %q", s, got)
		}
	}
}

func TestParseScenarioRejectsUnknown(t *testing.T) {
	if _, err := ParseScenario("frozen"); err == nil {
		t.Error("expected error on unknown scenario")
	}
}

func TestMedianOddCount(t *testing.T) {
	got := median([]time.Duration{3 * time.Second, 1 * time.Second, 2 * time.Second})
	if got != 2*time.Second {
		t.Errorf("median = %v, want 2s", got)
	}
}

func TestMedianEvenCount(t *testing.T) {
	got := median([]time.Duration{4 * time.Second, 1 * time.Second, 2 * time.Second, 3 * time.Second})
	// median of even-count sample = mean of the two middle values: (2+3)/2 = 2.5s
	if got != 2500*time.Millisecond {
		t.Errorf("median = %v, want 2.5s", got)
	}
}

func TestMedianEmptyReturnsZero(t *testing.T) {
	if got := median(nil); got != 0 {
		t.Errorf("median(nil) = %v, want 0", got)
	}
}

func TestPrepareColdRemovesVendorAndLocks(t *testing.T) {
	dir := t.TempDir()
	cacheRoot := filepath.Join(dir, ".bench-cache")
	for _, p := range []string{
		filepath.Join(dir, "vendor", "psr", "log"),
		filepath.Join(dir, "composer.lock"),
		filepath.Join(dir, "gomposer.lock"),
		filepath.Join(cacheRoot, "xdg", "gomposer", "metadata"),
		filepath.Join(cacheRoot, "composer-cache", "files"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareScenario(ScenarioCold, dir, cacheRoot); err != nil {
		t.Fatalf("prepareScenario: %v", err)
	}
	for _, p := range []string{"vendor", "composer.lock", "gomposer.lock"} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s should be removed: %v", p, err)
		}
	}
	if _, err := os.Stat(cacheRoot); !os.IsNotExist(err) {
		t.Errorf("cache root should be removed: %v", err)
	}
}

func TestPrepareWarmRemovesOnlyVendor(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gomposer.lock"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cacheRoot := filepath.Join(dir, ".bench-cache")
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareScenario(ScenarioWarm, dir, cacheRoot); err != nil {
		t.Fatalf("prepareScenario: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor")); !os.IsNotExist(err) {
		t.Error("vendor should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "gomposer.lock")); err != nil {
		t.Error("gomposer.lock should be preserved on warm")
	}
	if _, err := os.Stat(cacheRoot); err != nil {
		t.Error("cache root should be preserved on warm")
	}
}

func TestPrepareLockUnchangedTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"vendor/keep", "gomposer.lock"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareScenario(ScenarioLockUnchanged, dir, filepath.Join(dir, ".bench-cache")); err != nil {
		t.Fatalf("prepareScenario: %v", err)
	}
	for _, p := range []string{"vendor/keep", "gomposer.lock"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s should be preserved on lock-unchanged: %v", p, err)
		}
	}
}

// fakeRunner returns canned durations and records calls.
type fakeRunner struct {
	mu    sync.Mutex
	calls []fakeCall
	// fn returns (duration, error) given the call index. nil -> 100ms always.
	fn func(idx int, bin, workDir string) (time.Duration, error)
}

type fakeCall struct {
	bin     string
	workDir string
	env     map[string]string
}

func (f *fakeRunner) Run(_ context.Context, bin, workDir string, env map[string]string) (time.Duration, error) {
	f.mu.Lock()
	idx := len(f.calls)
	envCopy := make(map[string]string, len(env))
	for key, value := range env {
		envCopy[key] = value
	}
	f.calls = append(f.calls, fakeCall{bin: bin, workDir: workDir, env: envCopy})
	f.mu.Unlock()
	if f.fn != nil {
		return f.fn(idx, bin, workDir)
	}
	return 100 * time.Millisecond, nil
}

func writeFixture(t *testing.T, root, name string) Fixture {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"x/y","require":{"psr/log":"^3.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(dir)
	return Fixture{Name: name, Path: abs}
}

func TestRunProducesOneResultPerTuple(t *testing.T) {
	root := t.TempDir()
	f := writeFixture(t, root, "tiny")
	plan := Plan{
		Fixtures:     []Fixture{f},
		Scenarios:    []Scenario{ScenarioCold, ScenarioWarm, ScenarioLockUnchanged},
		Runs:         3,
		GomposerPath: "gomposer",
		ComposerPath: "composer",
	}
	fr := &fakeRunner{}
	results, err := Run(context.Background(), plan, fr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 1 fixture * 3 scenarios * 2 tools = 6 results.
	if len(results) != 6 {
		t.Errorf("got %d results, want 6", len(results))
	}
}

func TestRunUsesIsolatedCacheEnvironmentPerTuple(t *testing.T) {
	root := t.TempDir()
	fixtures := []Fixture{writeFixture(t, root, "one"), writeFixture(t, root, "two")}
	fr := &fakeRunner{}
	_, err := Run(context.Background(), Plan{
		Fixtures: fixtures, Scenarios: []Scenario{ScenarioCold}, Runs: 1,
		GomposerPath: "gomposer", ComposerPath: "composer",
	}, fr)
	if err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 4 {
		t.Fatalf("calls = %d, want 4", len(fr.calls))
	}
	seen := make(map[string]bool)
	for _, call := range fr.calls {
		for _, key := range []string{"XDG_CACHE_HOME", "COMPOSER_CACHE_DIR", "COMPOSER_HOME"} {
			if call.env[key] == "" {
				t.Errorf("%s missing from env: %v", key, call.env)
			}
		}
		if call.env["COMPOSER_NO_BLOCKING"] != "1" {
			t.Errorf("Composer benchmark policy environment missing: %v", call.env)
		}
		cacheRoot := filepath.Dir(call.env["XDG_CACHE_HOME"])
		if seen[cacheRoot] {
			t.Errorf("cache root reused across tuples: %s", cacheRoot)
		}
		seen[cacheRoot] = true
	}
}

func TestWarmupAndTimedRunsShareCacheEnvironment(t *testing.T) {
	root := t.TempDir()
	fr := &fakeRunner{}
	_, err := Run(context.Background(), Plan{
		Fixtures: []Fixture{writeFixture(t, root, "tiny")}, Scenarios: []Scenario{ScenarioWarm}, Runs: 2,
		GomposerPath: "gomposer", ComposerPath: "composer",
	}, fr)
	if err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 6 {
		t.Fatalf("calls = %d, want 2 tools * (warmup + 2 runs)", len(fr.calls))
	}
	for start := 0; start < len(fr.calls); start += 3 {
		want := fr.calls[start].env["XDG_CACHE_HOME"]
		for _, call := range fr.calls[start : start+3] {
			if call.env["XDG_CACHE_HOME"] != want {
				t.Errorf("tuple cache changed: %q != %q", call.env["XDG_CACHE_HOME"], want)
			}
		}
	}
}

func TestColdClearsCacheBeforeEveryTimedRun(t *testing.T) {
	root := t.TempDir()
	fr := &fakeRunner{fn: func(_ int, _, workDir string) (time.Duration, error) {
		cacheRoot := filepath.Join(workDir, ".bench-cache")
		if _, err := os.Stat(cacheRoot); !os.IsNotExist(err) {
			return 0, errors.New("cold cache survived into timed run")
		}
		if err := os.MkdirAll(filepath.Join(cacheRoot, "populated"), 0o755); err != nil {
			return 0, err
		}
		return time.Millisecond, nil
	}}
	_, err := Run(context.Background(), Plan{
		Fixtures: []Fixture{writeFixture(t, root, "tiny")}, Scenarios: []Scenario{ScenarioCold}, Runs: 3,
		GomposerPath: "gomposer", ComposerPath: "composer",
	}, fr)
	if err != nil {
		t.Fatal(err)
	}
}

func TestMergeEnvOverridesExistingValues(t *testing.T) {
	got := mergeEnv([]string{"PATH=/bin", "XDG_CACHE_HOME=/old"}, map[string]string{"XDG_CACHE_HOME": "/new", "COMPOSER_HOME": "/composer"})
	want := map[string]string{"PATH": "/bin", "XDG_CACHE_HOME": "/new", "COMPOSER_HOME": "/composer"}
	for _, entry := range got {
		for key, value := range want {
			if entry == key+"="+value {
				delete(want, key)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing environment entries: %v (got %v)", want, got)
	}
}

func TestRunReportsMedian(t *testing.T) {
	root := t.TempDir()
	f := writeFixture(t, root, "tiny")
	plan := Plan{
		Fixtures:     []Fixture{f},
		Scenarios:    []Scenario{ScenarioCold},
		Runs:         3,
		GomposerPath: "cgo",
		ComposerPath: "co",
	}
	durations := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 50 * time.Millisecond,
		200 * time.Millisecond, 300 * time.Millisecond, 1000 * time.Millisecond}
	fr := &fakeRunner{fn: func(idx int, _, _ string) (time.Duration, error) {
		return durations[idx], nil
	}}
	results, err := Run(context.Background(), plan, fr)
	if err != nil {
		t.Fatal(err)
	}
	// First three calls go to whichever tool we run first; we don't care which
	// one — but each tool gets exactly three samples and the median of
	// {10,20,50}=20ms and {200,300,1000}=300ms must both appear.
	medians := map[time.Duration]bool{}
	for _, r := range results {
		medians[r.Median] = true
	}
	if !medians[20*time.Millisecond] || !medians[300*time.Millisecond] {
		t.Errorf("results = %+v, want medians 20ms and 300ms", results)
	}
}

func TestRunSurfacesErrorFromCmdRunner(t *testing.T) {
	root := t.TempDir()
	f := writeFixture(t, root, "tiny")
	plan := Plan{
		Fixtures:     []Fixture{f},
		Scenarios:    []Scenario{ScenarioCold},
		Runs:         1,
		GomposerPath: "cgo",
		ComposerPath: "co",
	}
	fr := &fakeRunner{fn: func(int, string, string) (time.Duration, error) {
		return 0, errors.New("simulated install failure")
	}}
	if _, err := Run(context.Background(), plan, fr); err == nil {
		t.Error("expected error from failing CmdRunner")
	}
}

func TestRunDoesNotMutateOriginalFixture(t *testing.T) {
	root := t.TempDir()
	f := writeFixture(t, root, "tiny")
	plan := Plan{
		Fixtures:     []Fixture{f},
		Scenarios:    []Scenario{ScenarioCold},
		Runs:         1,
		GomposerPath: "cgo",
		ComposerPath: "co",
	}
	if _, err := Run(context.Background(), plan, &fakeRunner{}); err != nil {
		t.Fatal(err)
	}
	// Original fixture must still exist exactly as written.
	data, err := os.ReadFile(filepath.Join(f.Path, "composer.json"))
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	if !bytesContains(data, "psr/log") {
		t.Error("original fixture mutated")
	}
}

func bytesContains(b []byte, s string) bool {
	return len(b) >= len(s) && string(b) != "" && (string(b) == s || (len(b) > 0 && stringIndex(string(b), s) >= 0))
}

func stringIndex(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestExecCmdRunnerImplementsInterface(t *testing.T) {
	var _ CmdRunner = &execCmdRunner{}
}
