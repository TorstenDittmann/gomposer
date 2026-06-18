# Stage 1 / Plan 5: PSR-4 Autoloader Generation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Emit a Composer-compatible autoloader bundle into `vendor/` so that PHP code that does `require 'vendor/autoload.php';` can resolve classes from installed Stage-1 packages. Stage 1 covers PSR-4 only; `files`, `classmap`, and `--optimize-autoloader`-only paths are explicitly deferred to Stage 2 (but the file slots they will use are emitted as empty arrays so the canonical bootstrap shape is preserved).

**Architecture:** Generation is a pure function from `(project dir, vendor entries, root manifest autoload)` to a deterministic set of file writes. The Composer ClassLoader is the same vendored PHP file Composer itself ships; we embed it via `//go:embed` and copy it byte-for-byte. The init class name is per-project (`ComposerAutoloaderInit<HASH>`) so two checkouts of the same project on the same machine never collide. The generator does no I/O on metadata sources — it consumes already-resolved entries supplied by the orchestrator (Plan 6 wires it up).

The output is byte-stable: the same inputs always produce identical bytes. This is critical because (a) Composer is fussy about bootstrap shape, and (b) downstream Stage-2 work will start asserting reproducibility across runs.

**Tech Stack:** Go stdlib `embed`, `text/template`, `crypto/sha256`, `sort`, `path/filepath`, `os`. No new external deps.

**Depends on:**
- Plan 1 (Foundations) — uses `manifest.Autoload`.
- Plan 2 (Metadata) — uses `registry.Autoload` shape (PSR4 is `map[string]any` because Composer allows either `string` or `[]string` values).
- Plan 4 (Store / install) — assumes `vendor/<vendor>/<name>/` directories already exist and that the orchestrator passes us the `InstallPath` (relative to project) for each entry.

This plan is independent of Plan 3 (resolver) — the generator does not care how the entries were chosen.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/autoload/generator.go` | Top-level `Generate(opts Options) error` |
| `internal/autoload/generator_test.go` | Snapshot tests + end-to-end PHP test (gated on `php` being installed) |
| `internal/autoload/psr4.go` | Collect, normalize, and sort PSR-4 prefix → []relative-path |
| `internal/autoload/psr4_test.go` | Unit tests for prefix collection |
| `internal/autoload/hash.go` | Per-project init-class hash (sha256 of abs project path, first 32 hex) |
| `internal/autoload/hash_test.go` | Hash determinism + length tests |
| `internal/autoload/templates.go` | All `text/template` definitions for emitted PHP files |
| `internal/autoload/templates_test.go` | Sanity tests on individual templates |
| `internal/autoload/embedded/ClassLoader.php` | Vendored copy of Composer's ClassLoader |
| `internal/autoload/embedded/LICENSE` | Composer's MIT license, vendored alongside |
| `internal/autoload/embedded/embed.go` | `//go:embed` declarations |
| `internal/autoload/testdata/fixture-project/composer.json` | Tiny root manifest used in snapshot + e2e tests |
| `internal/autoload/testdata/fixture-project/vendor/acme/foo/src/Foo.php` | Fixture package source |
| `internal/autoload/testdata/fixture-project/vendor/acme/bar/src/Bar.php` | Second fixture package source |
| `internal/autoload/testdata/expected/autoload.php` | Snapshot of vendor/autoload.php |
| `internal/autoload/testdata/expected/autoload_real.php` | Snapshot of vendor/composer/autoload_real.php |
| `internal/autoload/testdata/expected/autoload_psr4.php` | Snapshot of vendor/composer/autoload_psr4.php |
| `internal/autoload/testdata/expected/autoload_namespaces.php` | Snapshot |
| `internal/autoload/testdata/expected/autoload_classmap.php` | Snapshot |
| `internal/autoload/testdata/expected/autoload_files.php` | Snapshot |
| `internal/autoload/testdata/expected/autoload_static.php` | Snapshot |
| `internal/autoload/testdata/expected/installed.php` | Snapshot |

---

## Task 1: Vendor in Composer's ClassLoader.php and LICENSE; embed them

**Files:**
- Create: `internal/autoload/embedded/ClassLoader.php`
- Create: `internal/autoload/embedded/LICENSE`
- Create: `internal/autoload/embedded/embed.go`

**Manual prerequisite for the implementer.** This task ships verbatim files from Composer itself. Composer's ClassLoader has been stable for years (the file ships unchanged from project to project), but you must obtain it from a real Composer install — do **not** hand-write or paraphrase. Two acceptable sources:

1. Run `composer install` in any small PHP project, then copy `vendor/composer/ClassLoader.php` and `vendor/composer/LICENSE` from that project.
2. Clone `composer/composer` from GitHub and copy `src/Composer/Autoload/ClassLoader.php` (rename to `ClassLoader.php`) and `LICENSE`.

Both files are MIT-licensed; vendoring them is what real Composer does and what we do here. We pin to a specific version (record the version in a comment in `embed.go`) and bump it deliberately.

- [ ] **Step 1: Place the vendored files**

Copy the two source files into `internal/autoload/embedded/`. Verify the ClassLoader file:

- begins with `<?php` and a copyright comment block,
- defines `namespace Composer\Autoload;`,
- declares `class ClassLoader`,
- ends with a single trailing newline.

Verify the LICENSE file is the standard MIT text with `Copyright (c) Nils Adermann, Jordi Boggiano` near the top.

- [ ] **Step 2: Write the embed glue**

Create `internal/autoload/embedded/embed.go`:

```go
// Package embedded vendors Composer's ClassLoader.php and its LICENSE so
// gomposer can drop them into vendor/composer/ at install time. The
// vendored files are MIT-licensed; see LICENSE for the full notice.
//
// Sourced from composer/composer @ v2.7.x (record the exact upstream commit
// sha or release tag here when bumping). Do NOT modify ClassLoader.php
// locally; if a fix is required, file it upstream and re-vendor.
package embedded

import _ "embed"

//go:embed ClassLoader.php
var ClassLoaderPHP []byte

//go:embed LICENSE
var LicenseText []byte
```

- [ ] **Step 3: Verify it builds**

Run: `go build ./internal/autoload/embedded/...`

