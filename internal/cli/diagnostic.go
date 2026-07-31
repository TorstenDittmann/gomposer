package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/torstendittmann/gomposer/internal/fetcher"
	"github.com/torstendittmann/gomposer/internal/registry"
	"github.com/torstendittmann/gomposer/internal/resolver"
)

type diagnostic struct {
	title string
	body  string
	hint  string
}

func diagnose(err error) diagnostic {
	if err == nil {
		return diagnostic{}
	}
	if errors.Is(err, context.Canceled) {
		return diagnostic{title: "Cancelled."}
	}
	var conflict *resolver.ConflictError
	if errors.As(err, &conflict) {
		return diagnostic{
			title: "No compatible dependency set was found.",
			body:  conflict.Error(),
			hint:  "Adjust the conflicting constraints, then run `gomposer update`.",
		}
	}
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return diagnostic{title: "composer.json is not valid JSON.", body: fmt.Sprintf("Syntax error near byte %d.", syntax.Offset), hint: "Fix composer.json and run the command again."}
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && errors.Is(pathErr.Err, os.ErrNotExist) && strings.HasSuffix(pathErr.Path, "composer.json") {
		return diagnostic{title: "No composer.json was found.", body: pathErr.Path, hint: "Run from a PHP project directory or pass `--project <dir>`."}
	}
	if errors.Is(err, registry.ErrPackageNotFound) {
		return diagnostic{title: "A required package could not be found.", body: cleanError(err.Error()), hint: "Check the package name, stability constraint, and configured repositories."}
	}
	if errors.Is(err, fetcher.ErrShaMismatch) {
		return diagnostic{title: "A downloaded package failed checksum verification.", body: cleanError(err.Error()), hint: "Run `gomposer cache clear store` and retry."}
	}
	var detailed interface{ DiagnosticDetails() []string }
	if errors.As(err, &detailed) {
		return diagnostic{title: "Platform requirements are not satisfied.", body: strings.Join(detailed.DiagnosticDetails(), "\n"), hint: "Install or enable the missing requirements, or use `--ignore-platform-req=<name>` for a deliberate exception."}
	}
	var statusErr interface{ HTTPStatusCode() int }
	if errors.As(err, &statusErr) && (statusErr.HTTPStatusCode() == 401 || statusErr.HTTPStatusCode() == 403) {
		return diagnostic{title: "Authentication was rejected by the package server.", body: cleanError(err.Error()), hint: "Configure credentials in Composer or gomposer auth configuration and retry."}
	}
	msg := cleanError(err.Error())
	switch {
	case strings.Contains(msg, "php executable not found"):
		return diagnostic{title: "PHP was not found.", body: msg, hint: "Install PHP (`brew install php` or `apt install php-cli`) or pass `--ignore-platform`."}
	case strings.Contains(msg, "platform requirements unsatisfied"):
		return diagnostic{title: "Platform requirements are not satisfied.", body: msg, hint: "Install or enable the missing requirements, or use `--ignore-platform-req=<name>` for a deliberate exception."}
	case strings.Contains(msg, "status 401"), strings.Contains(msg, "status 403"), strings.Contains(msg, "unexpected status 401"), strings.Contains(msg, "unexpected status 403"):
		return diagnostic{title: "Authentication was rejected by the package server.", body: msg, hint: "Configure credentials in Composer or gomposer auth configuration and retry."}
	case strings.Contains(msg, "scripts:") || strings.Contains(msg, "script") && strings.Contains(msg, "failed"):
		return diagnostic{title: "A project script failed.", body: msg, hint: "Use `--no-scripts` to confirm the failure is script-specific, then fix the script."}
	default:
		return diagnostic{title: msg}
	}
}

func cleanError(s string) string {
	for {
		before := s
		for _, prefix := range []string{"orchestrator: ", "platform: ", "fetcher: ", "packagist: "} {
			s = strings.TrimPrefix(s, prefix)
		}
		if s == before {
			return s
		}
	}
}

func renderPlainFailure(w io.Writer, err error) {
	d := diagnose(err)
	fmt.Fprintf(w, "gomposer: error: %s\n", d.title)
	if d.body != "" && d.body != d.title {
		for _, line := range strings.Split(d.body, "\n") {
			fmt.Fprintf(w, "gomposer:   %s\n", line)
		}
	}
	if d.hint != "" {
		fmt.Fprintf(w, "gomposer: hint: %s\n", d.hint)
	}
}

func renderTTYFailure(w io.Writer, paint func(string, string) string, err error) {
	d := diagnose(err)
	fmt.Fprintf(w, "  %s\n", paint("1;31", d.title))
	if d.body != "" && d.body != d.title {
		for _, line := range strings.Split(d.body, "\n") {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	if d.hint != "" {
		fmt.Fprintf(w, "\n  %s %s\n", paint("1;36", "Hint:"), d.hint)
	}
}

// handledError marks an error that the journey reporter has already printed.
// Execute uses it to avoid the old second "gomposer: ..." line.
type handledError struct{ error }

func (e handledError) Unwrap() error { return e.error }

func markHandled(err error) error {
	if err == nil {
		return nil
	}
	return handledError{error: err}
}

func isHandled(err error) bool {
	var h handledError
	return errors.As(err, &h)
}
