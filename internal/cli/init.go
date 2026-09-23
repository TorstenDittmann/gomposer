package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	var (
		projectDir  string
		name        string
		description string
		author      string
		pkgType     string
		homepage    string
		requires    []string
		requireDevs []string
		stability   string
		license     string
		autoload    string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a basic composer.json in the current directory",
		Long:  "Create a basic composer.json from flags. gomposer is non-interactive; pass --name and other fields explicitly (Composer-compatible option names).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := projectDir
			if dir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				dir = wd
			}
			dir, err := filepath.Abs(dir)
			if err != nil {
				return err
			}

			doc, tips, err := buildInitDocument(dir, initOptions{
				Name:        name,
				Description: description,
				Author:      author,
				Type:        pkgType,
				Homepage:    homepage,
				Requires:    requires,
				RequireDevs: requireDevs,
				Stability:   stability,
				License:     license,
				Autoload:    autoload,
			})
			if err != nil {
				return err
			}

			path := filepath.Join(dir, "composer.json")
			body, err := encodeInitDocument(doc)
			if err != nil {
				return err
			}

			// Create the autoload directory first so a failed MkdirAll does not
			// leave behind a composer.json that blocks retries.
			if autoload != "" {
				autoloadDir := filepath.Join(dir, filepath.Clean(autoload))
				if err := os.MkdirAll(autoloadDir, 0o755); err != nil {
					return fmt.Errorf("init: create autoload directory: %w", err)
				}
			}

			// O_EXCL makes the exists-check and create atomic against concurrent init.
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err != nil {
				if os.IsExist(err) {
					return fmt.Errorf("init: %s already exists", path)
				}
				return fmt.Errorf("init: write %s: %w", path, err)
			}
			if _, err := file.Write(body); err != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return fmt.Errorf("init: write %s: %w", path, err)
			}
			if err := file.Close(); err != nil {
				_ = os.Remove(path)
				return fmt.Errorf("init: write %s: %w", path, err)
			}

			if !flagQuiet {
				fmt.Fprintf(cmd.OutOrStdout(), "Writing %s\n", displayValidatePath(path))
				for _, tip := range tips {
					fmt.Fprintln(cmd.OutOrStdout(), tip)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory for composer.json (defaults to cwd)")
	cmd.Flags().StringVar(&name, "name", "", "name of the package (vendor/package)")
	cmd.Flags().StringVar(&description, "description", "", "description of the package")
	cmd.Flags().StringVar(&author, "author", "", `author name of the package, e.g. "Name <email@example.com>"`)
	cmd.Flags().StringVar(&pkgType, "type", "", "type of package (e.g. library, project)")
	cmd.Flags().StringVar(&homepage, "homepage", "", "homepage of the package")
	cmd.Flags().StringArrayVar(&requires, "require", nil, "package to require with a version constraint (repeatable)")
	cmd.Flags().StringArrayVar(&requireDevs, "require-dev", nil, "development requirement (repeatable)")
	cmd.Flags().StringVarP(&stability, "stability", "s", "", "minimum-stability (stable, RC, beta, alpha, or dev)")
	cmd.Flags().StringVarP(&license, "license", "l", "", "license of the package")
	cmd.Flags().StringVarP(&autoload, "autoload", "a", "", "add a PSR-4 autoload mapping for this relative directory (e.g. src/)")
	return cmd
}

type initOptions struct {
	Name        string
	Description string
	Author      string
	Type        string
	Homepage    string
	Requires    []string
	RequireDevs []string
	Stability   string
	License     string
	Autoload    string
}

type initAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

type initDocument struct {
	Name             string                       `json:"name"`
	Description      string                       `json:"description,omitempty"`
	Type             string                       `json:"type,omitempty"`
	License          string                       `json:"license,omitempty"`
	Homepage         string                       `json:"homepage,omitempty"`
	Autoload         *initAutoload                `json:"autoload,omitempty"`
	Authors          []initAuthor                 `json:"authors,omitempty"`
	Require          map[string]string            `json:"require"`
	RequireDev       map[string]string            `json:"require-dev,omitempty"`
	MinimumStability string                       `json:"minimum-stability,omitempty"`
}

type initAutoload struct {
	PSR4 map[string]string `json:"psr-4"`
}

func buildInitDocument(dir string, opts initOptions) (initDocument, []string, error) {
	var empty initDocument
	pkgName := strings.TrimSpace(opts.Name)
	if pkgName == "" {
		pkgName = defaultPackageName(dir)
	}
	if !composerPackageName.MatchString(pkgName) {
		return empty, nil, fmt.Errorf("init: package name %q is invalid, it should be lowercase and match vendor/package", pkgName)
	}

	require, err := parseInitRequirements(opts.Requires)
	if err != nil {
		return empty, nil, err
	}
	requireDev, err := parseInitRequirements(opts.RequireDevs)
	if err != nil {
		return empty, nil, err
	}

	stability := strings.TrimSpace(opts.Stability)
	if stability != "" {
		switch strings.ToLower(stability) {
		case "stable", "rc", "beta", "alpha", "dev":
			if strings.EqualFold(stability, "rc") {
				stability = "RC"
			} else {
				stability = strings.ToLower(stability)
			}
		default:
			return empty, nil, fmt.Errorf("init: invalid --stability %q (want stable, RC, beta, alpha, or dev)", opts.Stability)
		}
	}

	doc := initDocument{
		Name:             pkgName,
		Description:      strings.TrimSpace(opts.Description),
		Type:             strings.TrimSpace(opts.Type),
		License:          strings.TrimSpace(opts.License),
		Homepage:         strings.TrimSpace(opts.Homepage),
		Require:          require,
		RequireDev:       requireDev,
		MinimumStability: stability,
	}

	author, err := resolveInitAuthor(opts.Author)
	if err != nil {
		return empty, nil, err
	}
	if author != nil {
		doc.Authors = []initAuthor{*author}
	}

	var tips []string
	if auto := strings.TrimSpace(opts.Autoload); auto != "" {
		auto = filepath.ToSlash(auto)
		if !strings.HasSuffix(auto, "/") {
			auto += "/"
		}
		ns := psr4Namespace(pkgName)
		doc.Autoload = &initAutoload{PSR4: map[string]string{ns: auto}}
		tips = append(tips,
			fmt.Sprintf("PSR-4 autoloading configured. Use \"namespace %s;\" in %s", strings.TrimSuffix(ns, "\\"), auto),
			"Run `gomposer install` then include the autoloader with: require 'vendor/autoload.php';",
		)
	}

	return doc, tips, nil
}

func encodeInitDocument(doc initDocument) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "    ")
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("init: encode composer.json: %w", err)
	}
	return buf.Bytes(), nil
}

