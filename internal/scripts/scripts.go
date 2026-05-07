// Package scripts executes user-defined script entries from composer.json
// at install/update lifecycle events.
//
// Three script forms are accepted:
//
//   - Shell command: any string that does not match the other forms. Executed
//     via `sh -c <cmd>` on Unix. Working dir = project root, env inherited
//     plus COMPOSER_GO=1.
//   - PHP-callable: a string matching `Vendor\Class::method` or
//     `\Vendor\Class::method`. Executed via `php -r` after requiring
//     vendor/autoload.php. The method receives no arguments in stage 2;
//     synthetic Composer\Script\Event injection is a future plan.
//   - Composer-script ref: a string of the exact form `@<name>` (no
//     whitespace). Resolved by looking up `<name>` in the same scripts map.
//     Recursive with cycle detection.
//
// An event's value is []string; entries fire sequentially with fail-fast on
// non-zero exit. A failing script returns an error wrapping the redacted
// body (first 100 chars), the event name, and the exit code.
package scripts

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Event is a Composer lifecycle event name (e.g. "post-install-cmd").
type Event string

const (
	EventPreInstall       Event = "pre-install-cmd"
	EventPostInstall      Event = "post-install-cmd"
	EventPreUpdate        Event = "pre-update-cmd"
	EventPostUpdate       Event = "post-update-cmd"
	EventPreAutoloadDump  Event = "pre-autoload-dump"
	EventPostAutoloadDump Event = "post-autoload-dump"
)

// Options configures a Run call.
type Options struct {
	// ProjectDir is the working directory for every script. Required.
	ProjectDir string
	// Scripts is the full event->bodies map from manifest.Manifest.Scripts.
	// Required. May be empty (Run becomes a no-op).
	Scripts map[string][]string
	// Verbose logs the body of each script before running it (redacted).
	Verbose bool
}

// Runner is the interface the orchestrator imports. The default
// implementation runs subprocesses; tests inject a fake.
type Runner interface {
	Run(ctx context.Context, event Event, opts Options) error
}

// New returns the default subprocess-based runner.
func New() Runner { return &defaultRunner{} }

type defaultRunner struct{}

// Run executes every entry under opts.Scripts[event] in order. A no-op when
// the event has no entries. Returns the first non-nil error.
func (r *defaultRunner) Run(ctx context.Context, event Event, opts Options) error {
	bodies, ok := opts.Scripts[string(event)]
	if !ok || len(bodies) == 0 {
		return nil
	}
	if opts.ProjectDir == "" {
		return errors.New("scripts: ProjectDir is required")
	}
	visited := make(map[string]struct{})
	for _, body := range bodies {
		if err := r.runOne(ctx, event, body, opts, visited, 0); err != nil {
			return err
		}
	}
	return nil
}

const maxRefDepth = 32

func (r *defaultRunner) runOne(ctx context.Context, event Event, body string, opts Options, visited map[string]struct{}, depth int) error {
	if depth > maxRefDepth {
		return fmt.Errorf("scripts: %s: ref depth exceeded %d (cycle?)", event, maxRefDepth)
	}
	form, a, b, err := classify(body)
	if err != nil {
		return fmt.Errorf("scripts: %s: %w", event, err)
	}
	switch form {
	case formRef:
		name := a
		if _, seen := visited[name]; seen {
			return fmt.Errorf("scripts: %s: cycle through @%s", event, name)
		}
		visited[name] = struct{}{}
		nested, ok := opts.Scripts[name]
		if !ok {
			return fmt.Errorf("scripts: %s: unknown ref @%s", event, name)
		}
		for _, sub := range nested {
			if err := r.runOne(ctx, event, sub, opts, visited, depth+1); err != nil {
				return err
			}
		}
		// Allow the same ref to appear in independent branches by clearing on
		// the way out. Cycle detection still catches A->B->A because the path
		// through B sees A in `visited` before the recursion returns.
		delete(visited, name)
		return nil
	case formShell:
		return runShell(ctx, body, opts)
	case formPHPCallable:
		class, method := a, b
		return runPHPCallable(ctx, class, method, opts)
	default:
		return fmt.Errorf("scripts: %s: internal: unknown form", event)
	}
}

type form int

const (
	formShell form = iota
	formPHPCallable
	formRef
)

// phpCallablePattern matches `Foo\Bar::method` or `\Foo\Bar::method`.
// We require the entire body to match (no leading/trailing whitespace, no
// trailing args). Anything with parens, semicolons, or shell metachars falls
// through to shell.
var phpCallablePattern = regexp.MustCompile(`^\\?[A-Za-z_][A-Za-z0-9_]*(\\[A-Za-z_][A-Za-z0-9_]*)*::[A-Za-z_][A-Za-z0-9_]*$`)

// refPattern matches `@name` with no whitespace. `@php artisan ...` is NOT a
// ref (whitespace), so it falls through to shell where the user's `php`
// binary handles it.
var refPattern = regexp.MustCompile(`^@([A-Za-z_][A-Za-z0-9_:.\-]*)$`)

// classify returns the script form along with form-specific extras:
//   - formShell:        a, b unused
//   - formPHPCallable:  a = class, b = method
//   - formRef:          a = referenced name, b unused
func classify(body string) (form, string, string, error) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return 0, "", "", errors.New("empty script body")
	}
	if m := refPattern.FindStringSubmatch(trimmed); m != nil {
		return formRef, m[1], "", nil
	}
	if phpCallablePattern.MatchString(trimmed) {
		i := strings.Index(trimmed, "::")
		return formPHPCallable, trimmed[:i], trimmed[i+2:], nil
	}
	return formShell, "", "", nil
}

// redactBody truncates a script body to 100 chars + "..." for safe inclusion
// in error messages. Scripts may contain credentials passed via env-derived
// command substitution; truncation is a defense-in-depth measure.
func redactBody(body string) string {
	const max = 100
	if len(body) <= max {
		return body
	}
	return body[:max] + "..."
}
