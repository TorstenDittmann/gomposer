package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/gomposer/internal/auth"
	"github.com/torstendittmann/gomposer/internal/constraint"
	"github.com/torstendittmann/gomposer/internal/lock"
)

const maxAuditResponse = 32 << 20

var (
	errAuditFindings = errors.New("security vulnerability advisories found")
	auditEndpoint    = "https://packagist.org/api/security-advisories/"
	auditHTTPClient  = &http.Client{Timeout: 10 * time.Second}
)

type securityAdvisory struct {
	AdvisoryID       string           `json:"advisoryId"`
	PackageName      string           `json:"packageName"`
	RemoteID         string           `json:"remoteId,omitempty"`
	Title            string           `json:"title"`
	Link             string           `json:"link,omitempty"`
	CVE              string           `json:"cve,omitempty"`
	AffectedVersions string           `json:"affectedVersions"`
	Source           string           `json:"source,omitempty"`
	Sources          []advisorySource `json:"sources"`
	ReportedAt       string           `json:"reportedAt,omitempty"`
	Severity         string           `json:"severity,omitempty"`
	InstalledVersion string           `json:"installedVersion,omitempty"`
}

type advisorySource struct {
	Name     string `json:"name"`
	RemoteID string `json:"remoteId"`
}

type auditResponse struct {
	Advisories map[string][]securityAdvisory `json:"advisories"`
}

type auditResult struct {
	Advisories map[string][]securityAdvisory `json:"advisories"`
	Checked    []string                      `json:"checked"`
	Unknown    []string                      `json:"unknown"`
}

func newAuditCmd() *cobra.Command {
	var projectDir, format string
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Check locked packages for security advisories",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return fmt.Errorf("invalid --format value %q (want text or json)", format)
			}
			_, _, _, file, err := loadInspectionProject(projectDir, "audit")
			if err != nil {
				return err
			}
			packages := append([]lock.Package(nil), file.Packages...)
			if !flagNoDev {
				packages = append(packages, file.PackagesDev...)
			}
			versions := make(map[string]string)
			for _, pkg := range packages {
				if pkg.Type == "workspace" || pkg.Name == "" {
					continue
				}
				name := strings.ToLower(pkg.Name)
				version := pkg.VersionNormalized
				if version == "" {
					version = pkg.Version
				}
				if old, exists := versions[name]; exists && old != version {
					return fmt.Errorf("audit: package %q is locked at multiple versions", name)
				}
				versions[name] = version
			}
			names := make([]string, 0, len(versions))
			for name := range versions {
				names = append(names, name)
			}
			sort.Strings(names)
			if len(names) == 0 {
				if !flagQuiet {
					if format == "json" {
						return writeShowJSON(cmd.OutOrStdout(), auditResult{Advisories: map[string][]securityAdvisory{}, Checked: []string{}, Unknown: []string{}})
					}
					fmt.Fprintln(cmd.OutOrStdout(), "No packages - skipping audit.")
				}
				return nil
			}
			response, err := fetchAdvisories(cmd.Context(), names)
			if err != nil {
				return err
			}
			result, findings, err := matchAdvisories(names, versions, response)
			if err != nil {
				return err
			}
			if !flagQuiet {
				if format == "json" {
					err = writeShowJSON(cmd.OutOrStdout(), result)
				} else if findings == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No security vulnerability advisories found.")
				} else {
					writeAudit(cmd.OutOrStdout(), result, findings)
				}
				if err != nil {
					return err
				}
			}
			if findings > 0 {
				return markHandled(errAuditFindings)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	cmd.Flags().StringVarP(&format, "format", "f", "text", "output format: text or json")
	return cmd
}

func fetchAdvisories(ctx context.Context, names []string) (*auditResponse, error) {
	values := url.Values{}
	for _, name := range names {
		values.Add("packages[]", name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, auditEndpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("audit: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gomposer (+https://github.com/TorstenDittmann/gomposer)")
	if store, err := auth.Load(); err == nil && store != nil {
		if credentials, ok := store.Lookup(req.URL.Host); ok {
			credentials.Apply(req)
		}
	}
	resp, err := auditHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("audit: request advisories: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("audit: advisory service returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAuditResponse+1))
	if err != nil {
		return nil, fmt.Errorf("audit: read response: %w", err)
	}
	if len(body) > maxAuditResponse {
		return nil, errors.New("audit: advisory response exceeds 32 MiB")
	}
	var response auditResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("audit: decode response: %w", err)
	}
	if response.Advisories == nil {
		return nil, errors.New("audit: advisory response is missing advisories")
	}
	return &response, nil
}

func matchAdvisories(names []string, versions map[string]string, response *auditResponse) (auditResult, int, error) {
	result := auditResult{Advisories: make(map[string][]securityAdvisory), Checked: []string{}, Unknown: []string{}}
	findings := 0
	for _, name := range names {
		advisories, known := response.Advisories[name]
		if !known {
			result.Unknown = append(result.Unknown, name)
			continue
		}
		result.Checked = append(result.Checked, name)
		version, err := constraint.ParseVersion(versions[name])
		if err != nil {
			return auditResult{}, 0, fmt.Errorf("audit: invalid locked version %q for %s: %w", versions[name], name, err)
		}
		for _, advisory := range advisories {
			if advisory.PackageName != "" && !strings.EqualFold(advisory.PackageName, name) {
				return auditResult{}, 0, fmt.Errorf("audit: advisory %q package name %q does not match %q", advisory.AdvisoryID, advisory.PackageName, name)
			}
			affected, err := constraint.Parse(advisory.AffectedVersions)
			if err != nil {
				return auditResult{}, 0, fmt.Errorf("audit: advisory %q for %s has invalid affectedVersions %q: %w", advisory.AdvisoryID, name, advisory.AffectedVersions, err)
			}
			if affected.Satisfies(version) {
				advisory.PackageName = name
				advisory.InstalledVersion = versions[name]
				if advisory.Sources == nil {
					advisory.Sources = []advisorySource{}
				}
				result.Advisories[name] = append(result.Advisories[name], advisory)
				findings++
			}
		}
		sort.Slice(result.Advisories[name], func(i, j int) bool {
			return result.Advisories[name][i].AdvisoryID < result.Advisories[name][j].AdvisoryID
		})
	}
	return result, findings, nil
}

func writeAudit(w io.Writer, result auditResult, findings int) {
	fmt.Fprintf(w, "Found %d security vulnerability advisories affecting %d packages:\n", findings, len(result.Advisories))
	packages := make([]string, 0, len(result.Advisories))
	for name := range result.Advisories {
		packages = append(packages, name)
	}
	sort.Strings(packages)
	for _, name := range packages {
		for _, advisory := range result.Advisories[name] {
			fmt.Fprintln(w)
			writeDetailField(w, "package", name)
			writeDetailField(w, "version", advisory.InstalledVersion)
			writeDetailField(w, "severity", valueOrDash(advisory.Severity))
			writeDetailField(w, "advisory", advisory.AdvisoryID)
			writeDetailField(w, "cve", valueOrDash(advisory.CVE))
			writeDetailField(w, "title", advisory.Title)
			writeDetailField(w, "url", valueOrDash(advisory.Link))
			writeDetailField(w, "affected", advisory.AffectedVersions)
		}
	}
}
