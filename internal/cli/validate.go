package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/gomposer/internal/constraint"
	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/manifest"
)

var (
	errValidateWarnings = errors.New("validate: warnings present")
	errValidateErrors   = errors.New("validate: errors present")
	errValidateMissing  = errors.New("validate: composer.json missing or unreadable")
)

var exactVersionConstraint = regexp.MustCompile(`^[vV]?\d+\.\d+\.\d+([.-][0-9A-Za-z.-]+)?$`)

type validateIssue struct {
	Section string
	Message string
}

type validateResult struct {
	Errors    []validateIssue
	Publish   []validateIssue
	Warnings  []validateIssue
	LockPath  string
	FileLabel string
}

func newValidateCmd() *cobra.Command {
	var (
		projectDir     string
		noCheckAll     bool
		noCheckLock    bool
		noCheckPublish bool
		noCheckVersion bool
		strict         bool
	)
	cmd := &cobra.Command{
		Use:   "validate [file]",
		Short: "Validate composer.json and gomposer.lock",
		Long:  "Validate composer.json for schema and constraint errors. When gomposer.lock is present, also check that it matches the manifest.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveValidatePath(projectDir, args)
			if err != nil {
				if errors.Is(err, errValidateMissing) {
					fmt.Fprintln(cmd.ErrOrStderr(), err.Error())
					return markHandled(err)
				}
				return err
			}
			result, err := runValidate(path, validateOptions{
				NoCheckAll:     noCheckAll,
				NoCheckLock:    noCheckLock,
				NoCheckPublish: noCheckPublish,
				NoCheckVersion: noCheckVersion,
			})
			if err != nil {
				if errors.Is(err, errValidateMissing) {
					fmt.Fprintln(cmd.ErrOrStderr(), err.Error())
					return markHandled(err)
				}
				return err
			}
			if !flagQuiet {
				writeValidateReport(cmd.OutOrStdout(), result, noCheckPublish)
			}
			switch {
			case len(result.Errors) > 0 || (!noCheckPublish && len(result.Publish) > 0):
				return markHandled(errValidateErrors)
			case strict && len(result.Warnings) > 0:
				return markHandled(errValidateWarnings)
			default:
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	cmd.Flags().BoolVar(&noCheckAll, "no-check-all", false, "do not warn about unbound or exact version constraints")
	cmd.Flags().BoolVar(&noCheckLock, "no-check-lock", false, "do not check whether gomposer.lock is up to date")
	cmd.Flags().BoolVar(&noCheckPublish, "no-check-publish", false, "do not treat publishability problems as errors")
	cmd.Flags().BoolVar(&noCheckVersion, "no-check-version", false, "do not warn when the version field is present")
	cmd.Flags().BoolVar(&strict, "strict", false, "return a non-zero exit code for warnings as well as errors")
	return cmd
}

type validateOptions struct {
	NoCheckAll     bool
	NoCheckLock    bool
	NoCheckPublish bool
	NoCheckVersion bool
}

func resolveValidatePath(projectDir string, args []string) (string, error) {
	if len(args) == 1 {
		path := args[0]
		if !filepath.IsAbs(path) {
			wd, err := os.Getwd()
			if err != nil {
				return "", err
			}
			path = filepath.Join(wd, path)
		}
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("%w: %s", errValidateMissing, path)
			}
			return "", fmt.Errorf("%w: %v", errValidateMissing, err)
		}
		return path, nil
	}
	dir := projectDir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "composer.json")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", errValidateMissing, path)
		}
		return "", fmt.Errorf("%w: %v", errValidateMissing, err)
	}
	return path, nil
}

