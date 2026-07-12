# Cache Controls Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `gomposer cache` command group — usage overview with per-layer sizes, raw path printing, and selective clearing — backed by a layer registry that becomes the single source of truth for cache subdirectory names.

**Architecture:** New `Layer` type + fixed registry in `internal/cache` (the package that owns `Root()`), with `Path()`/`Size()`/`Clear()` per layer. The orchestrator's four magic-string join sites switch to registry constants. A new cobra command group in `internal/cli` is a thin consumer.

**Tech Stack:** Go 1.25, stdlib + spf13/cobra (already a dependency). Tests with `go test`.

**Spec:** `docs/superpowers/specs/2026-07-12-cache-controls-design.md` — read it before starting.

## Global Constraints

- Go 1.25; no new dependencies.
- Layer registry (user-facing name → on-disk subdir): `store`→`store`, `metadata`→`packagist`, `resolution`→`resolution`, `vcs`→`vcs`, in exactly that display order.
- `Size` of a missing directory is 0, not an error. `Clear` of a missing directory is a no-op returning 0. `Clear` returns bytes freed (Size immediately before removal) and does NOT recreate the directory.
- No confirmation prompt on clear. Unknown layer names fail BEFORE anything is cleared. Duplicate layer args deduplicate, preserving first-mention order.
- Human-readable sizes: decimal units (B, kB, MB, GB; 1000-based), one decimal place (integers for plain B).
- `cache dir` prints exactly the root path + newline, even under `--quiet` (the path IS the output). `cache` and `cache clear` print nothing (except errors) under `--quiet`.
- `cache clear` with >1 layer cleared prints a `freed <total>` summary line; with exactly 1 layer it omits it. Layers freeing 0 bytes still print their line.
- Behavior of install/update must be untouched — the orchestrator swap changes where subdir strings come from, not their values.
- Cache-touching tests isolate with `t.Setenv("XDG_CACHE_HOME", t.TempDir())`.
- Commit messages: conventional-commit style, no `Co-Authored-By` trailer.

---

### Task 1: Layer registry in `internal/cache` + orchestrator swap

**Files:**
- Create: `internal/cache/layers.go`
- Test: `internal/cache/layers_test.go`
- Modify: `internal/orchestrator/pipeline.go:897,908,929` (three `filepath.Join(cacheRoot, "...")` literals)
- Modify: `internal/orchestrator/cachekey.go:47` (`filepath.Join(root, "resolution")`)

**Interfaces:**
- Consumes: `cache.Root()` (`internal/cache/dir.go:21`).
- Produces: exported vars `cache.LayerStore`, `cache.LayerMetadata`, `cache.LayerResolution`, `cache.LayerVCS` (each a `Layer{Name, Subdir string}`); `cache.Layers() []Layer`; `cache.LayerByName(name string) (Layer, bool)`; methods `(Layer).Path() (string, error)`, `(Layer).Size() (int64, error)`, `(Layer).Clear() (int64, error)`. Task 2 relies on all of these exact names.

- [ ] **Step 1: Write the failing tests**

Create `internal/cache/layers_test.go`:

```go
package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLayersRegistry(t *testing.T) {
	want := []Layer{
		{Name: "store", Subdir: "store"},
		{Name: "metadata", Subdir: "packagist"},
		{Name: "resolution", Subdir: "resolution"},
		{Name: "vcs", Subdir: "vcs"},
	}
	got := Layers()
	if len(got) != len(want) {
		t.Fatalf("Layers() returned %d layers, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Layers()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLayerByName(t *testing.T) {
	for _, l := range Layers() {
		got, ok := LayerByName(l.Name)
		if !ok || got != l {
			t.Errorf("LayerByName(%q) = %+v, %v", l.Name, got, ok)
		}
	}
	if _, ok := LayerByName("bogus"); ok {
		t.Error("LayerByName(\"bogus\") should return false")
	}
}

func TestLayerPathJoinsRoot(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	dir, err := LayerMetadata.Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if want := filepath.Join(xdg, "gomposer", "packagist"); dir != want {
		t.Errorf("Path() = %q, want %q", dir, want)
	}
}

func TestLayerSizeSumsNestedFiles(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir, err := LayerStore.Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.zip"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "b.zip"), []byte("1234567"), 0o644); err != nil {
		t.Fatal(err)
	}
	size, err := LayerStore.Size()
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	if size != 12 {
		t.Errorf("Size() = %d, want 12 (5 + 7 nested)", size)
	}
}

func TestLayerSizeMissingDirIsZero(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	size, err := LayerVCS.Size()
	if err != nil {
		t.Fatalf("Size on missing dir: %v", err)
	}
	if size != 0 {
		t.Errorf("Size() = %d, want 0 for missing dir", size)
	}
}

func TestLayerClearRemovesAndReports(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir, err := LayerMetadata.Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.json"), []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	freed, err := LayerMetadata.Clear()
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if freed != 3 {
		t.Errorf("Clear() freed %d, want 3", freed)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("layer dir still exists after Clear")
	}
	// Second clear: missing dir is a no-op returning 0.
	freed2, err := LayerMetadata.Clear()
	if err != nil {
		t.Fatalf("second Clear: %v", err)
	}
	if freed2 != 0 {
		t.Errorf("second Clear() freed %d, want 0", freed2)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cache -run TestLayer -v`
Expected: FAIL to compile with `undefined: Layer`

- [ ] **Step 3: Write the implementation**

Create `internal/cache/layers.go`:

```go
package cache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Layer identifies one cache subdirectory. Name is the user-facing
// identifier used in CLI arguments and output; Subdir is the on-disk
// directory name under Root(). They differ for "metadata", whose
// directory is "packagist" for historical layout reasons but whose
// user-facing concept is registry metadata.
type Layer struct {
	Name   string
	Subdir string
}

// The four cache layers. Every consumer of a cache subdirectory must
// source the name from here — see Layers() for the display-ordered set.
var (
	LayerStore      = Layer{Name: "store", Subdir: "store"}           // content-addressed package archives
	LayerMetadata   = Layer{Name: "metadata", Subdir: "packagist"}    // registry HTTP + parsed metadata
	LayerResolution = Layer{Name: "resolution", Subdir: "resolution"} // resolver result cache
	LayerVCS        = Layer{Name: "vcs", Subdir: "vcs"}               // VCS clone cache
)

// Layers returns the fixed registry in display order.
func Layers() []Layer {
	return []Layer{LayerStore, LayerMetadata, LayerResolution, LayerVCS}
}

// LayerByName looks a layer up by its user-facing name.
func LayerByName(name string) (Layer, bool) {
	for _, l := range Layers() {
		if l.Name == name {
			return l, true
		}
	}
	return Layer{}, false
}

// Path returns the absolute directory for the layer (Root()/Subdir).
// It does not create the directory.
func (l Layer) Path() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, l.Subdir), nil
}

// Size returns the total bytes of all regular files under the layer's
// directory. A missing directory is 0 bytes, not an error. Walk errors
// on individual entries abort with the error — a partial sum would
// silently lie.
func (l Layer) Size() (int64, error) {
	dir, err := l.Path()
	if err != nil {
		return 0, err
	}
	var total int64
	err = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("cache: size %s: %w", l.Name, err)
	}
	return total, nil
}

// Clear removes the layer's directory tree and returns the bytes freed
// (its Size immediately before removal). Clearing a missing directory
// is a no-op returning 0. Consumers recreate their directories on
// demand (store.New, the packagist caches, resolutionCacheDir, and the
// VCS cache all MkdirAll on first use), so Clear does not recreate
// anything.
func (l Layer) Clear() (int64, error) {
	size, err := l.Size()
	if err != nil {
		return 0, err
	}
	dir, err := l.Path()
	if err != nil {
		return 0, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return 0, fmt.Errorf("cache: clear %s: %w", l.Name, err)
	}
	return size, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cache -v`