func parseInitRequirements(values []string) (map[string]string, error) {
	out := make(map[string]string)
	for _, value := range values {
		spec, err := parseInitRequirement(value)
		if err != nil {
			return nil, err
		}
		out[spec.Name] = spec.Constraint
	}
	return out, nil
}

func parseInitRequirement(input string) (requirementSpec, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return requirementSpec{}, fmt.Errorf("init: empty --require value")
	}
	// Accept Composer forms: name:constraint, name=constraint, "name constraint".
	separators := []string{":", "=", " "}
	for _, sep := range separators {
		if name, version, ok := strings.Cut(input, sep); ok {
			name = strings.TrimSpace(name)
			version = strings.TrimSpace(version)
			if name != "" && version != "" {
				return parseRequirement(name + ":" + version)
			}
		}
	}
	return parseRequirement(input)
}

func resolveInitAuthor(raw string) (*initAuthor, error) {
	raw = strings.TrimSpace(raw)
	if raw != "" {
		author, err := parseAuthor(raw)
		if err != nil {
			return nil, err
		}
		return &author, nil
	}
	name := strings.TrimSpace(gitConfigValue("user.name"))
	email := strings.TrimSpace(gitConfigValue("user.email"))
	if name == "" && email == "" {
		return nil, nil
	}
	if name == "" {
		name = email
	}
	return &initAuthor{Name: name, Email: email}, nil
}

var authorPattern = regexp.MustCompile(`^([^<]+?)\s*<([^>]+)>$`)

func parseAuthor(raw string) (initAuthor, error) {
	if m := authorPattern.FindStringSubmatch(raw); m != nil {
		return initAuthor{Name: strings.TrimSpace(m[1]), Email: strings.TrimSpace(m[2])}, nil
	}
	if strings.Contains(raw, "@") && !strings.Contains(raw, " ") {
		return initAuthor{Name: raw, Email: raw}, nil
	}
	return initAuthor{Name: raw}, nil
}

func gitConfigValue(key string) string {
	cmd := exec.Command("git", "config", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func defaultPackageName(dir string) string {
	vendor := sanitizePackageSegment(os.Getenv("USER"))
	if vendor == "" {
		vendor = sanitizePackageSegment(os.Getenv("USERNAME"))
	}
	if vendor == "" {
		vendor = "vendor"
	}
	base := sanitizePackageSegment(filepath.Base(dir))
	if base == "" {
		base = "package"
	}
	return vendor + "/" + base
}

func sanitizePackageSegment(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '_' || r == '.' || r == '-':
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	return out
}

func psr4Namespace(packageName string) string {
	parts := strings.Split(packageName, "/")
	ns := make([]string, 0, len(parts))
	for _, part := range parts {
		ns = append(ns, pascalCaseSegment(part))
	}
	return strings.Join(ns, "\\") + "\\"
}

func pascalCaseSegment(s string) string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
	var b strings.Builder
	for _, field := range fields {
		if field == "" {
			continue
		}
		runes := []rune(field)
		runes[0] = unicode.ToUpper(runes[0])
		for i := 1; i < len(runes); i++ {
			runes[i] = unicode.ToLower(runes[i])
		}
		b.WriteString(string(runes))
	}
	return b.String()
}