func runValidate(path string, opts validateOptions) (validateResult, error) {
	result := validateResult{FileLabel: displayValidatePath(path)}
	body, err := os.ReadFile(path)
	if err != nil {
		return result, fmt.Errorf("%w: %v", errValidateMissing, err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		result.Errors = append(result.Errors, validateIssue{
			Section: "error",
			Message: fmt.Sprintf("%s does not contain valid JSON: %v", result.FileLabel, err),
		})
		return result, nil
	}
	if raw == nil {
		result.Errors = append(result.Errors, validateIssue{
			Section: "error",
			Message: fmt.Sprintf("%s top-level value must be an object", result.FileLabel),
		})
		return result, nil
	}

	m, err := manifest.Parse(body)
	if err != nil {
		result.Errors = append(result.Errors, validateIssue{
			Section: "error",
			Message: err.Error(),
		})
		return result, nil
	}

	dir := filepath.Dir(path)
	validatePackageIdentity(&result, m, raw, opts)
	validateRequirementMaps(&result, m, opts)
	validateRepositories(&result, m)
	validateStability(&result, m)
	workspaces := validateWorkspaces(&result, dir, m)

	if !opts.NoCheckLock {
		result.LockPath = filepath.Join(dir, "gomposer.lock")
		validateLock(&result, dir, body, m, workspaces)
	}
	return result, nil
}

func displayValidatePath(path string) string {
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(wd, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return "./" + filepath.ToSlash(rel)
}

func validatePackageIdentity(result *validateResult, m *manifest.Manifest, raw map[string]json.RawMessage, opts validateOptions) {
	if m.Name == "" {
		result.Publish = append(result.Publish, validateIssue{
			Section: "publish",
			Message: "name : The property name is required",
		})
	} else if !composerPackageName.MatchString(m.Name) {
		result.Errors = append(result.Errors, validateIssue{
			Section: "error",
			Message: fmt.Sprintf("name %q is invalid, it should be lowercase and match vendor/package", m.Name),
		})
	}

	if _, ok := raw["description"]; !ok {
		result.Publish = append(result.Publish, validateIssue{
			Section: "publish",
			Message: "description : The property description is required",
		})
	} else {
		var desc string
		if err := json.Unmarshal(raw["description"], &desc); err != nil || strings.TrimSpace(desc) == "" {
			result.Publish = append(result.Publish, validateIssue{
				Section: "publish",
				Message: "description : The property description is required",
			})
		}
	}

	if rawLicense, ok := raw["license"]; !ok {
		result.Warnings = append(result.Warnings, validateIssue{
			Section: "warning",
			Message: "No license specified, it is recommended to do so. For closed-source software you may use \"proprietary\" as license.",
		})
	} else if err := validateLicenseValue(rawLicense); err != nil {
		result.Errors = append(result.Errors, validateIssue{
			Section: "error",
			Message: err.Error(),
		})
	}

	if !opts.NoCheckVersion {
		if _, ok := raw["version"]; ok {
			result.Warnings = append(result.Warnings, validateIssue{
				Section: "warning",
				Message: "The version field is present, it is recommended to leave it out if the package is published on Packagist.",
			})
		}
	}
}

func validateLicenseValue(raw json.RawMessage) error {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if strings.TrimSpace(single) == "" {
			return fmt.Errorf("license : must be a non-empty string or array of strings")
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		if len(many) == 0 {
			return fmt.Errorf("license : must be a non-empty string or array of strings")
		}
		for _, item := range many {
			if strings.TrimSpace(item) == "" {
				return fmt.Errorf("license : array entries must be non-empty strings")
			}
		}
		return nil
	}
	return fmt.Errorf("license : must be a string or array of strings")
}

func validateRequirementMaps(result *validateResult, m *manifest.Manifest, opts validateOptions) {
	check := func(field string, reqs map[string]string) {
		names := sortedMapKeys(reqs)
		for _, name := range names {
			constraintText := reqs[name]
			if !validRequirementName(name) {
				result.Errors = append(result.Errors, validateIssue{
					Section: "error",
					Message: fmt.Sprintf("%s.%s : invalid package name %q", field, name, name),
				})
				continue
			}
			if strings.TrimSpace(constraintText) == "" {
				result.Errors = append(result.Errors, validateIssue{
					Section: "error",
					Message: fmt.Sprintf("%s.%s : constraint must not be empty", field, name),
				})
				continue
			}
			if _, err := constraint.Parse(constraintText); err != nil {
				result.Errors = append(result.Errors, validateIssue{
					Section: "error",
					Message: fmt.Sprintf("%s.%s : invalid version constraint %q: %v", field, name, constraintText, err),
				})
				continue
			}
			if opts.NoCheckAll {
				continue
			}
			trimmed := strings.TrimSpace(constraintText)
			if trimmed == "*" {
				result.Warnings = append(result.Warnings, validateIssue{
					Section: "warning",
					Message: fmt.Sprintf("%s.%s : unbound version constraints (*) should be avoided", field, name),
				})
			} else if exactVersionConstraint.MatchString(trimmed) {
				result.Warnings = append(result.Warnings, validateIssue{
					Section: "warning",
					Message: fmt.Sprintf("%s.%s : exact version constraints (%s) should be avoided if the package follows semantic versioning", field, name, trimmed),
				})
			}
		}
	}
	check("require", m.Require)
	check("require-dev", m.RequireDev)
}

func validateRepositories(result *validateResult, m *manifest.Manifest) {
	for i, repo := range m.Repositories {
		if err := repo.Validate(); err != nil {
			result.Errors = append(result.Errors, validateIssue{
				Section: "error",
				Message: fmt.Sprintf("repositories[%d] : %v", i, err),
			})
		}
	}
}

func validateStability(result *validateResult, m *manifest.Manifest) {
	if m.MinimumStability == "" {
		return
	}
	switch strings.ToLower(m.MinimumStability) {
	case "stable", "rc", "beta", "alpha", "dev":
		return
	default:
		result.Errors = append(result.Errors, validateIssue{
			Section: "error",
			Message: fmt.Sprintf("minimum-stability %q is invalid (want stable, RC, beta, alpha, or dev)", m.MinimumStability),
		})
	}
}

func validateWorkspaces(result *validateResult, dir string, m *manifest.Manifest) []manifest.Workspace {
	if len(m.Workspaces) == 0 {
		return nil
	}
	workspaces, err := manifest.DiscoverWorkspaces(dir, m, func(format string, args ...any) {
		result.Warnings = append(result.Warnings, validateIssue{
			Section: "warning",
			Message: fmt.Sprintf(format, args...),
		})
	})
	if err != nil {
		result.Errors = append(result.Errors, validateIssue{
			Section: "error",
			Message: err.Error(),
		})
		return nil
	}
	return workspaces
}

func validateLock(result *validateResult, dir string, manifestBytes []byte, m *manifest.Manifest, workspaces []manifest.Workspace) {
	lockPath := filepath.Join(dir, "gomposer.lock")
	body, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		result.Errors = append(result.Errors, validateIssue{
			Section: "lock",
			Message: fmt.Sprintf("could not read gomposer.lock: %v", err),
		})
		return
	}
	file, err := lock.Decode(body)
	if err != nil {
		result.Errors = append(result.Errors, validateIssue{
			Section: "lock",
			Message: err.Error(),
		})
		return
	}

	sum := sha256.Sum256(manifestBytes)
	wantHash := "sha256:" + hex.EncodeToString(sum[:])
	switch {
	case file.ManifestContentHash == "":
		result.Errors = append(result.Errors, validateIssue{
			Section: "lock",
			Message: "gomposer.lock is missing manifestContentHash; run `gomposer update` to rebuild it.",
		})
	case file.ManifestContentHash != wantHash:
		result.Errors = append(result.Errors, validateIssue{
			Section: "lock",
			Message: "The lock file is not up to date with the latest changes in composer.json, it is recommended that you run `gomposer update`.",
		})
	}

	byName := make(map[string]lock.Package, len(file.Packages)+len(file.PackagesDev))
	for _, pkg := range file.Packages {
		byName[pkg.Name] = pkg
	}
	for _, pkg := range file.PackagesDev {
		byName[pkg.Name] = pkg
	}

	checkLocked := func(reqs map[string]string) {
		for _, name := range sortedMapKeys(reqs) {
			if !composerPackageName.MatchString(name) {
				continue // platform packages are not locked
			}
			constraintText := reqs[name]
			c, err := constraint.Parse(constraintText)
			if err != nil {
				continue // already reported
			}
			if c.IsWorkspace {
				continue
			}
			pkg, ok := byName[name]
			if !ok {
				result.Errors = append(result.Errors, validateIssue{
					Section: "lock",
					Message: fmt.Sprintf("Required package %q is missing from gomposer.lock.", name),
				})
				continue
			}
			ver, err := constraint.ParseVersion(pkg.Version)
			if err != nil {
				result.Errors = append(result.Errors, validateIssue{
					Section: "lock",
					Message: fmt.Sprintf("Required package %q is in the lock file as %q but that version could not be parsed.", name, pkg.Version),
				})
				continue
			}
			if !c.Satisfies(ver) {
				result.Errors = append(result.Errors, validateIssue{
					Section: "lock",
					Message: fmt.Sprintf("Required package %q is in the lock file as %q but that does not satisfy your constraint %q.", name, pkg.Version, constraintText),
				})
			}
		}
	}
	checkLocked(m.Require)
	// A no-dev lock (gomposer update --no-dev) legitimately omits packagesDev.
	includeDev := len(file.PackagesDev) > 0
	if includeDev {
		checkLocked(m.RequireDev)
	}
	for _, ws := range workspaces {
		if ws.Manifest == nil {
			continue
		}
		checkLocked(ws.Manifest.Require)
		if includeDev {
			checkLocked(ws.Manifest.RequireDev)
		}
	}

	for _, pkg := range file.Packages {
		if pkg.Type != "workspace" {
			continue
		}
		rel := pkg.Source.URL
		if rel == "" {
			result.Errors = append(result.Errors, validateIssue{
				Section: "lock",
				Message: fmt.Sprintf("Workspace package %q is missing a path in gomposer.lock.", pkg.Name),
			})
			continue
		}
		wsPath := rel
		if !filepath.IsAbs(wsPath) {
			wsPath = filepath.Join(dir, rel)
		}
		if _, err := os.Stat(filepath.Join(wsPath, "composer.json")); err != nil {
			result.Errors = append(result.Errors, validateIssue{
				Section: "lock",
				Message: fmt.Sprintf("Workspace package %q path %q is missing or unreadable.", pkg.Name, rel),
			})
		}
	}
}

func writeValidateReport(w io.Writer, result validateResult, noCheckPublish bool) {
	hasPublish := !noCheckPublish && len(result.Publish) > 0
	hasErrors := len(result.Errors) > 0 || hasPublish
	hasWarnings := len(result.Warnings) > 0

	switch {
	case !hasErrors && !hasWarnings:
		fmt.Fprintf(w, "%s is valid\n", result.FileLabel)
	case !hasErrors && hasWarnings:
		fmt.Fprintf(w, "%s is valid, but with a few warnings\n", result.FileLabel)
		fmt.Fprintln(w, "See https://getcomposer.org/doc/04-schema.md for details on the schema")
	case hasPublish && len(result.Errors) == 0:
		fmt.Fprintf(w, "%s is valid for simple usage with gomposer but has\n", result.FileLabel)
		fmt.Fprintln(w, "strict errors that make it unable to be published as a package")
		fmt.Fprintln(w, "See https://getcomposer.org/doc/04-schema.md for details on the schema")
	default:
		fmt.Fprintf(w, "%s is invalid, the following errors/warnings were found:\n", result.FileLabel)
	}

	writeIssueSection(w, "Publish errors", result.Publish, !noCheckPublish)
	writeIssueSection(w, "Lock file errors", filterIssues(result.Errors, "lock"), true)
	writeIssueSection(w, "Errors", filterIssues(result.Errors, "error"), true)
	writeIssueSection(w, "General warnings", result.Warnings, true)
}

func filterIssues(issues []validateIssue, section string) []validateIssue {
	out := make([]validateIssue, 0, len(issues))
	for _, issue := range issues {
		if issue.Section == section {
			out = append(out, issue)
		}
	}
	return out
}

func writeIssueSection(w io.Writer, title string, issues []validateIssue, enabled bool) {
	if !enabled || len(issues) == 0 {
		return
	}
	fmt.Fprintf(w, "# %s\n", title)
	for _, issue := range issues {
		fmt.Fprintf(w, "- %s\n", issue.Message)
	}
}

// sort helper already exists as sortedMapKeys in show.go
