# Stage 3 / Plan 1: Benchmark harness (`cmd/bench`)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a `cmd/bench` Go binary that runs `composer-go install` and `composer install` over a fixed fixture corpus and reports cold/warm/lock-unchanged wall times. Output is a markdown table suitable for pasting into the README. The harness is the foundation for the Stage 3 acceptance bar from the design spec ("warm-cache and lock-unchanged installs >=5x faster than Composer; cold installs >=2x faster"). After this plan, contributors run `go run ./cmd/bench --corpus cmd/bench/testdata/corpus --runs 5` on their own machines and produce reproducible numbers we can quote in the README and the spec.

**Architecture:**

- The bench binary is a self-contained CLI separate from `cmd/composer-go`. It shells out to two binaries (the user's `composer-go` build and `composer`) and times their wall clock with `time.Now()` around `exec.Cmd.Run()`.
- Each fixture is a directory under `cmd/bench/testdata/corpus/` containing a `composer.json`. Fixtures are committed to the repository so benchmarks are reproducible. The harness copies a fixture into a temp directory before every run; the original under `testdata/` is never mutated.
- For each `(fixture, scenario, tool)` tuple the runner repeats `--runs N` times and reports the **median** wall time. Median (not mean) is robust to one slow outlier from network jitter or a cold OS file cache.
- The three scenarios are scripted by `prepareScenario(scenario, dir, tool)`:
  - **cold:** `rm -rf vendor composer-go.lock composer.lock` before *each* run. Forces a full resolve + download.
  - **warm:** run the tool once before timing to warm the metadata HTTP cache and the content-addressed package store. Before each timed run, only `rm -rf vendor` is removed; lockfile and caches stay.
  - **lock-unchanged:** run the tool once before timing to populate everything. Each timed run starts from a fully populated state and should be near-instant — this is the scenario where composer-go's resolution-result cache shines.
- Output rendering lives in `report.go`. The default mode is markdown; columns are `Fixture | Scenario | composer-go | composer | speed-up`. Speed-up is `composer / composer-go` rounded to one decimal place. Sorted by fixture name then scenario in the canonical order `cold, warm, lock-unchanged`.
- Tests live alongside the source. **No test in CI invokes a real composer or composer-go binary.** Instead, `runner.go` accepts an injectable command runner; tests pass a fake that returns canned durations.

**Tech Stack:** Go 1.22+, standard library only (`os/exec`, `time`, `flag`, `encoding/json`, `path/filepath`, `sort`). No new third-party deps.

**Depends on:**
- **Stage 1 complete.** `composer-go install` works end-to-end on Packagist projects and the four cache layers exist.
- **Stage 2 complete.** Real-world Laravel and Symfony skeletons install. Without Stage 2 the `laravel-skeleton` and `symfony-skeleton` fixtures cannot resolve (platform reqs + VCS metadata + classmap autoloader are all required).
- The harness does not import any `internal/` package — it talks to composer-go strictly through its CLI surface, exactly as a user would. This is a deliberate boundary so benchmarks measure the same thing users experience.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `cmd/bench/main.go` | CLI entrypoint: parse flags, load corpus, dispatch runner, print report |
| `cmd/bench/main_test.go` | Smoke test: bench binary builds and `--help` exits 0 |
| `cmd/bench/corpus.go` | `Fixture` struct, `LoadCorpus(dir)` walks a corpus directory |
| `cmd/bench/corpus_test.go` | Unit tests for corpus loading + validation |
| `cmd/bench/runner.go` | `Run(ctx, plan, exec) ([]Result, error)`; `Plan`, `Scenario`, `Tool`, `Result` types; `CmdRunner` interface |
| `cmd/bench/runner_test.go` | Unit tests using a fake `CmdRunner`; covers prep steps, median, error surface |
| `cmd/bench/report.go` | `RenderMarkdown(results []Result) string` |
| `cmd/bench/report_test.go` | Snapshot-style tests for table formatting and sorting |
| `cmd/bench/testdata/corpus/tiny-psrlog/composer.json` | Minimal fixture: `psr/log: ^3.0` |
| `cmd/bench/testdata/corpus/monolog/composer.json` | `monolog/monolog: ^3.0` |
| `cmd/bench/testdata/corpus/laravel-skeleton/composer.json` | Subset copy of `laravel/laravel`'s composer.json |
| `cmd/bench/testdata/corpus/symfony-skeleton/composer.json` | Subset copy of `symfony/skeleton`'s composer.json |

---

## Task 1: Corpus fixtures

**Files:**
- Create: `cmd/bench/testdata/corpus/tiny-psrlog/composer.json`
- Create: `cmd/bench/testdata/corpus/monolog/composer.json`
- Create: `cmd/bench/testdata/corpus/laravel-skeleton/composer.json`
- Create: `cmd/bench/testdata/corpus/symfony-skeleton/composer.json`

These fixtures are committed verbatim. Bench tests do not exercise them; the harness copies them into a temp directory at runtime. Keeping them under `testdata/` (rather than a top-level `bench/`) makes Go's own tooling skip them during package compilation.

- [ ] **Step 1: Create `tiny-psrlog`**

```json
{
  "name": "composer-go-bench/tiny-psrlog",
  "type": "library",
  "description": "Single-package leaf fixture for composer-go benchmarks.",
  "require": {
    "psr/log": "^3.0"
  }
}
```

- [ ] **Step 2: Create `monolog`**

```json
{
  "name": "composer-go-bench/monolog",
  "type": "library",
  "description": "Small real-world fixture exercising 3-4 transitive deps.",
  "require": {
    "monolog/monolog": "^3.0"
  }
}
```

- [ ] **Step 3: Create `laravel-skeleton`**

Copy a representative subset of `laravel/laravel`'s `composer.json`. Pin the major versions to what the current Laravel skeleton ships with at the time you create the fixture; a comment in the file or a sibling `README` is fine but not required. The point is a stable reference, not a moving target.

```json
{
  "name": "composer-go-bench/laravel-skeleton",
  "type": "project",
  "description": "Subset of laravel/laravel for benchmarking.",
  "require": {
    "php": "^8.2",
    "laravel/framework": "^11.0",
    "laravel/tinker": "^2.9"
  },
  "require-dev": {
    "fakerphp/faker": "^1.23",
    "laravel/pint": "^1.13",
    "mockery/mockery": "^1.6",
    "nunomaduro/collision": "^8.1",
    "phpunit/phpunit": "^11.0.1"
  },
  "minimum-stability": "stable",
  "prefer-stable": true
}
```

- [ ] **Step 4: Create `symfony-skeleton`**

```json
{
  "name": "composer-go-bench/symfony-skeleton",
  "type": "project",
  "description": "Subset of symfony/skeleton for benchmarking.",
  "require": {
    "php": "^8.2",
    "symfony/console": "^7.0",
    "symfony/dotenv": "^7.0",
    "symfony/flex": "^2.4",
    "symfony/framework-bundle": "^7.0",
    "symfony/runtime": "^7.0",
    "symfony/yaml": "^7.0"
  },
  "minimum-stability": "stable",
  "prefer-stable": true
}
```

- [ ] **Step 5: Commit**

```bash
git add cmd/bench/testdata/corpus
git commit -m "feat(bench): add fixture corpus (tiny-psrlog, monolog, laravel, symfony)"
```

---

## Task 2: Corpus loader

**Files:**
- Create: `cmd/bench/corpus.go`
- Create: `cmd/bench/corpus_test.go`

`LoadCorpus` walks a directory and returns a sorted `[]Fixture`. Each fixture is one immediate subdirectory containing `composer.json`. Hidden directories (leading `.`) are skipped. Sorting is stable by name so report output is deterministic.

- [ ] **Step 1: Write the failing test**

Create `cmd/bench/corpus_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadCorpusReturnsSortedFixtures(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zeta", "alpha", "mid"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "composer.json"),
			[]byte(`{"name":"x/y"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadCorpus(root)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	names := make([]string, len(got))
	for i, f := range got {
		names[i] = f.Name
	}
	want := []string{"alpha", "mid", "zeta"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

func TestLoadCorpusSkipsDirsWithoutComposerJSON(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "no-manifest"), 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(root, "good")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(good, "composer.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCorpus(root)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 1 || got[0].Name != "good" {
		t.Errorf("got %+v, want one fixture named good", got)
	}
}

func TestLoadCorpusSkipsHiddenDirs(t *testing.T) {
	root := t.TempDir()
	hidden := filepath.Join(root, ".cache")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "composer.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCorpus(root)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want zero fixtures, got %+v", got)
	}
}