Expected: clean build. If `embed` complains about empty files, the copy step in Step 1 was incomplete.

- [ ] **Step 4: Smoke-test the embed**

Append a temporary test (`internal/autoload/embedded/embed_test.go`) that asserts `len(ClassLoaderPHP) > 1000` and `bytes.HasPrefix(ClassLoaderPHP, []byte("<?php"))`, run `go test ./internal/autoload/embedded/...`, then delete the test file once green. (We are checking only that the embed wired up; the snapshot tests in later tasks cover the actual content.)

- [ ] **Step 5: Commit**

```bash
git add internal/autoload/embedded
git commit -m "vendor(autoload): embed Composer ClassLoader.php and LICENSE"
```

---

## Task 2: Per-project init-class hash

**Files:**
- Create: `internal/autoload/hash.go`
- Create: `internal/autoload/hash_test.go`

The init class name is `ComposerAutoloaderInit<HASH>` where `<HASH>` is sha256 of the absolute project path, first 32 hex chars. This matches Composer's behaviour closely enough that two unrelated projects on the same machine never collide on the same PHP process.

- [ ] **Step 1: Write the failing test**

Create `internal/autoload/hash_test.go`:

```go
package autoload

import (
	"strings"
	"testing"
)

func TestInitHashIs32HexChars(t *testing.T) {
	got := InitHash("/home/u/projects/blog")
	if len(got) != 32 {
		t.Fatalf("len = %d, want 32", len(got))
	}
	for _, r := range got {
		ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !ok {
			t.Fatalf("non-hex rune %q in %q", r, got)
		}
	}
}

func TestInitHashIsDeterministic(t *testing.T) {
	a := InitHash("/abs/path")
	b := InitHash("/abs/path")
	if a != b {
		t.Errorf("hash not deterministic: %s vs %s", a, b)
	}
}

func TestInitHashDiffersByPath(t *testing.T) {
	a := InitHash("/abs/path/one")
	b := InitHash("/abs/path/two")
	if a == b {
		t.Errorf("hashes should differ: %s == %s", a, b)
	}
}

func TestInitClassName(t *testing.T) {
	name := InitClassName("/abs/path")
	if !strings.HasPrefix(name, "ComposerAutoloaderInit") {
		t.Errorf("name = %q, want ComposerAutoloaderInit prefix", name)
	}
	if len(name) != len("ComposerAutoloaderInit")+32 {
		t.Errorf("name length = %d", len(name))
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/autoload/...`

Expected: build error on `InitHash`, `InitClassName`.

- [ ] **Step 3: Implement**

Create `internal/autoload/hash.go`:

```go
package autoload

import (
	"crypto/sha256"
	"encoding/hex"
)

// InitHash returns the 32-hex-char identifier used to make the
// ComposerAutoloaderInit<HASH> class name unique per project on a given
// machine. The input must be the project's absolute path; the orchestrator
// is responsible for resolving it (e.g. via filepath.Abs) before calling.
//
// Composer itself uses md5 of a similar input; we use sha256 truncated to
// 32 hex chars (128 bits). Truncation is fine — collision resistance is
// not the threat model; uniqueness across a few projects on one machine is.
func InitHash(absProjectDir string) string {
	sum := sha256.Sum256([]byte(absProjectDir))
	return hex.EncodeToString(sum[:])[:32]
}

// InitClassName returns the full PHP class name for the autoloader init
// class for the given project.
func InitClassName(absProjectDir string) string {
	return "ComposerAutoloaderInit" + InitHash(absProjectDir)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/autoload/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/autoload/hash.go internal/autoload/hash_test.go
git commit -m "feat(autoload): per-project init-class hash"
```

---

## Task 3: PSR-4 prefix collection and normalization

**Files:**
- Create: `internal/autoload/psr4.go`
- Create: `internal/autoload/psr4_test.go`

