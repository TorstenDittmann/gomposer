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

	WarnPSR0(opts.RootAutoload, opts.Entries)

	psr4 := CollectPSR4(opts.ProjectDir, opts.RootAutoload, opts.Entries)
	files := CollectFiles(opts.RootAutoload, opts.Entries)
	classmap, err := CollectClassmap(opts.ProjectDir, opts.RootAutoload, opts.Entries)
	if err != nil {
		return err
	}

	sorted := SortedPrefixes(psr4)
	data := renderData{
		InitClass:       InitClassName(opts.ProjectDir),
		Hash:            InitHash(opts.ProjectDir),
		PSR4:            psr4,
		SortedPSR4:      sorted,
		PSR4ByFirstChar: buildFirstCharGroups(sorted),
		Files:           files,
		Classmap:        classmap,
		SortedClasses:   SortedClassmapKeys(classmap),
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