func TestLoadCorpusErrorsOnMissingRoot(t *testing.T) {
	if _, err := LoadCorpus(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("expected error on missing root")
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./cmd/bench/...`

Expected: build error on `LoadCorpus`, `Fixture`.

- [ ] **Step 3: Implement loader**

Create `cmd/bench/corpus.go`:

```go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Fixture is a single benchmark project: a directory containing composer.json.
type Fixture struct {
	// Name is the directory's base name (e.g. "tiny-psrlog").
	Name string
	// Path is the absolute path to the fixture directory under testdata/corpus.
	Path string
}

// LoadCorpus walks root and returns one Fixture per immediate subdirectory
// that contains a composer.json. Hidden directories (names starting with '.')
// are skipped. Results are sorted by Name for determinism.
func LoadCorpus(root string) ([]Fixture, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("bench: read corpus dir %q: %w", root, err)
	}
	var fixtures []Fixture
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "composer.json")); err != nil {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("bench: abs path for %q: %w", dir, err)
		}
		fixtures = append(fixtures, Fixture{Name: e.Name(), Path: abs})
	}
	sort.Slice(fixtures, func(i, j int) bool {
		return fixtures[i].Name < fixtures[j].Name
	})
	return fixtures, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/bench/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/bench/corpus.go cmd/bench/corpus_test.go