We must merge PSR-4 prefixes from the root manifest's `autoload` section AND every vendor entry's autoload section. A single prefix can map to multiple directories (Composer allows `"App\\": ["src/", "lib/"]`); we deduplicate while preserving stable order. Trailing slashes on the directory side are normalized to be present (Composer's runtime accepts both, but the file format always writes them).

The PSR4 source type is `map[string]any` because the JSON value can be either `string` or `[]string`; we normalize to `[]string`.

- [ ] **Step 1: Write the failing test**

Create `internal/autoload/psr4_test.go`:

```go
package autoload

import (
	"reflect"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
)

func TestCollectPSR4FromRoot(t *testing.T) {
	root := manifest.Autoload{
		PSR4: map[string]string{
			"App\\":          "src/",
			"App\\Tests\\":   "tests",
		},
	}
	out := CollectPSR4(".", root, nil)
	want := map[string][]string{
		"App\\":        {"src/"},
		"App\\Tests\\": {"tests/"},
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("got %v, want %v", out, want)
	}
}

func TestCollectPSR4FromVendorEntry(t *testing.T) {
	entries := []Entry{
		{
			Name:        "acme/foo",
			Version:     "1.0.0",
			InstallPath: "vendor/acme/foo",
			Autoload: registry.Autoload{
				PSR4: map[string]any{
					"Acme\\Foo\\": "src/",
				},
			},
		},
	}
	out := CollectPSR4(".", manifest.Autoload{}, entries)
	if got := out["Acme\\Foo\\"]; len(got) != 1 || got[0] != "vendor/acme/foo/src/" {
		t.Errorf("got %v", out)
	}
}

func TestCollectPSR4MergesMultipleDirs(t *testing.T) {
	entries := []Entry{
		{
			Name:        "acme/foo",
			InstallPath: "vendor/acme/foo",
			Autoload: registry.Autoload{
				PSR4: map[string]any{
					"Shared\\": []any{"src-a/", "src-b/"},
				},
			},
		},
		{
			Name:        "acme/bar",
			InstallPath: "vendor/acme/bar",
			Autoload: registry.Autoload{
				PSR4: map[string]any{
					"Shared\\": "src/",
				},
			},
		},
	}
	out := CollectPSR4(".", manifest.Autoload{}, entries)
	got := out["Shared\\"]
	want := []string{"vendor/acme/foo/src-a/", "vendor/acme/foo/src-b/", "vendor/acme/bar/src/"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCollectPSR4NormalizesTrailingSlash(t *testing.T) {
	entries := []Entry{
		{
			Name:        "acme/foo",
			InstallPath: "vendor/acme/foo",
			Autoload: registry.Autoload{
				PSR4: map[string]any{
					"Acme\\":  "src",   // no trailing slash
					"Other\\": "src/",  // already trailing
				},
			},
		},
	}
	out := CollectPSR4(".", manifest.Autoload{}, entries)
	if got := out["Acme\\"]; got[0] != "vendor/acme/foo/src/" {
		t.Errorf("Acme = %v, want trailing slash normalized", got)
	}
	if got := out["Other\\"]; got[0] != "vendor/acme/foo/src/" {
		t.Errorf("Other = %v", got)
	}
}

func TestSortedPrefixes(t *testing.T) {
	in := map[string][]string{
		"Z\\":  {"z/"},
		"A\\":  {"a/"},
		"App\\": {"src/"},
	}
	got := SortedPrefixes(in)
	if got[0] != "A\\" || got[1] != "App\\" || got[2] != "Z\\" {
		t.Errorf("got %v, want sorted lexicographically", got)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/autoload/...`

Expected: build error on `Entry`, `CollectPSR4`, `SortedPrefixes`.

- [ ] **Step 3: Implement**

Create `internal/autoload/psr4.go`:

```go
package autoload

import (
	"path"
	"sort"
	"strings"

	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
)

// Entry is one resolved package as the orchestrator hands it to the
// autoloader generator. InstallPath is relative to the project root and
// uses forward slashes regardless of host OS (we never embed host-OS path
// separators in generated PHP).
type Entry struct {
	Name        string
	Version     string
	InstallPath string // e.g. "vendor/acme/foo"
	Autoload    registry.Autoload
}

// CollectPSR4 merges PSR-4 prefixes from the root manifest's autoload
// section and every vendor entry. Returned values:
//   - keys are PSR-4 namespace prefixes (e.g. "Acme\\Foo\\")
//   - values are project-relative directory paths with a trailing slash
//
// Order within each value slice preserves first-seen order: root entries
// before vendor entries, vendor entries in the supplied order. Composer
// does the same so that root paths win for class lookup ties.
//
// projectDir is currently unused (paths are project-relative) but is kept
// in the signature for symmetry with future absolute-path generators.
func CollectPSR4(projectDir string, root manifest.Autoload, entries []Entry) map[string][]string {
	out := map[string][]string{}

	// Root manifest first.
	for prefix, dir := range root.PSR4 {
		out[prefix] = appendUnique(out[prefix], normalizeDir(dir))
	}

	// Vendor entries in supplied order.
	for _, e := range entries {
		for prefix, raw := range e.Autoload.PSR4 {
			for _, dir := range toStringSlice(raw) {
				rel := joinSlash(e.InstallPath, dir)
				out[prefix] = appendUnique(out[prefix], normalizeDir(rel))
			}
		}
	}
	return out
}

// SortedPrefixes returns the keys of m sorted lexicographically.
// Used everywhere we serialize PSR-4 maps so output is byte-stable.
func SortedPrefixes(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// toStringSlice normalizes the polymorphic JSON value (string OR []string)
// that Composer accepts in psr-4 entries.
func toStringSlice(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	}
	return nil
}

// normalizeDir ensures the directory ends with exactly one "/".
// Empty string is preserved as empty (means "package install root").
func normalizeDir(d string) string {
	if d == "" {
		return ""
	}
	d = strings.TrimRight(d, "/")
	return d + "/"
}

// joinSlash joins forward-slash paths without ever introducing a host
// path separator.
func joinSlash(a, b string) string {
	if b == "" {
		return strings.TrimRight(a, "/") + "/"
	}
	return path.Join(a, b)
}

func appendUnique(s []string, x string) []string {
	for _, e := range s {
		if e == x {
			return s
		}
	}
	return append(s, x)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/autoload/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/autoload/psr4.go internal/autoload/psr4_test.go
git commit -m "feat(autoload): collect and normalize PSR-4 prefixes"
```

---

## Task 4: PHP file templates

**Files:**
- Create: `internal/autoload/templates.go`
- Create: `internal/autoload/templates_test.go`

Every PHP file we emit has its full source defined here as a Go template literal. The renderer is byte-deterministic: sort keys, fixed indentation, single trailing newline. The exact bytes here are the contract — Stage-2 work will assert on them.

- [ ] **Step 1: Write the file**

Create `internal/autoload/templates.go`:

```go
package autoload

import (
	"bytes"
	"fmt"
	"text/template"
)

// renderData carries every value referenced by any template.
type renderData struct {
	InitClass string              // e.g. "ComposerAutoloaderInitabc..."
	Hash      string              // 32-hex chars
	PSR4      map[string][]string // prefix -> []relativeDir (trailing slash)
	// SortedPSR4 is supplied alongside PSR4 so templates iterate in
	// deterministic order without resorting on each render.
	SortedPSR4 []string
}

// autoloadPHPTpl is the canonical entrypoint that user code includes via
// `require 'vendor/autoload.php'`. We track Composer's exact bytes here.
const autoloadPHPTpl = `<?php

// autoload.php @generated by gomposer

require_once __DIR__ . '/composer/autoload_real.php';

return {{.InitClass}}::getLoader();
`

// autoloadRealTpl defines the init class. Composer's real version handles
// optimize-autoloader, apc, suffix, etc.; for Stage 1 we only need the
// non-optimized path. Stage 2 will extend this template (or fork it).
const autoloadRealTpl = `<?php

// autoload_real.php @generated by gomposer

class {{.InitClass}}
{
    private static $loader;

    public static function loadClassLoader($class)
    {
        if ('Composer\Autoload\ClassLoader' === $class) {
            require __DIR__ . '/ClassLoader.php';
        }
    }

    /**
     * @return \Composer\Autoload\ClassLoader
     */
    public static function getLoader()
    {
        if (null !== self::$loader) {
            return self::$loader;
        }

        spl_autoload_register(array('{{.InitClass}}', 'loadClassLoader'), true, true);
        self::$loader = $loader = new \Composer\Autoload\ClassLoader(\dirname(__DIR__));
        spl_autoload_unregister(array('{{.InitClass}}', 'loadClassLoader'));

        $useStaticLoader = PHP_VERSION_ID >= 50600 && !defined('HHVM_VERSION');
        if ($useStaticLoader) {
            require __DIR__ . '/autoload_static.php';
            call_user_func(\Composer\Autoload\ComposerStaticInit{{.Hash}}::getInitializer($loader));
        } else {
            $map = require __DIR__ . '/autoload_namespaces.php';
            foreach ($map as $namespace => $path) {
                $loader->set($namespace, $path);
            }

            $map = require __DIR__ . '/autoload_psr4.php';
            foreach ($map as $namespace => $path) {
                $loader->setPsr4($namespace, $path);
            }

            $classMap = require __DIR__ . '/autoload_classmap.php';
            if ($classMap) {
                $loader->addClassMap($classMap);
            }
        }

        $loader->register(true);

        return $loader;
    }
}
`

// autoloadPSR4Tpl writes the prefix -> [absolute-path, ...] map.
// We use $vendorDir/$baseDir constants to mirror Composer's exact shape.
const autoloadPSR4Tpl = `<?php

// autoload_psr4.php @generated by gomposer

$vendorDir = dirname(__DIR__);
$baseDir = dirname($vendorDir);

return array(
{{- range $prefix := .SortedPSR4}}
    {{phpString $prefix}} => array({{range $i, $dir := index $.PSR4 $prefix}}{{if $i}}, {{end}}{{phpDir $dir}}{{end}}),
{{- end}}
);
`

// autoloadNamespacesTpl is always empty in Stage 1 (PSR-0 deferred).
const autoloadNamespacesTpl = `<?php

// autoload_namespaces.php @generated by gomposer

$vendorDir = dirname(__DIR__);
$baseDir = dirname($vendorDir);

return array(
);
`

// autoloadClassmapTpl is always empty in Stage 1.
const autoloadClassmapTpl = `<?php

// autoload_classmap.php @generated by gomposer

$vendorDir = dirname(__DIR__);
$baseDir = dirname($vendorDir);

return array(
);
`

// autoloadFilesTpl is always empty in Stage 1.
const autoloadFilesTpl = `<?php

// autoload_files.php @generated by gomposer

$vendorDir = dirname(__DIR__);
$baseDir = dirname($vendorDir);

return array(
);
`

// autoloadStaticTpl mirrors Composer's --optimize-autoloader output for
// the PSR-4 portion only. It is required because autoload_real.php prefers
// it on PHP >= 5.6. In Stage 2 we expand this with classmap and files.
const autoloadStaticTpl = `<?php

// autoload_static.php @generated by gomposer

namespace Composer\Autoload;

class ComposerStaticInit{{.Hash}}
{
    public static $prefixLengthsPsr4 = array(
{{- range $prefix := .SortedPSR4}}
        {{phpString (firstChar $prefix)}} => array({{phpString $prefix}} => {{len $prefix}}),
{{- end}}
    );

    public static $prefixDirsPsr4 = array(
{{- range $prefix := .SortedPSR4}}
        {{phpString $prefix}} => array({{range $i, $dir := index $.PSR4 $prefix}}{{if $i}}, {{end}}{{phpDir $dir}}{{end}}),
{{- end}}
    );

    public static function getInitializer(ClassLoader $loader)
    {
        return \Closure::bind(function () use ($loader) {
            $loader->prefixLengthsPsr4 = ComposerStaticInit{{.Hash}}::$prefixLengthsPsr4;
            $loader->prefixDirsPsr4 = ComposerStaticInit{{.Hash}}::$prefixDirsPsr4;
        }, null, ClassLoader::class);
    }
}
`

// installedTpl is a minimal stub for Stage 1. Symfony/Laravel begin to read
// this in Stage 2; until then "no installed packages info available" is
// acceptable. The format mirrors Composer/InstalledVersions array shape.
const installedTpl = `<?php return array(
    'root' => array(
        'name' => 'gomposer/stage1-stub',
        'pretty_version' => '1.0.0+no-version-set',
        'version' => '1.0.0.0',
        'reference' => null,
        'type' => 'library',
        'install_path' => __DIR__ . '/../../',
        'aliases' => array(),
        'dev' => false,
    ),
    'versions' => array(),
);
`

// installedVersionsTpl is the InstalledVersions class. Stage 1 ships a
// stub that returns empty data; Stage 2 will replace this with a faithful
// port. Keeping the class present (even with stub data) means user code
// that does `Composer\InstalledVersions::getRootPackage()` does not fatal.
const installedVersionsTpl = `<?php

// InstalledVersions.php @generated by gomposer (Stage 1 stub)
//
// This is a minimal stub. Stage 2 will replace it with the full
// InstalledVersions implementation. User code that calls into this class
// will receive empty/null answers rather than a fatal error.

namespace Composer;

class InstalledVersions
{
    private static $installed;

    public static function getInstallPath($packageName)
    {
        return null;
    }

    public static function getInstalledPackages()
    {
        return array();
    }

    public static function isInstalled($packageName, $includeDevRequirements = true)
    {
        return false;
    }

    public static function getVersion($packageName)
    {
        return null;
    }

    public static function getPrettyVersion($packageName)
    {
        return null;
    }

    public static function getRootPackage()
    {
        $installed = self::getInstalled();
        return $installed['root'];
    }

    public static function getAllRawData()
    {
        return array(self::getInstalled());
    }

    private static function getInstalled()
    {
        if (null === self::$installed) {
            self::$installed = require __DIR__ . '/installed.php';
        }
        return self::$installed;
    }
}
`

// renderAll renders every template to a map keyed by relative output path.
func renderAll(d renderData) (map[string][]byte, error) {
	files := []struct {
		path, tpl string
	}{
		{"vendor/autoload.php", autoloadPHPTpl},
		{"vendor/composer/autoload_real.php", autoloadRealTpl},
		{"vendor/composer/autoload_psr4.php", autoloadPSR4Tpl},
		{"vendor/composer/autoload_namespaces.php", autoloadNamespacesTpl},
		{"vendor/composer/autoload_classmap.php", autoloadClassmapTpl},
		{"vendor/composer/autoload_files.php", autoloadFilesTpl},
		{"vendor/composer/autoload_static.php", autoloadStaticTpl},
		{"vendor/composer/installed.php", installedTpl},
		{"vendor/composer/InstalledVersions.php", installedVersionsTpl},
	}
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		b, err := renderOne(f.tpl, d)
		if err != nil {
			return nil, fmt.Errorf("autoload: render %s: %w", f.path, err)
		}
		out[f.path] = b
	}
	return out, nil
}

func renderOne(tpl string, d renderData) ([]byte, error) {
	t := template.New("").Funcs(template.FuncMap{
		"phpString": phpString,
		"phpDir":    phpDir,
		"firstChar": firstChar,
	})
	t, err := t.Parse(tpl)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// phpString emits a single-quoted PHP string with backslashes and single
// quotes escaped. PHP single-quoted strings only need those two escapes,
// which matches what Composer emits. Newlines in keys would be invalid
// PSR-4 namespaces so we don't try to escape them.
func phpString(s string) string {
	r := make([]byte, 0, len(s)+2)
	r = append(r, '\'')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			r = append(r, '\\', '\\')
		case '\'':
			r = append(r, '\\', '\'')
		default:
			r = append(r, c)
		}
	}
	r = append(r, '\'')
	return string(r)
}

// phpDir emits a directory expression like "$vendorDir . '/acme/foo/src'"
// when the path lives under vendor/, or "$baseDir . '/src'" otherwise.
// Trailing slashes are stripped because Composer never emits them inside
// the array (the runtime adds them).
func phpDir(rel string) string {
	rel = trimTrailingSlash(rel)
	const vendorPrefix = "vendor/"
	if len(rel) >= len(vendorPrefix) && rel[:len(vendorPrefix)] == vendorPrefix {
		return "$vendorDir . " + phpString("/"+rel[len(vendorPrefix):])
	}
	if rel == "" {
		return "$baseDir . " + phpString("/")
	}
	return "$baseDir . " + phpString("/"+rel)
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// firstChar returns the first character of s as a length-1 string.
// Used by autoload_static.php's prefixLengthsPsr4 buckets.
func firstChar(s string) string {
	if s == "" {
		return ""
	}
	return s[:1]
}
```

- [ ] **Step 2: Write template sanity tests**

Create `internal/autoload/templates_test.go`:

```go
package autoload

import (
	"strings"
	"testing"
)

func TestPhpStringEscapesBackslashAndQuote(t *testing.T) {
	got := phpString(`App\Foo`)
	if got != `'App\\Foo'` {
		t.Errorf("got %s", got)
	}
	got = phpString("can't")
	if got != `'can\'t'` {
		t.Errorf("got %s", got)
	}
}

func TestPhpDirVendorPath(t *testing.T) {
	got := phpDir("vendor/acme/foo/src/")
	want := `$vendorDir . '/acme/foo/src'`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestPhpDirProjectPath(t *testing.T) {
	got := phpDir("src/")
	if got != `$baseDir . '/src'` {
		t.Errorf("got %s", got)
	}
}

func TestRenderAllProducesAllSlots(t *testing.T) {
	d := renderData{
		InitClass:  "ComposerAutoloaderInit" + strings.Repeat("a", 32),
		Hash:       strings.Repeat("a", 32),
		PSR4:       map[string][]string{"App\\": {"src/"}},
		SortedPSR4: []string{"App\\"},
	}
	out, err := renderAll(d)
	if err != nil {
		t.Fatalf("renderAll: %v", err)
	}
	for _, p := range []string{
		"vendor/autoload.php",
		"vendor/composer/autoload_real.php",
		"vendor/composer/autoload_psr4.php",
		"vendor/composer/autoload_namespaces.php",
		"vendor/composer/autoload_classmap.php",
		"vendor/composer/autoload_files.php",
		"vendor/composer/autoload_static.php",
		"vendor/composer/installed.php",
		"vendor/composer/InstalledVersions.php",
	} {
		if _, ok := out[p]; !ok {
			t.Errorf("missing output: %s", p)
		}
	}
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/autoload/...`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/autoload/templates.go internal/autoload/templates_test.go
git commit -m "feat(autoload): PHP file templates with deterministic emission"
```

---

## Task 5: Top-level `Generate(opts)` function

**Files:**
- Create: `internal/autoload/generator.go`

This is the public entry point. The orchestrator calls it with already-resolved entries; it produces the byte map, renders, and writes the files. ClassLoader.php and LICENSE come from the embed package.

- [ ] **Step 1: Write the implementation**

Create `internal/autoload/generator.go`:

```go
// Package autoload generates Composer-compatible PSR-4 autoloader files
// inside a project's vendor/ directory. Stage 1 writes PSR-4 only; the
// `files` and `classmap` slots are emitted as empty arrays so the bootstrap
// shape matches Composer's. Stage 2 will populate them.
package autoload

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/torstendittmann/gomposer/internal/autoload/embedded"
	"github.com/torstendittmann/gomposer/internal/manifest"
)