Expected: PASS (all new TestLayer* tests plus existing package tests)

- [ ] **Step 5: Commit the registry**

```bash
git add internal/cache/layers.go internal/cache/layers_test.go
git commit -m "feat(cache): layer registry with per-layer size and clear"
```

- [ ] **Step 6: Swap the orchestrator's magic strings**

In `internal/orchestrator/pipeline.go` (three sites inside `defaultDeps`, around lines 897, 908, 929):

```go
// before:
CacheRoot: filepath.Join(cacheRoot, "vcs"),
// after:
CacheRoot: filepath.Join(cacheRoot, cache.LayerVCS.Subdir),
```

```go
// before:
CacheDir: filepath.Join(cacheRoot, "packagist"),
// after:
CacheDir: filepath.Join(cacheRoot, cache.LayerMetadata.Subdir),
```

```go
// before:
s, err := store.New(filepath.Join(cacheRoot, "store"))
// after:
s, err := store.New(filepath.Join(cacheRoot, cache.LayerStore.Subdir))
```

In `internal/orchestrator/cachekey.go` (inside `resolutionCacheDir`, line 47):

```go
// before:
d := filepath.Join(root, "resolution")
// after:
d := filepath.Join(root, cache.LayerResolution.Subdir)
```

Both files already import `github.com/torstendittmann/gomposer/internal/cache`; no import changes needed.

- [ ] **Step 7: Verify the swap changed nothing**

Run: `go build ./... && go test ./internal/orchestrator -count=1`
Expected: build ok, all tests pass (paths are byte-identical strings; the suite exercising install/update proves it)

- [ ] **Step 8: Commit the swap**

```bash
git add internal/orchestrator/pipeline.go internal/orchestrator/cachekey.go
git commit -m "refactor(orchestrator): source cache subdir names from the layer registry"
```

---

### Task 2: `gomposer cache` command group

**Files:**
- Create: `internal/cli/cache.go`
- Modify: `internal/cli/root.go:41` (add `root.AddCommand(newCacheCmd())` after the update command)
- Test: `internal/cli/cache_test.go`

**Interfaces:**
- Consumes (from Task 1): `cache.Root() (string, error)`; `cache.Layers() []cache.Layer`; `cache.LayerByName(string) (cache.Layer, bool)`; `(cache.Layer).Path() (string, error)`, `.Size() (int64, error)`, `.Clear() (int64, error)`; struct fields `Layer.Name`. Also the existing package-level `flagQuiet bool` in `internal/cli/root.go:18`.
- Produces: `newCacheCmd() *cobra.Command` (registered in root); unexported helpers `runCacheInfo`, `resolveLayerArgs`, `humanBytes` — final, no later task.

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/cache_test.go`:

```go
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/cache"
)

