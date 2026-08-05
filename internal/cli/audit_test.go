package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

func executeAudit(t *testing.T, endpoint string, args ...string) (string, error) {
	t.Helper()
	oldEndpoint, oldClient := auditEndpoint, auditHTTPClient
	auditEndpoint, auditHTTPClient = endpoint, http.DefaultClient
	t.Cleanup(func() { auditEndpoint, auditHTTPClient = oldEndpoint, oldClient })
	flagNoDev, flagQuiet = false, false
	var out bytes.Buffer
	cmd := newRootCmd("dev")
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"audit"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestAuditReportsMatchingAdvisories(t *testing.T) {
	dir := seedShowProject(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("User-Agent") == "" {
			t.Errorf("request method=%s user-agent=%q", r.Method, r.Header.Get("User-Agent"))
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		got := append([]string(nil), r.Form["packages[]"]...)
		sort.Strings(got)
		if strings.Join(got, ",") != "acme/root,acme/test,psr/log" {
			t.Errorf("packages = %v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"advisories":{"acme/root":[{"advisoryId":"PKSA-test","packageName":"acme/root","title":"Test vulnerability","affectedVersions":"<2.2.0","severity":"high","cve":"CVE-2026-0001","link":"https://example.test/advisory"}],"acme/test":[],"psr/log":[]}}`))
	}))
	defer server.Close()

	out, err := executeAudit(t, server.URL, "--project", dir)
	if err == nil || !isHandled(err) {
		t.Fatalf("expected handled finding error, got %v", err)
	}
	for _, want := range []string{"Found 1", "acme/root", "PKSA-test", "CVE-2026-0001", "high"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestAuditIgnoresUnaffectedAndSupportsJSON(t *testing.T) {
	dir := seedShowProject(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"advisories":{"acme/root":[{"advisoryId":"PKSA-safe","packageName":"acme/root","title":"Old issue","affectedVersions":"<2.0.0"}],"acme/test":[],"psr/log":[]}}`))
	}))
	defer server.Close()
	out, err := executeAudit(t, server.URL, "--project", dir, "--format=json")
	if err != nil {
		t.Fatal(err)
	}
	var result auditResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(result.Advisories) != 0 || len(result.Checked) != 3 || len(result.Unknown) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestAuditNoDevExcludesDevelopmentPackages(t *testing.T) {
	dir := seedShowProject(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if strings.Contains(strings.Join(r.Form["packages[]"], ","), "acme/test") {
			t.Errorf("dev package included: %v", r.Form)
		}
		_, _ = w.Write([]byte(`{"advisories":{"acme/root":[],"psr/log":[]}}`))
	}))
	defer server.Close()
	if _, err := executeAudit(t, server.URL, "--project", dir, "--no-dev"); err != nil {
		t.Fatal(err)
	}
}

func TestAuditRejectsMalformedResponse(t *testing.T) {
	dir := seedShowProject(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"unexpected":true}`))
	}))
	defer server.Close()
	if _, err := executeAudit(t, server.URL, "--project", dir); err == nil || !strings.Contains(err.Error(), "missing advisories") {
		t.Fatalf("error = %v", err)
	}
}