// Options is the input to Generate. ProjectDir must be absolute (the
// orchestrator resolves it before calling). Entries is the list of all
// installed packages, in the order the orchestrator chose. RootAutoload
// is the root manifest's `autoload` (NOT autoload-dev — that is included
// by the orchestrator into the same slice when --no-dev is unset, since
// the runtime cannot tell them apart anyway in Stage 1).
type Options struct {
	ProjectDir   string
	Entries      []Entry
	RootAutoload manifest.Autoload
}

// Generate writes the full autoloader bundle into opts.ProjectDir/vendor/.
// All writes are atomic per file (write-temp + rename). On error, files
// already written are left in place — the orchestrator's caller is
// responsible for cleanup if it wants a strict all-or-nothing install.
//
// Generate is byte-deterministic: same Options -> same files.
func Generate(opts Options) error {
	if !filepath.IsAbs(opts.ProjectDir) {
		return errors.New("autoload: ProjectDir must be absolute")
	}
	psr4 := CollectPSR4(opts.ProjectDir, opts.RootAutoload, opts.Entries)
	data := renderData{
		InitClass:  InitClassName(opts.ProjectDir),
		Hash:       InitHash(opts.ProjectDir),
		PSR4:       psr4,
		SortedPSR4: SortedPrefixes(psr4),
	}

	out, err := renderAll(data)
	if err != nil {
		return err
	}

	// Add the embedded files. Their content is identical for every project
	// so they bypass templating.
	out["vendor/composer/ClassLoader.php"] = embedded.ClassLoaderPHP
	out["vendor/composer/LICENSE"] = embedded.LicenseText

	for rel, body := range out {
		abs := filepath.Join(opts.ProjectDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return fmt.Errorf("autoload: mkdir %s: %w", abs, err)
		}
		if err := writeAtomic(abs, body); err != nil {
			return fmt.Errorf("autoload: write %s: %w", abs, err)
		}
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./internal/autoload/...`

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add internal/autoload/generator.go
git commit -m "feat(autoload): top-level Generate writes the full bundle"
```

---

## Task 6: Fixture project for snapshot + e2e tests

**Files:**
- Create: `internal/autoload/testdata/fixture-project/composer.json`
- Create: `internal/autoload/testdata/fixture-project/vendor/acme/foo/src/Foo.php`
- Create: `internal/autoload/testdata/fixture-project/vendor/acme/bar/src/Bar.php`

The fixture is a tiny project with two pretend-installed PSR-4 packages. We use it for both byte-stable snapshot tests (Task 7) and the PHP smoke test (Task 9).

- [ ] **Step 1: Write the root manifest**

Create `internal/autoload/testdata/fixture-project/composer.json`:

```json
{
    "name": "gomposer/test-fixture",
    "type": "library",
    "autoload": {
        "psr-4": {
            "App\\": "src/"
        }
    }
}
```

- [ ] **Step 2: Write fixture package source files**

Create `internal/autoload/testdata/fixture-project/vendor/acme/foo/src/Foo.php`:

```php
<?php

namespace Acme\Foo;

class Foo
{
    public static function hello(): string
    {
        return 'foo';
    }
}
```

Create `internal/autoload/testdata/fixture-project/vendor/acme/bar/src/Bar.php`:

```php
<?php

namespace Acme\Bar;

class Bar
{
    public static function hello(): string
    {
        return 'bar';
    }
}
```

- [ ] **Step 3: Note (no commit yet)**

Don't commit until snapshots exist (Task 7). Otherwise the fixture is half-wired in repo history.

---

## Task 7: Snapshot tests — byte-for-byte assertion

**Files:**
- Create: `internal/autoload/testdata/expected/*` (eight expected files)
- Create: `internal/autoload/generator_test.go` (snapshot subtests)

Composer is fussy about bootstrap shape. The only safe way to know we're emitting the right thing is to assert byte equality against fixtures. We use a fixed project path (`/gomposer-test/fixture`) so the InitHash is reproducible across machines.

- [ ] **Step 1: Write the snapshot test (will fail; expected files don't exist)**

Create `internal/autoload/generator_test.go`:

```go
package autoload

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
)

// fixedProjectDir gives reproducible InitHash across machines.
const fixedProjectDir = "/gomposer-test/fixture"

func fixtureEntries() []Entry {
	return []Entry{
		{
			Name:        "acme/foo",
			Version:     "1.0.0",
			InstallPath: "vendor/acme/foo",
			Autoload: registry.Autoload{
				PSR4: map[string]any{
					"Acme\\Foo\\": "src/",
				},
			},
		},
		{
			Name:        "acme/bar",
			Version:     "1.0.0",
			InstallPath: "vendor/acme/bar",
			Autoload: registry.Autoload{
				PSR4: map[string]any{
					"Acme\\Bar\\": "src/",
				},
			},
		},
	}
}

func fixtureRoot() manifest.Autoload {
	return manifest.Autoload{
		PSR4: map[string]string{"App\\": "src/"},
	}
}

func TestSnapshot(t *testing.T) {
	tmp := t.TempDir()
	// Symlink-or-bind tmp to fixedProjectDir? Simpler: pass fixedProjectDir
	// to InitHash but write into tmp. Generate uses ProjectDir for both,
	// so we generate into tmp and post-process to reset the hash if needed.
	//
	// Solution: use fixedProjectDir directly. We don't actually write to
	// it; we pass it to a hash-only render path. For the on-disk part we
	// make ProjectDir = tmp. Two generates are awkward, so instead we
	// compare the in-memory render output (renderAll) and assert ClassLoader
	// + LICENSE are passthroughs.

	// Render in-memory using the fixed project dir for hash determinism.
	psr4 := CollectPSR4(fixedProjectDir, fixtureRoot(), fixtureEntries())
	out, err := renderAll(renderData{
		InitClass:  InitClassName(fixedProjectDir),
		Hash:       InitHash(fixedProjectDir),
		PSR4:       psr4,
		SortedPSR4: SortedPrefixes(psr4),
	})
	if err != nil {
		t.Fatalf("renderAll: %v", err)
	}

	cases := []struct {
		gen, expected string
	}{
		{"vendor/autoload.php", "autoload.php"},
		{"vendor/composer/autoload_real.php", "autoload_real.php"},
		{"vendor/composer/autoload_psr4.php", "autoload_psr4.php"},
		{"vendor/composer/autoload_namespaces.php", "autoload_namespaces.php"},
		{"vendor/composer/autoload_classmap.php", "autoload_classmap.php"},
		{"vendor/composer/autoload_files.php", "autoload_files.php"},
		{"vendor/composer/autoload_static.php", "autoload_static.php"},
		{"vendor/composer/installed.php", "installed.php"},
	}
	for _, tc := range cases {
		t.Run(tc.expected, func(t *testing.T) {
			got, ok := out[tc.gen]
			if !ok {
				t.Fatalf("missing generated output: %s", tc.gen)
			}
			expectedPath := filepath.Join("testdata", "expected", tc.expected)
			want, err := os.ReadFile(expectedPath)
			if err != nil {
				t.Fatalf("read expected %s: %v", expectedPath, err)
			}
			if !bytes.Equal(got, want) {
				// On mismatch, write the actual to disk for easy diffing.
				_ = os.WriteFile(expectedPath+".actual", got, 0o644)
				t.Errorf("byte mismatch for %s — see %s.actual for the actual bytes", tc.expected, expectedPath)
			}
		})
	}
	_ = tmp
}
```

- [ ] **Step 2: Run to capture actual output (test will fail and write `.actual` files)**

Run: `go test ./internal/autoload/... -run TestSnapshot`

Expected: every subtest fails with "read expected ...: no such file or directory". Because the test fails before writing `.actual`, we'll instead seed the expected files manually in Step 3.

- [ ] **Step 3: Seed expected files via a one-shot helper**

Add a temporary test that writes expected outputs to disk (delete after Step 4):

```go
func TestWriteExpected(t *testing.T) {
	if os.Getenv("WRITE_EXPECTED") != "1" {
		t.Skip("set WRITE_EXPECTED=1 to regenerate")
	}
	psr4 := CollectPSR4(fixedProjectDir, fixtureRoot(), fixtureEntries())
	out, err := renderAll(renderData{
		InitClass:  InitClassName(fixedProjectDir),
		Hash:       InitHash(fixedProjectDir),
		PSR4:       psr4,
		SortedPSR4: SortedPrefixes(psr4),
	})
	if err != nil {
		t.Fatal(err)
	}
	for gen, body := range out {
		base := filepath.Base(gen)
		dest := filepath.Join("testdata", "expected", base)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
```

Run: `WRITE_EXPECTED=1 go test ./internal/autoload/... -run TestWriteExpected`

Expected: `testdata/expected/` populated with eight files.

**Manual inspection step:** Open `autoload_psr4.php` and visually verify:
- `'Acme\\Bar\\' => array($vendorDir . '/acme/bar/src')`
- `'Acme\\Foo\\' => array($vendorDir . '/acme/foo/src')`
- `'App\\' => array($baseDir . '/src')`
- Sorted lexicographically.

Open `autoload_static.php` and verify the `$prefixLengthsPsr4` buckets are keyed by first char (`'A'` for all three here, all in one `array(...)`).

If anything looks wrong, fix the templates in Task 4 and re-run `WRITE_EXPECTED=1`.

- [ ] **Step 4: Remove the helper, run snapshot test for real**

Delete `TestWriteExpected`. Run: `go test ./internal/autoload/... -run TestSnapshot`

Expected: PASS.

- [ ] **Step 5: Add a "Generate writes files" subtest**

Append to `generator_test.go`:

```go
func TestGenerateWritesFiles(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(Options{
		ProjectDir:   dir,
		Entries:      fixtureEntries(),
		RootAutoload: fixtureRoot(),
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	for _, want := range []string{
		"vendor/autoload.php",
		"vendor/composer/autoload_real.php",
		"vendor/composer/autoload_psr4.php",
		"vendor/composer/autoload_namespaces.php",
		"vendor/composer/autoload_classmap.php",
		"vendor/composer/autoload_files.php",
		"vendor/composer/autoload_static.php",
		"vendor/composer/autoload_static.php",
		"vendor/composer/installed.php",
		"vendor/composer/InstalledVersions.php",
		"vendor/composer/ClassLoader.php",
		"vendor/composer/LICENSE",
	} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}

	// ClassLoader.php must be byte-identical to the embedded copy.
	got, err := os.ReadFile(filepath.Join(dir, "vendor/composer/ClassLoader.php"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, []byte("<?php")) {
		t.Errorf("ClassLoader.php does not start with <?php")
	}
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/autoload/...`

Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/autoload internal/autoload/testdata
git commit -m "feat(autoload): snapshot tests with byte-for-byte expected fixtures"
```

---

## Task 8: Idempotency and overwrite tests

**Files:**
- Modify: `internal/autoload/generator_test.go`

Re-running `Generate` over an existing `vendor/composer/` must overwrite cleanly and yield identical bytes.

- [ ] **Step 1: Append the test**

```go
func TestGenerateIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		ProjectDir:   dir,
		Entries:      fixtureEntries(),
		RootAutoload: fixtureRoot(),
	}
	if err := Generate(opts); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "vendor/composer/autoload_psr4.php"))
	if err != nil {
		t.Fatal(err)
	}

	if err := Generate(opts); err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "vendor/composer/autoload_psr4.php"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("autoload_psr4.php changed across regenerations")
	}
}