// seedLayer writes a file of n bytes into the named layer's directory.
func seedLayer(t *testing.T, name, file string, n int) {
	t.Helper()
	l, ok := cache.LayerByName(name)
	if !ok {
		t.Fatalf("unknown layer %q", name)
	}
	dir, err := l.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), bytes.Repeat([]byte("x"), n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runCache executes the CLI with args and returns captured output.
func runCache(t *testing.T, args ...string) (string, error) {
	t.Helper()
	flagQuiet = false // reset shared flag state between tests
	var out bytes.Buffer
	root := newRootCmd("dev")
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestCacheInfoListsLayersAndTotal(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	seedLayer(t, "store", "a.zip", 5)
	out, err := runCache(t, "cache")
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	for _, want := range []string{"store", "metadata", "resolution", "vcs", "total", "5 B"} {
		if !strings.Contains(out, want) {
			t.Errorf("cache output missing %q:\n%s", want, out)
		}
	}
}

func TestCacheDirPrintsOnlyPath(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	out, err := runCache(t, "cache", "dir")
	if err != nil {
		t.Fatalf("cache dir: %v", err)
	}
	if want := filepath.Join(xdg, "gomposer") + "\n"; out != want {
		t.Errorf("cache dir output = %q, want %q", out, want)
	}
}

func TestCacheClearAllReportsFreed(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	seedLayer(t, "store", "a.zip", 5)
	seedLayer(t, "metadata", "m.json", 3)
	out, err := runCache(t, "cache", "clear")
	if err != nil {
		t.Fatalf("cache clear: %v", err)
	}
	for _, want := range []string{"cleared store (5 B)", "cleared metadata (3 B)", "cleared resolution (0 B)", "cleared vcs (0 B)", "freed 8 B"} {
		if !strings.Contains(out, want) {
			t.Errorf("clear output missing %q:\n%s", want, out)
		}
	}
	for _, name := range []string{"store", "metadata"} {
		l, _ := cache.LayerByName(name)
		dir, _ := l.Path()
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s dir still exists after clear", name)
		}
	}
}

func TestCacheClearSelectiveLeavesOthers(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	seedLayer(t, "store", "a.zip", 5)
	seedLayer(t, "metadata", "m.json", 3)
	out, err := runCache(t, "cache", "clear", "metadata")
	if err != nil {
		t.Fatalf("cache clear metadata: %v", err)
	}
	if !strings.Contains(out, "cleared metadata (3 B)") {
		t.Errorf("clear output:\n%s", out)
	}
	if strings.Contains(out, "freed") {
		t.Errorf("single-layer clear must omit the total line:\n%s", out)
	}
	if size, _ := cache.LayerStore.Size(); size != 5 {
		t.Errorf("store layer touched by selective clear; size = %d, want 5", size)
	}
}

func TestCacheClearUnknownLayerFailsBeforeClearing(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	seedLayer(t, "store", "a.zip", 5)
	_, err := runCache(t, "cache", "clear", "store", "bogus")
	if err == nil || !strings.Contains(err.Error(), `unknown cache layer "bogus"`) {
		t.Fatalf("err = %v, want unknown-layer error naming bogus", err)
	}
	if size, _ := cache.LayerStore.Size(); size != 5 {
		t.Errorf("store cleared despite arg error; size = %d, want 5", size)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1000, "1.0 kB"},
		{1500, "1.5 kB"},
		{142_300_000, "142.3 MB"},
		{3_100_000_000, "3.1 GB"},
		{2_500_000_000_000, "2500.0 GB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.n); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli -run 'TestCache|TestHumanBytes' -v`
Expected: FAIL to compile with `undefined: humanBytes` (and unknown `cache` command at runtime once it compiles)

- [ ] **Step 3: Write the implementation**

Create `internal/cli/cache.go`:

```go
package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/gomposer/internal/cache"
)

func newCacheCmd() *cobra.Command {
	cacheCmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect the gomposer cache",
		Long:  "Prints the cache location and per-layer disk usage. Use `cache dir` for the raw path and `cache clear` to delete layers.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCacheInfo(cmd)
		},
	}
	cacheCmd.AddCommand(newCacheDirCmd())
	cacheCmd.AddCommand(newCacheClearCmd())
	return cacheCmd
}

func runCacheInfo(cmd *cobra.Command) error {
	if flagQuiet {
		return nil
	}
	root, err := cache.Root()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, root)
	var total int64
	for _, l := range cache.Layers() {
		size, err := l.Size()
		if err != nil {
			return err
		}
		total += size
		fmt.Fprintf(out, "  %-11s %9s\n", l.Name, humanBytes(size))
	}
	fmt.Fprintf(out, "  %-11s %9s\n", "total", humanBytes(total))
	return nil
}

func newCacheDirCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dir",
		Short: "Print the cache directory path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := cache.Root()
			if err != nil {
				return err
			}
			// The path IS the output, not decoration — print it even
			// under --quiet so `du -sh $(gomposer cache dir)` composes.
			fmt.Fprintln(cmd.OutOrStdout(), root)
			return nil
		},
	}
}

func newCacheClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear [layer...]",
		Short: "Clear cache layers (all layers when none are named)",
		RunE: func(cmd *cobra.Command, args []string) error {
			layers, err := resolveLayerArgs(args)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var total int64
			for _, l := range layers {
				freed, err := l.Clear()
				if err != nil {
					return err
				}
				total += freed
				if !flagQuiet {
					fmt.Fprintf(out, "cleared %s (%s)\n", l.Name, humanBytes(freed))
				}
			}
			if !flagQuiet && len(layers) > 1 {
				fmt.Fprintf(out, "freed %s\n", humanBytes(total))
			}
			return nil
		},
	}
}

// resolveLayerArgs maps CLI layer names to registry layers. No args →
// every layer. Unknown names fail before anything is cleared;
// duplicates collapse, preserving first-mention order.
func resolveLayerArgs(args []string) ([]cache.Layer, error) {
	if len(args) == 0 {
		return cache.Layers(), nil
	}
	seen := make(map[string]bool, len(args))
	layers := make([]cache.Layer, 0, len(args))
	for _, name := range args {
		l, ok := cache.LayerByName(name)
		if !ok {
			valid := make([]string, 0, len(cache.Layers()))
			for _, v := range cache.Layers() {
				valid = append(valid, v.Name)
			}
			return nil, fmt.Errorf("unknown cache layer %q (valid: %s)", name, strings.Join(valid, ", "))
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		layers = append(layers, l)
	}
	return layers, nil
}

// humanBytes renders n with decimal units (1000-based) and one decimal
// place: 999 B, 1.0 kB, 142.3 MB, 3.1 GB. GB is the cap — larger
// values render as (possibly >1000) GB.
func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 2; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMG"[exp])
}
```

In `internal/cli/root.go`, register the group (after `root.AddCommand(newUpdateCmd())` at line 41):

```go
	root.AddCommand(newInstallCmd())
	root.AddCommand(newUpdateCmd())
	root.AddCommand(newCacheCmd())
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cli -run 'TestCache|TestHumanBytes' -v`
Expected: PASS (all six tests)

- [ ] **Step 5: Run the full suite**

Run: `go build ./... && go test ./... -count=1`
Expected: all packages ok

- [ ] **Step 6: Manual smoke test**

```bash
go build -o /tmp/gomposer ./cmd/gomposer
/tmp/gomposer cache          # real cache: path + four layer lines + total
/tmp/gomposer cache dir      # exactly one line: the path
XDG_CACHE_HOME=$(mktemp -d) /tmp/gomposer cache clear            # four "cleared ... (0 B)" lines + freed 0 B
XDG_CACHE_HOME=$(mktemp -d) /tmp/gomposer cache clear bogus      # error naming valid layers, non-zero exit
/tmp/gomposer cache --help   # shows dir + clear subcommands
```

Do NOT run a bare `cache clear` against the real cache during the smoke test — use the `XDG_CACHE_HOME` temp dirs shown above.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/cache.go internal/cli/cache_test.go internal/cli/root.go
git commit -m "feat(cli): gomposer cache command group (info, dir, clear)"
```

---

## Notes for the reviewer / implementer

- The `flagQuiet` reset in the test helper matters: cobra flag vars are package globals in this codebase, and tests in `internal/cli` share them.
- `cache dir` printing under `--quiet` is intentional and spec-mandated (scriptability); do not "fix" it to respect the flag.
- The registry vars (not just `Layers()`) exist so the orchestrator call sites can reference `cache.LayerVCS.Subdir` etc. without error plumbing changes — `defaultDeps` already holds a fetched `cacheRoot`.
