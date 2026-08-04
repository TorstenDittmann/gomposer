package autoload

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

// InstalledData describes the package metadata exposed through Composer's
// installed.php and InstalledVersions runtime API.
type InstalledData struct {
	Root     InstalledRoot
	Packages []InstalledPackage
}

type InstalledRoot struct {
	Name          string
	PrettyVersion string
	Version       string
	Reference     string
	Type          string
	Aliases       []string
	Dev           bool
}

type InstalledPackage struct {
	Name           string
	PrettyVersion  string
	Version        string
	Reference      string
	Type           string
	InstallPath    string
	Aliases        []string
	DevRequirement bool
}

func normalizeInstalled(data InstalledData) (InstalledData, error) {
	if data.Root.Name == "" {
		data.Root.Name = "__root__"
	}
	if data.Root.PrettyVersion == "" {
		data.Root.PrettyVersion = "1.0.0+no-version-set"
	}
	if data.Root.Version == "" {
		data.Root.Version = normalizeVersion(data.Root.PrettyVersion)
	}
	if data.Root.Type == "" {
		data.Root.Type = "project"
	}
	data.Root.Aliases = sortedUnique(data.Root.Aliases)

	seen := map[string]bool{strings.ToLower(data.Root.Name): true}
	packages := append([]InstalledPackage(nil), data.Packages...)
	for i := range packages {
		p := &packages[i]
		if p.Name == "" {
			return InstalledData{}, fmt.Errorf("autoload: installed package name is empty")
		}
		key := strings.ToLower(p.Name)
		if seen[key] {
			return InstalledData{}, fmt.Errorf("autoload: duplicate installed package %q", p.Name)
		}
		seen[key] = true
		if p.PrettyVersion == "" {
			p.PrettyVersion = "1.0.0+no-version-set"
		}
		if p.Version == "" {
			p.Version = normalizeVersion(p.PrettyVersion)
		}
		if p.Type == "" {
			p.Type = "library"
		}
		p.InstallPath = strings.TrimSuffix(path.Clean(strings.ReplaceAll(p.InstallPath, `\`, "/")), "/")
		if p.InstallPath == "." || strings.HasPrefix(p.InstallPath, "/") || p.InstallPath == ".." || strings.HasPrefix(p.InstallPath, "../") {
			return InstalledData{}, fmt.Errorf("autoload: invalid install path %q for %s", p.InstallPath, p.Name)
		}
		p.Aliases = sortedUnique(p.Aliases)
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].Name < packages[j].Name })
	data.Packages = packages
	return data, nil
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	values = append([]string(nil), values...)
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

// normalizeVersion is a fallback for roots, workspaces, and schema-v1 locks
// written before normalized versions were persisted. Registry packages retain
// the authoritative Composer-normalized value instead.
func normalizeVersion(version string) string {
	if version == "1.0.0+no-version-set" {
		return "1.0.0.0"
	}
	if strings.HasPrefix(version, "dev-") || strings.HasSuffix(version, "-dev") {
		return version
	}
	v := strings.TrimPrefix(version, "v")
	suffix := ""
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		suffix, v = v[i:], v[:i]
	}
	parts := strings.Split(v, ".")
	for len(parts) < 4 {
		parts = append(parts, "0")
	}
	if len(parts) > 4 {
		parts = parts[:4]
	}
	for _, part := range parts {
		if _, err := strconv.Atoi(part); err != nil {
			return version
		}
	}
	return strings.Join(parts, ".") + suffix
}