func TestGenerateRejectsRelativeProjectDir(t *testing.T) {
	err := Generate(Options{ProjectDir: "relative/path"})
	if err == nil {
		t.Error("expected error on relative ProjectDir")
	}
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/autoload/...`

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/autoload/generator_test.go
git commit -m "test(autoload): idempotency and absolute-path enforcement"
```

---

## Task 9: End-to-end test against real PHP (gated on `php` being installed)

**Files:**
- Modify: `internal/autoload/generator_test.go`

The ultimate proof: PHP can require our `vendor/autoload.php` and resolve a class. We use the fixture project from Task 6 (its source files declare `Acme\Foo\Foo`, `Acme\Bar\Bar`, and `App\Foo`). If `php` isn't on PATH, skip — gomposer's CI matrix may run on minimal images.

- [ ] **Step 1: Add an `App\Foo` source file in the fixture**

Create `internal/autoload/testdata/fixture-project/src/Foo.php`:

```php
<?php

namespace App;

class Foo
{
    public static function hello(): string
    {
        return 'app-foo';
    }
}
```

This sits at the project root (not under `vendor/`) so the root manifest's `App\\: src/` mapping resolves it.

- [ ] **Step 2: Append the e2e test**

```go
func TestEndToEndPHPClassResolution(t *testing.T) {
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("php not on PATH; skipping e2e")
	}

	// Copy the fixture into a writable tempdir so Generate's writes
	// don't pollute testdata.
	dir := t.TempDir()
	src := filepath.Join("testdata", "fixture-project")
	if err := copyDir(src, dir); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	if err := Generate(Options{
		ProjectDir: dir,
		Entries: []Entry{
			{
				Name:        "acme/foo",
				Version:     "1.0.0",
				InstallPath: "vendor/acme/foo",
				Autoload:    registry.Autoload{PSR4: map[string]any{"Acme\\Foo\\": "src/"}},
			},
			{
				Name:        "acme/bar",
				Version:     "1.0.0",
				InstallPath: "vendor/acme/bar",
				Autoload:    registry.Autoload{PSR4: map[string]any{"Acme\\Bar\\": "src/"}},
			},
		},
		RootAutoload: manifest.Autoload{PSR4: map[string]string{"App\\": "src/"}},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	cases := []struct {
		class string
		want  string // PHP echoes "1" if class_exists is true, "" otherwise
	}{
		{`App\\Foo`, "1"},
		{`Acme\\Foo\\Foo`, "1"},
		{`Acme\\Bar\\Bar`, "1"},
		{`Nope\\Missing`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.class, func(t *testing.T) {
			script := "require 'vendor/autoload.php'; echo class_exists('" + tc.class + "') ? '1' : '';"
			cmd := exec.Command("php", "-r", script)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("php failed: %v\noutput:\n%s", err, out)
			}
			if string(out) != tc.want {
				t.Errorf("class_exists(%s) = %q, want %q", tc.class, string(out), tc.want)
			}
		})
	}
}

// copyDir copies src to dst recursively. Symlinks are not handled; the
// fixture tree contains only regular files.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
```

Add the imports:
- `"io/fs"`
- `"os/exec"`

- [ ] **Step 3: Run with PHP available**

Run: `go test ./internal/autoload/... -run TestEndToEnd -v`

Expected: subtests for `App\Foo`, `Acme\Foo\Foo`, `Acme\Bar\Bar` PASS; `Nope\Missing` PASSes by returning empty.

- [ ] **Step 4: Run on a system without PHP (or simulate)**

If PHP is on PATH locally, temporarily prepend a fake PATH that excludes it, OR rename the binary in `$PATH` for one invocation:

```bash
PATH=/tmp/empty go test ./internal/autoload/... -run TestEndToEnd -v
```

Expected: SKIP with message "php not on PATH; skipping e2e".

- [ ] **Step 5: Commit**

```bash
git add internal/autoload/generator_test.go internal/autoload/testdata/fixture-project/src
git commit -m "test(autoload): end-to-end class resolution with php -r"
```

---

## Task 10: Documentation comment for stub `InstalledVersions`

**Files:**
- Modify: `internal/autoload/templates.go`

Stage 2 will replace the stubbed `InstalledVersions` with the real implementation. We add a top-of-file comment in the generated PHP and a doc note in the Go source so future implementers and grep-with-confidence searchers find it.

- [ ] **Step 1: Verify the existing template note**

The template `installedVersionsTpl` already has a stub-marker comment. Check it's clear: "Stage 2 will replace it with the full InstalledVersions implementation."

- [ ] **Step 2: Add a Go-side doc comment on the template constant**

Replace the line above `installedVersionsTpl` with:

```go
// installedVersionsTpl is the InstalledVersions class. Stage 1 ships a
// stub that returns empty data; Symfony 6+, Laravel 10+, and any package
// that calls Composer\InstalledVersions::*() will get null/empty answers
// rather than fatal errors. Stage 2 (Real-world coverage) replaces this
// with a faithful port. The TODO marker below is grep-friendly:
//
//   TODO(stage2): port full InstalledVersions implementation
```

- [ ] **Step 3: Run tests to confirm no regression**

Run: `go test ./internal/autoload/...`

Expected: PASS (snapshot bytes unchanged because we only edited Go-side comments).

- [ ] **Step 4: Commit**

```bash
git add internal/autoload/templates.go
git commit -m "docs(autoload): mark InstalledVersions stub for Stage 2 follow-up"
```

---

## Task 11: Wire generator option for empty entry list (no packages)

**Files:**
- Modify: `internal/autoload/generator_test.go`

A project with only root autoload and no vendor packages is valid (e.g. a library repo before publishing). Make sure Generate works with `Entries: nil`.

- [ ] **Step 1: Append the test**

```go
func TestGenerateWithNoEntries(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(Options{
		ProjectDir:   dir,
		Entries:      nil,
		RootAutoload: manifest.Autoload{PSR4: map[string]string{"App\\": "src/"}},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "vendor/composer/autoload_psr4.php"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`'App\\'`)) {
		t.Errorf("expected App\\ prefix in autoload_psr4.php, got:\n%s", body)
	}
}

func TestGenerateWithNoAutoloadAtAll(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(Options{
		ProjectDir: dir,
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Empty PSR-4 array still produces a valid PHP file that returns array().
	body, err := os.ReadFile(filepath.Join(dir, "vendor/composer/autoload_psr4.php"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("return array(")) {
		t.Errorf("autoload_psr4.php missing return array(): %s", body)
	}
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/autoload/...`

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/autoload/generator_test.go
git commit -m "test(autoload): empty entries and missing autoload sections"
```

---

## Plan 5 acceptance check

After all tasks:

- `go test ./internal/autoload/...` passes (offline, no network).
- `go test ./internal/autoload/... -run TestEndToEnd -v` PASSes when `php` is on PATH; SKIPs cleanly when it isn't.
- `internal/autoload/testdata/expected/` contains eight PHP fixture files; the snapshot test asserts byte equality against them.
- `Generate(opts)` is byte-deterministic: two runs over the same Options produce identical files (verified by `TestGenerateIsIdempotent`).
- The vendored `embedded/ClassLoader.php` is present, MIT-licensed, and copied verbatim from upstream Composer (the source release/commit is recorded in `embed.go`).
- `vendor/composer/InstalledVersions.php` and `vendor/composer/installed.php` exist as Stage-1 stubs; the Go-side comment marks the Stage-2 follow-up clearly.
- The public surface — `autoload.Generate`, `autoload.Options`, `autoload.Entry` — is stable for Plan 6 (orchestrator wire-up) to consume without modification.

If any of these fails, fix forward in a follow-up commit before declaring Plan 5 done. Specifically:

1. If snapshot tests fail because of trivial whitespace drift, verify the failure with a side-by-side diff (`diff testdata/expected/autoload_psr4.php testdata/expected/autoload_psr4.php.actual`) before deciding whether to update the snapshot or fix the template — the bytes are the contract; do not casually re-snapshot.
2. If the e2e test fails on a CI runner with PHP installed, the most likely cause is the embedded `ClassLoader.php` being subtly wrong (e.g., truncated). Re-vendor from a clean Composer install and re-run.
3. If `Generate` corrupts an existing `vendor/composer/` (very unlikely given write-temp-then-rename), check that no parallel test is racing on the same temp dir — `t.TempDir()` should make this impossible, but a stray hand-rolled path could not.
