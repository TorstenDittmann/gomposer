package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/scripts"
)

// NamedScriptRunner executes a named composer.json script. The default
// implementation is scripts.New(); tests inject a recording fake.
type NamedScriptRunner interface {
	RunNamed(ctx context.Context, name string, opts scripts.Options) error
}

// ScriptCommand is the input to RunScript.
type ScriptCommand struct {
	// ProjectDir is the starting directory (cwd or --project). The nearest
	// composer.json walking up is the selected manifest, so a workspace
	// member runs its own scripts rather than the root's.
	ProjectDir string
	Name       string
	ExtraArgs  []string
	Timeout    time.Duration
	Verbose    bool
	Runner     NamedScriptRunner
}

// RunScript executes the named script from the selected composer.json.
// No lockfile is required. PHP-callable scripts still need vendor/autoload.php.
func RunScript(ctx context.Context, cmd ScriptCommand) error {
	dir, m, err := resolveScriptProject(cmd.ProjectDir)
	if err != nil {
		return err
	}
	if cmd.Name == "" {
		return fmt.Errorf("orchestrator: script name is required")
	}
	runner := cmd.Runner
	if runner == nil {
		runner = scripts.New()
	}
	return runner.RunNamed(ctx, cmd.Name, scripts.Options{
		ProjectDir: dir,
		Scripts:    m.Scripts,
		Verbose:    cmd.Verbose,
		ExtraArgs:  cmd.ExtraArgs,
		Timeout:    cmd.Timeout,
	})
}

// ListScripts returns the sorted script names from the selected composer.json.
func ListScripts(projectDir string) ([]string, error) {
	_, m, err := resolveScriptProject(projectDir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(m.Scripts))
	for name := range m.Scripts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// resolveScriptProject walks up from dir until it finds a composer.json and
// parses it. Unlike findWorkspaceRoot, it stops at the first manifest so a
// workspace member's scripts run in that member directory.
func resolveScriptProject(dir string) (string, *manifest.Manifest, error) {
	if dir == "" {
		return "", nil, fmt.Errorf("orchestrator: ProjectDir is required")
	}
	if st, err := os.Stat(dir); err != nil {
		_, loadErr := loadManifest(dir)
		return "", nil, loadErr
	} else if !st.IsDir() {
		return "", nil, fmt.Errorf("orchestrator: ProjectDir is not a directory: %s", dir)
	}
	cur := dir
	for {
		path := filepath.Join(cur, "composer.json")
		if _, err := os.Stat(path); err == nil {
			m, err := loadManifest(cur)
			if err != nil {
				return "", nil, err
			}
			return cur, m, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			_, err := loadManifest(dir)
			return "", nil, err
		}
		cur = parent
	}
}
