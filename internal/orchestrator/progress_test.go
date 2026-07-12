package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/torstendittmann/gomposer/internal/registry"
)

type recordedProgress struct {
	mu                       sync.Mutex
	events                   []string
	fetched, extracted, done int
}

func (r *recordedProgress) record(s string) {
	r.mu.Lock()
	r.events = append(r.events, s)
	r.mu.Unlock()
}

func (r *recordedProgress) BeginFetch(n int)    { r.record("BeginFetch") }
func (r *recordedProgress) IncFetch(n string)   { r.mu.Lock(); r.fetched++; r.mu.Unlock() }
func (r *recordedProgress) EndFetch()           { r.record("EndFetch") }
func (r *recordedProgress) BeginExtract(n int)  { r.record("BeginExtract") }
func (r *recordedProgress) IncExtract(n string) { r.mu.Lock(); r.extracted++; r.mu.Unlock() }
func (r *recordedProgress) EndExtract()         { r.record("EndExtract") }
func (r *recordedProgress) BeginResolve(n int)  { r.record("BeginResolve") }
func (r *recordedProgress) IncResolve(n string) {}
func (r *recordedProgress) EndResolve()         { r.record("EndResolve") }
func (r *recordedProgress) Done(n int)          { r.mu.Lock(); r.done = n; r.mu.Unlock() }

func TestProgressInvokedFromPipeline(t *testing.T) {
	// Isolate the on-disk resolution cache so this test's resolve is always
	// a fresh one (and therefore always fires BeginResolve/EndResolve),
	// regardless of what a prior test run may have cached under the real
	// XDG cache dir.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"vendor/root","require":{"a/one":"1.0.0","a/two":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"a/one": {Name: "a/one", Versions: []registry.PackageVersion{{
			Name: "a/one", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Dist: registry.Dist{Type: "zip", URL: "u1", Sha: "s1"},
		}}},
		"a/two": {Name: "a/two", Versions: []registry.PackageVersion{{
			Name: "a/two", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Dist: registry.Dist{Type: "zip", URL: "u2", Sha: "s2"},
		}}},
	}}

	rp := &recordedProgress{}
	opts := Options{
		ProjectDir:   dir,
		Source:       src,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		Workers:      2,
		NoScripts:    true,
		Progress:     rp,
	}

	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if rp.fetched != 2 {
		t.Errorf("fetched count = %d, want 2", rp.fetched)
	}
	if rp.extracted != 2 {
		t.Errorf("extracted count = %d, want 2", rp.extracted)
	}
	if rp.done != 2 {
		t.Errorf("done = %d, want 2", rp.done)
	}
	want := []string{"BeginResolve", "EndResolve", "BeginFetch", "EndFetch", "BeginExtract", "EndExtract"}
	if len(rp.events) != len(want) {
		t.Fatalf("events = %v, want %v", rp.events, want)
	}
	for i, w := range want {
		if rp.events[i] != w {
			t.Errorf("events[%d] = %q, want %q", i, rp.events[i], w)
		}
	}
}