git commit -m "feat(bench): corpus loader"
```

---

## Task 3: Runner types and `CmdRunner` interface

**Files:**
- Create: `cmd/bench/runner.go`
- Create: `cmd/bench/runner_test.go`

We start by defining the data types and the injectable command-runner interface. The actual scenario logic ships in Tasks 4 and 5. Splitting the file lets us TDD each piece.

- [ ] **Step 1: Write the failing test**

Create `cmd/bench/runner_test.go`:

```go
package main

import (
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
```

- [ ] **Step 2: Verify failure**

Run: `go test ./cmd/bench/...`

Expected: build error on `Scenario`, `ParseScenario`, `median`, etc.

- [ ] **Step 3: Implement types**

Create `cmd/bench/runner.go`:

```go
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
	Fixtures        []Fixture
	Scenarios       []Scenario
	Runs            int
	ComposerGoPath  string
	ComposerPath    string
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/bench/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/bench/runner.go cmd/bench/runner_test.go
git commit -m "feat(bench): runner types, CmdRunner interface, median helper"
```

---

## Task 4: Scenario preparation

**Files:**
- Modify: `cmd/bench/runner.go`
- Modify: `cmd/bench/runner_test.go`

Each scenario maps to a deterministic filesystem-prep step. Cold rips out vendor, both lockfiles, and (separately) any cached `.composer-go/store` so the run actually re-downloads. Warm and lock-unchanged require a "warmup" install once before the timed runs; that warmup is also a `CmdRunner.Run` call.

- [ ] **Step 1: Append failing test**

Append to `cmd/bench/runner_test.go`:

```go
import (
	"os"
	"path/filepath"
)

func TestPrepareColdRemovesVendorAndLocks(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{
		filepath.Join(dir, "vendor", "psr", "log"),
		filepath.Join(dir, "composer.lock"),
		filepath.Join(dir, "composer-go.lock"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareScenario(ScenarioCold, dir); err != nil {
		t.Fatalf("prepareScenario: %v", err)
	}
	for _, p := range []string{"vendor", "composer.lock", "composer-go.lock"} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s should be removed: %v", p, err)
		}
	}
}

func TestPrepareWarmRemovesOnlyVendor(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "composer-go.lock"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareScenario(ScenarioWarm, dir); err != nil {
		t.Fatalf("prepareScenario: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor")); !os.IsNotExist(err) {
		t.Error("vendor should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "composer-go.lock")); err != nil {
		t.Error("composer-go.lock should be preserved on warm")
	}
}

func TestPrepareLockUnchangedTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"vendor/keep", "composer-go.lock"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareScenario(ScenarioLockUnchanged, dir); err != nil {
		t.Fatalf("prepareScenario: %v", err)
	}
	for _, p := range []string{"vendor/keep", "composer-go.lock"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s should be preserved on lock-unchanged: %v", p, err)
		}
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./cmd/bench/...`

Expected: build error on `prepareScenario`.

- [ ] **Step 3: Implement prepareScenario**

Append to `cmd/bench/runner.go`:

```go
import (
	"os"
	"path/filepath"
)

// prepareScenario brings dir into the pre-run state for scenario s. It is
// called BEFORE every timed run (not just once per scenario) so that cold
// truly measures cold every time.
func prepareScenario(s Scenario, dir string) error {
	switch s {
	case ScenarioCold:
		for _, rel := range []string{"vendor", "composer.lock", "composer-go.lock"} {
			if err := os.RemoveAll(filepath.Join(dir, rel)); err != nil {
				return fmt.Errorf("bench: prepare cold: rm %s: %w", rel, err)
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/bench/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/bench/runner.go cmd/bench/runner_test.go
git commit -m "feat(bench): scenario filesystem preparation"
```

---

## Task 5: The `Run` orchestration

**Files:**
- Modify: `cmd/bench/runner.go`
- Modify: `cmd/bench/runner_test.go`

`Run` iterates `(fixture, scenario, tool)` tuples. For each tuple it: copies the fixture into a fresh temp directory, optionally runs a warmup, then runs the timed loop, computing the median. It uses the injected `CmdRunner` so tests can simulate the binaries without invoking them.

- [ ] **Step 1: Append failing test**

Append to `cmd/bench/runner_test.go`:

```go
import (
	"context"
	"errors"
	"sync"
)

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
}

func (f *fakeRunner) Run(_ context.Context, bin, workDir string) (time.Duration, error) {
	f.mu.Lock()
	idx := len(f.calls)
	f.calls = append(f.calls, fakeCall{bin: bin, workDir: workDir})
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
		Fixtures:       []Fixture{f},
		Scenarios:      []Scenario{ScenarioCold, ScenarioWarm, ScenarioLockUnchanged},
		Runs:           3,
		ComposerGoPath: "composer-go",
		ComposerPath:   "composer",
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

func TestRunReportsMedian(t *testing.T) {
	root := t.TempDir()
	f := writeFixture(t, root, "tiny")
	plan := Plan{
		Fixtures:       []Fixture{f},
		Scenarios:      []Scenario{ScenarioCold},
		Runs:           3,
		ComposerGoPath: "cgo",
		ComposerPath:   "co",
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
		Fixtures:       []Fixture{f},
		Scenarios:      []Scenario{ScenarioCold},
		Runs:           1,
		ComposerGoPath: "cgo",
		ComposerPath:   "co",
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
		Fixtures:       []Fixture{f},
		Scenarios:      []Scenario{ScenarioCold},
		Runs:           1,
		ComposerGoPath: "cgo",
		ComposerPath:   "co",
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
```

- [ ] **Step 2: Verify failure**

Run: `go test ./cmd/bench/...`

Expected: build error on `Run`.

- [ ] **Step 3: Implement Run + helpers**

Append to `cmd/bench/runner.go`:

```go
import (
	"io"
)

// Run executes plan and returns one Result per (fixture, scenario, tool).
//
// For every tuple:
//   1. Copy the fixture into a fresh temp directory.
//   2. If the scenario needs a warmup, run one untimed install with the tool.
//   3. Loop plan.Runs times: prepareScenario → CmdRunner.Run → record duration.
//   4. Compute the median of the recorded durations.
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
		{ToolComposerGo, plan.ComposerGoPath},
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
	work, err := os.MkdirTemp("", "composer-go-bench-")
	if err != nil {
		return Result{}, fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(work)

	if err := copyDir(fx.Path, work); err != nil {
		return Result{}, fmt.Errorf("seed fixture: %w", err)
	}

	if scenarioNeedsWarmup(sc) {
		if _, err := runner.Run(ctx, bin, work); err != nil {
			return Result{}, fmt.Errorf("warmup: %w", err)
		}
	}

	samples := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		if err := prepareScenario(sc, work); err != nil {
			return Result{}, err
		}
		d, err := runner.Run(ctx, bin, work)
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/bench/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/bench/runner.go cmd/bench/runner_test.go
git commit -m "feat(bench): Run orchestration with fixture isolation and median timing"
```

---

## Task 6: Markdown report rendering

**Files:**
- Create: `cmd/bench/report.go`
- Create: `cmd/bench/report_test.go`

`RenderMarkdown(results)` produces a table sorted by fixture (alphabetical) and scenario (canonical `cold,warm,lock-unchanged` order). Each row pairs the composer-go and composer medians for the same `(fixture, scenario)` and computes the speed-up.

- [ ] **Step 1: Write the failing test**

Create `cmd/bench/report_test.go`:

```go
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
```

- [ ] **Step 2: Verify failure**

Run: `go test ./cmd/bench/...`

Expected: build error on `RenderMarkdown`.

- [ ] **Step 3: Implement renderer**

Create `cmd/bench/report.go`:

```go
package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// RenderMarkdown returns a markdown table with one row per (fixture, scenario).
// composer-go and composer medians are paired side by side; speed-up is
// composer / composer-go rounded to one decimal.
//
// Empty results produce a header-only table — useful for CI smoke tests.
func RenderMarkdown(results []Result) string {
	var b strings.Builder
	b.WriteString("| Fixture | Scenario | composer-go | composer | speed-up |\n")
	b.WriteString("|---------|----------|-------------|----------|----------|\n")

	type key struct {
		fixture  string
		scenario Scenario
	}
	type pair struct {
		cgo *Result
		co  *Result
	}
	pairs := map[key]*pair{}
	for i := range results {
		r := &results[i]
		k := key{fixture: r.Fixture, scenario: r.Scenario}
		p, ok := pairs[k]
		if !ok {
			p = &pair{}
			pairs[k] = p
		}
		switch r.Tool {
		case ToolComposerGo:
			p.cgo = r
		case ToolComposer:
			p.co = r
		}
	}

	keys := make([]key, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	scenarioRank := map[Scenario]int{
		ScenarioCold:          0,
		ScenarioWarm:          1,
		ScenarioLockUnchanged: 2,
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].fixture != keys[j].fixture {
			return keys[i].fixture < keys[j].fixture
		}
		return scenarioRank[keys[i].scenario] < scenarioRank[keys[j].scenario]
	})

	for _, k := range keys {
		p := pairs[k]
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
			k.fixture, k.scenario,
			formatDuration(p.cgo), formatDuration(p.co),
			formatSpeedup(p.cgo, p.co))
	}
	return b.String()
}

func formatDuration(r *Result) string {
	if r == nil {
		return "n/a"
	}
	return humanDuration(r.Median)
}

// humanDuration prints a duration with a small number of significant digits.
// e.g. 1.234s, 234ms, 12.3ms, 4.5us.
func humanDuration(d time.Duration) string {
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.2gs", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d >= time.Microsecond:
		return fmt.Sprintf("%dus", d.Microseconds())
	default:
		return d.String()
	}
}

func formatSpeedup(cgo, co *Result) string {
	if cgo == nil || co == nil || cgo.Median <= 0 {
		return "n/a"
	}
	x := float64(co.Median) / float64(cgo.Median)
	return fmt.Sprintf("%.1fx", x)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/bench/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/bench/report.go cmd/bench/report_test.go
git commit -m "feat(bench): markdown report renderer with paired tools and speed-up column"
```

---

## Task 7: Production `CmdRunner` (real `os/exec`)

**Files:**
- Modify: `cmd/bench/runner.go`

The production implementation shells out to the binary named by `bin` with `install` as the only argument. Stdout and stderr are discarded by default — the bench output is a clean table, not a wall of composer logs. We capture stderr only on failure for diagnostics.

- [ ] **Step 1: Append failing test (build-only smoke)**

We deliberately do NOT exercise the real binary in CI. The only test we add is a constructor smoke test:

Append to `cmd/bench/runner_test.go`:

```go
func TestExecCmdRunnerImplementsInterface(t *testing.T) {
	var _ CmdRunner = &execCmdRunner{}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./cmd/bench/...`

Expected: build error on `execCmdRunner`.

- [ ] **Step 3: Implement execCmdRunner**

Append to `cmd/bench/runner.go`:

```go
import (
	"bytes"
	"os/exec"
)

// execCmdRunner is the production CmdRunner. It runs `<bin> install` in
// workDir, discards stdout, captures stderr only for error reporting, and
// times the entire exec.Cmd.Run() call.
type execCmdRunner struct{}

func (execCmdRunner) Run(ctx context.Context, bin, workDir string) (time.Duration, error) {
	if bin == "" {
		return 0, fmt.Errorf("bench: empty binary path")
	}
	cmd := exec.CommandContext(ctx, bin, "install")
	cmd.Dir = workDir
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/bench/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/bench/runner.go cmd/bench/runner_test.go
git commit -m "feat(bench): execCmdRunner shells out to the real binary"
```

---

## Task 8: CLI entrypoint

**Files:**
- Create: `cmd/bench/main.go`
- Create: `cmd/bench/main_test.go`

`main.go` parses flags, validates the corpus and binary paths, dispatches to `Run`, and prints `RenderMarkdown` to stdout. Errors go to stderr with a non-zero exit code.

- [ ] **Step 1: Write the failing test**

Create `cmd/bench/main_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestParseFlagsDefaults(t *testing.T) {
	p, err := parseFlags([]string{"--corpus", "/some/dir"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if p.Corpus != "/some/dir" {
		t.Errorf("Corpus = %q", p.Corpus)
	}
	if p.Runs != 3 {
		t.Errorf("Runs = %d, want 3", p.Runs)
	}
	if len(p.Scenarios) != 3 {
		t.Errorf("Scenarios = %v, want all three", p.Scenarios)
	}
}

func TestParseFlagsCustomScenarios(t *testing.T) {
	p, err := parseFlags([]string{"--corpus", "/x", "--scenarios", "cold,warm"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if len(p.Scenarios) != 2 {
		t.Errorf("Scenarios = %v, want [cold warm]", p.Scenarios)
	}
}

func TestParseFlagsRejectsUnknownScenario(t *testing.T) {
	if _, err := parseFlags([]string{"--corpus", "/x", "--scenarios", "frozen"}); err == nil {
		t.Error("expected error on unknown scenario")
	}
	if _, err := parseFlags([]string{"--corpus", "/x", "--scenarios", "frozen"}); err != nil {
		if !strings.Contains(err.Error(), "frozen") {
			t.Errorf("error should mention 'frozen': %v", err)
		}
	}
}

func TestParseFlagsRequiresCorpus(t *testing.T) {
	if _, err := parseFlags(nil); err == nil {
		t.Error("expected error when --corpus missing")
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./cmd/bench/...`

Expected: build error on `parseFlags`.

- [ ] **Step 3: Implement main.go**

Create `cmd/bench/main.go`:

```go
// Command bench measures composer-go install vs composer install over a fixed
// corpus and prints a markdown report. It is a manual tool: nothing in CI
// invokes the real binaries.
//
// Usage:
//
//	go run ./cmd/bench \
//	  --corpus cmd/bench/testdata/corpus \
//	  --composer-go ./composer-go \
//	  --composer /usr/local/bin/composer \
//	  --runs 5 \
//	  --scenarios cold,warm,lock-unchanged
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// flagPlan is the parsed CLI form. We separate it from runner.Plan so flag
// parsing has no dependency on the corpus loader.
type flagPlan struct {
	Corpus     string
	ComposerGo string
	Composer   string
	Runs       int
	Scenarios  []Scenario
}

func parseFlags(argv []string) (*flagPlan, error) {
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	corpus := fs.String("corpus", "", "directory of fixtures (each subdirectory must contain composer.json)")
	composerGo := fs.String("composer-go", "composer-go", "path to the composer-go binary")
	composer := fs.String("composer", "composer", "path to the composer binary")
	runs := fs.Int("runs", 3, "number of timed runs per (fixture, scenario, tool); median is reported")
	scenariosCSV := fs.String("scenarios", "cold,warm,lock-unchanged",
		"comma-separated subset of cold,warm,lock-unchanged")

	if err := fs.Parse(argv); err != nil {
		return nil, err
	}
	if *corpus == "" {
		return nil, fmt.Errorf("--corpus is required")
	}
	if *runs < 1 {
		return nil, fmt.Errorf("--runs must be >=1")
	}

	var scs []Scenario
	for _, raw := range strings.Split(*scenariosCSV, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		sc, err := ParseScenario(raw)
		if err != nil {
			return nil, err
		}
		scs = append(scs, sc)
	}
	if len(scs) == 0 {
		return nil, fmt.Errorf("--scenarios produced empty list")
	}

	return &flagPlan{
		Corpus:     *corpus,
		ComposerGo: *composerGo,
		Composer:   *composer,
		Runs:       *runs,
		Scenarios:  scs,
	}, nil
}

func main() {
	if err := mainImpl(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(1)
	}
}

func mainImpl(argv []string) error {
	fp, err := parseFlags(argv)
	if err != nil {
		return err
	}
	fixtures, err := LoadCorpus(fp.Corpus)
	if err != nil {
		return err
	}
	if len(fixtures) == 0 {
		return fmt.Errorf("no fixtures found under %q", fp.Corpus)
	}

	plan := Plan{
		Fixtures:       fixtures,
		Scenarios:      fp.Scenarios,
		Runs:           fp.Runs,
		ComposerGoPath: fp.ComposerGo,
		ComposerPath:   fp.Composer,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	results, err := Run(ctx, plan, execCmdRunner{})
	if err != nil {
		return err
	}
	fmt.Print(RenderMarkdown(results))
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/bench/...`

Expected: PASS.

- [ ] **Step 5: Build smoke**

Run: `go build ./cmd/bench`

Expected: builds without errors.

- [ ] **Step 6: Commit**

```bash
git add cmd/bench/main.go cmd/bench/main_test.go
git commit -m "feat(bench): CLI entrypoint with --corpus/--runs/--scenarios flags"
```

---

## Task 9: End-to-end dry run with the test corpus

**Files:**
- None (manual verification only)

This task verifies the harness works against the committed corpus using a real composer-go binary. We do NOT add this as a CI test (the spec is explicit that benchmarks are manual). The verification is documented here so contributors have a known-good recipe.

- [ ] **Step 1: Build composer-go**

```bash
go build -o composer-go ./cmd/composer-go
```

- [ ] **Step 2: Run the bench against tiny-psrlog only**

```bash
go run ./cmd/bench \
  --corpus cmd/bench/testdata/corpus \
  --composer-go ./composer-go \
  --composer "$(which composer)" \
  --runs 3 \
  --scenarios cold,warm,lock-unchanged
```

Expected: a markdown table on stdout. composer-go's `lock-unchanged` row should be in the low milliseconds (resolution-result cache hit). composer-go's `warm` row should be much faster than composer's `warm`. composer-go's `cold` row should be at least as fast as composer's `cold`.

- [ ] **Step 3: Spot-check the speed-up column**

The Stage-3 acceptance bar from the design spec is:

> warm-cache and lock-unchanged installs >=5x faster than Composer; cold installs >=2x faster.

If any of those targets is missed at this point, the work is **not** "fix the benchmark." It is to surface the regression in a follow-up Stage-3 plan (concurrency tuning, speculative prefetch, etc.). The bench is the measurement; closing the gap is everything else in Stage 3.

- [ ] **Step 4: No commit needed**

This task is verification only. Move on.

---

## Task 10: README pointer

**Files:**
- Modify: `README.md` (if it exists; if not, create a minimal one)

A short pointer in the README so future contributors discover the harness without reading every plan.

- [ ] **Step 1: Add a "Benchmarks" section**

Append to (or create) `README.md`:

```markdown
## Benchmarks

`cmd/bench` measures composer-go vs composer over a small fixture corpus and
prints a markdown table. It is run manually; CI does not invoke composer.

```sh
go build -o composer-go ./cmd/composer-go
go run ./cmd/bench \
  --corpus cmd/bench/testdata/corpus \
  --composer-go ./composer-go \
  --composer "$(which composer)" \
  --runs 5
```

The harness reports the median of N runs per `(fixture, scenario, tool)`.
Scenarios:

- **cold:** `vendor/`, `composer.lock`, and `composer-go.lock` removed before
  every run.
- **warm:** lockfile and on-disk caches preserved; only `vendor/` is removed.
- **lock-unchanged:** nothing is removed; the timed run starts fully populated.
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs(readme): point at cmd/bench"
```

---

## Stage 3 / Plan 1 acceptance check

- [ ] `go test ./cmd/bench/...` passes offline. No test invokes a real composer or composer-go binary.
- [ ] `go build ./cmd/bench` produces a binary.
- [ ] `cmd/bench/testdata/corpus/` contains four fixtures: `tiny-psrlog`, `monolog`, `laravel-skeleton`, `symfony-skeleton`. Each has a `composer.json` and nothing else committed.
- [ ] `go run ./cmd/bench --corpus cmd/bench/testdata/corpus --composer-go ./composer-go --composer "$(which composer)" --runs 3` prints a markdown table with twelve data rows (4 fixtures * 3 scenarios) when run on a developer machine with both binaries available.
- [ ] Output rows are sorted by fixture (alphabetical) then scenario (`cold, warm, lock-unchanged`).
- [ ] Speed-up column is `composer / composer-go` rounded to one decimal place.
- [ ] `--scenarios cold` selects only the cold rows.
- [ ] `--runs 5` collects five samples per cell and reports their median.
- [ ] Scenario filesystem prep is correct: cold removes vendor + both lockfiles every run; warm removes only vendor; lock-unchanged removes nothing.
- [ ] The original fixture under `testdata/corpus/` is never mutated by a bench run.
- [ ] An `os/exec` failure from either binary surfaces with stderr context and aborts the entire bench (no silent "best 2 of 3").

If any item fails, fix forward in this plan before moving on to Stage 3 / Plan 2 (speculative prefetch).
