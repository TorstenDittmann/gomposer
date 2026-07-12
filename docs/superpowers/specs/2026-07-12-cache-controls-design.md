# Cache controls

## Motivation

The gomposer cache has four layers under one root, but no user-facing way to inspect or clear them — users must know the platform-specific path (`~/Library/Caches/gomposer` on macOS, `~/.cache/gomposer` elsewhere, `$XDG_CACHE_HOME/gomposer` when set) and `rm -rf` by hand. Composer users expect a built-in (`composer clear-cache`). This adds a `gomposer cache` command group: usage overview, path printing, and selective clearing.

A secondary cleanup rides along: the four cache subdirectory names are currently magic strings scattered across the orchestrator (`"store"`, `"packagist"`, `"vcs"` in `internal/orchestrator/pipeline.go:897-929`; `"resolution"` in `internal/orchestrator/cachekey.go:47`). This feature centralizes them in a layer registry inside `internal/cache`, the package that already owns `Root()`.

## Scope

- New `internal/cache/layers.go`: `Layer` type + fixed registry of the four layers, with per-layer `Path()`, `Size()`, `Clear()`.
- New `internal/cli/cache.go`: `gomposer cache` command group (`cache`, `cache dir`, `cache clear [layers...]`), registered in `root.go`.
- Orchestrator's four magic-string join sites switch to the registry accessors. Behavior identical.

## Non-goals

- **No confirmation prompt on clear.** It is a cache: always safe to delete, always rebuildable. Composer does not prompt either.
- **No concurrent-clear guard.** Clearing while an install runs in another terminal is not a supported operation (exactly like today's manual `rm -rf`). The content-addressed store and MkdirAll-on-demand consumers make it non-corrupting, but in-flight operations may error. The command documentation does not advertise concurrent use.
- **No per-project cache.** All layers are global; `vendor/` and `gomposer.lock` are project state, not cache, and are untouched.
- **No top-level `clear-cache` Composer alias.** Can be added later as a one-line hidden alias if muscle memory demands it.
- **No cache size limits / GC policy.** Inspect and clear only.

## Design

### Layer registry — `internal/cache/layers.go`

```go
// Layer identifies one cache subdirectory. Name is the user-facing
// identifier used in CLI arguments and output; Subdir is the on-disk
// directory name under Root(). They differ for "metadata", whose
// directory is "packagist" for historical layout reasons but whose
// user-facing concept is registry metadata.
type Layer struct {
    Name   string
    Subdir string
}

// Layers returns the fixed registry in display order.
func Layers() []Layer {
    return []Layer{
        {Name: "store", Subdir: "store"},           // content-addressed package archives
        {Name: "metadata", Subdir: "packagist"},    // registry HTTP + parsed metadata
        {Name: "resolution", Subdir: "resolution"}, // resolver result cache
        {Name: "vcs", Subdir: "vcs"},               // VCS clone cache
    }
}

// LayerByName looks a layer up by its user-facing name.
func LayerByName(name string) (Layer, bool)

// Path returns the absolute directory for the layer (Root()/Subdir).
// It does not create the directory.
func (l Layer) Path() (string, error)

// Size returns the total bytes of all files under the layer's
// directory. A missing directory is 0 bytes, not an error.
func (l Layer) Size() (int64, error)

// Clear removes the layer's directory tree and returns the bytes
// freed (its Size immediately before removal). Clearing a missing
// directory is a no-op returning 0. Consumers recreate their
// directories on demand (store.New, packagist caches,
// resolutionCacheDir, and the VCS cache all MkdirAll on first use),
// so Clear does not recreate anything.
func (l Layer) Clear() (int64, error)
```

`Size` walks with `filepath.WalkDir`, summing `DirEntry.Info().Size()` for regular files. Walk errors on individual entries abort with the error (cache dirs are owned by us; partial sums would silently lie).

### CLI — `internal/cli/cache.go`

Cobra group registered in `NewRootCmd` alongside install/update:

- **`gomposer cache`** (group's own `RunE`): prints the root path, one line per layer with human-readable size, and a total:

  ```
  /Users/you/Library/Caches/gomposer
    store       142.3 MB
    metadata     12.1 MB
    resolution   40.2 kB
    vcs           4.0 MB
    total       158.4 MB
  ```

- **`gomposer cache dir`**: prints exactly the root path and a newline — nothing else, so `du -sh $(gomposer cache dir)` and friends compose.

- **`gomposer cache clear [layer...]`**: no args clears every layer; with args clears only the named layers. Prints one line per layer and a total:

  ```
  cleared store (142.3 MB)
  cleared metadata (12.1 MB)
  freed 154.4 MB
  ```

  An unknown layer name errors before anything is cleared: `unknown cache layer "foo" (valid: store, metadata, resolution, vcs)`, exit non-zero. Duplicate layer args are deduplicated. With a single named layer the total line is omitted (it would repeat the per-layer line). Layers that free 0 bytes still print their line — output stays predictable regardless of cache state.

- `--quiet` (existing persistent flag) suppresses all non-error output for every subcommand. `cache dir` under `--quiet` still prints the path — the path IS the output, not decoration.

Human-readable sizes use decimal units (B, kB, MB, GB; 1000-based) with one decimal place, implemented as a small local helper in `internal/cli` (no new dependency).

### Orchestrator call-site swap

The four join sites switch to registry accessors; no behavior change:

- `internal/orchestrator/pipeline.go:897` — `vcs.Config.CacheRoot` ← layer `vcs`
- `internal/orchestrator/pipeline.go:908` — `packagist.Config.CacheDir` ← layer `metadata`
- `internal/orchestrator/pipeline.go:929` — `store.New(...)` ← layer `store`
- `internal/orchestrator/cachekey.go:47` — `resolutionCacheDir()` ← layer `resolution`

Since these sites currently compute `filepath.Join(cacheRoot, "<name>")` from an already-fetched `cacheRoot`, the swap uses `Layer.Path()` (which re-derives Root) or keeps the local join against `Layer.Subdir` — implementer's choice, provided the string literal comes from the registry, not a repeated literal.

## Error handling

- `Root()` failure (no `$HOME`, no `$XDG_CACHE_HOME`) propagates unchanged; CLI prints it and exits non-zero.
- `Size`/`Clear` I/O errors surface with the layer name wrapped in.
- Unknown layer names fail fast, before any clearing happens.

## Tests

- **Unit (`internal/cache/layers_test.go`)**, all under `t.Setenv("XDG_CACHE_HOME", t.TempDir())`:
  - `Layers()` returns exactly the four expected name/subdir pairs in order.
  - `LayerByName` round-trips every registered name; unknown name returns false.
  - `Size` sums nested files correctly; missing dir → 0, no error.
  - `Clear` removes the tree, returns pre-removal size; second Clear → 0, no error.
- **CLI (`internal/cli/cache_test.go`)**, driving the cobra command with captured stdout and isolated XDG root:
  - `cache` prints path, four layer lines, and total.
  - `cache dir` prints exactly the path + newline.
  - `cache clear` (no args) empties every layer dir and reports freed bytes.
  - `cache clear metadata` clears only `packagist/`, leaves `store/` intact.
  - `cache clear bogus` errors, names the valid layers, clears nothing.
- **Orchestrator**: existing suite passing proves the call-site swap (paths are identical strings).

## Related follow-ups (not this pass)

- `clear-cache` top-level alias for Composer muscle memory.
- Cache GC policy (max store size, LRU eviction) if the store grows unbounded in practice.
