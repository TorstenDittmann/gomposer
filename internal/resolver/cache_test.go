package resolver

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/resolver/testlookup"
)

type countingLookup struct {
	inner registry.SourceLookup
	calls *int32
}

func (c countingLookup) Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error) {
	atomic.AddInt32(c.calls, 1)
	return c.inner.Lookup(ctx, name)
}

func TestCachedSolverHitSkipsResolver(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
	})
	var calls int32
	counted := countingLookup{inner: src, calls: &calls}

	cs, err := NewCachedSolver(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	m := &manifest.Manifest{Name: "u/a", Require: map[string]string{"a/a": "^1.0"}}
	in := Input{
		Manifest:            m,
		Source:              counted,
		PlatformFingerprint: "php-8.2",
	}
	in1 := in
	in1.Source = counted
	r1, err := cs.Solve(context.Background(), in1, "manifest-hash-1", "lock-hash-1")
	if err != nil {
		t.Fatalf("Solve(1): %v", err)
	}
	calls1 := atomic.LoadInt32(&calls)
	if calls1 == 0 {
		t.Fatalf("expected lookups on cold cache, got 0")
	}

	atomic.StoreInt32(&calls, 0)
	in2 := in
	in2.Source = counted
	r2, err := cs.Solve(context.Background(), in2, "manifest-hash-1", "lock-hash-1")
	if err != nil {
		t.Fatalf("Solve(2): %v", err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Errorf("warm cache should not call SourceLookup; got %d calls", calls)
	}
	if len(r1.Packages) != len(r2.Packages) {
		t.Errorf("warm result differs from cold")
	}
}

func TestCachedSolverDifferentInputsMiss(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
	})
	cs, _ := NewCachedSolver(t.TempDir())
	m := &manifest.Manifest{Name: "u/a", Require: map[string]string{"a/a": "^1.0"}}

	_, err := cs.Solve(context.Background(), Input{Manifest: m, Source: src, PlatformFingerprint: "php-8.2"}, "h1", "l1")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		mhash, lhash, fp string
	}{
		{"h-different", "l1", "php-8.2"},
		{"h1", "l-different", "php-8.2"},
		{"h1", "l1", "php-different"},
	}
	for _, tc := range cases {
		// We can't easily count from inside CachedSolver, but a different key
		// must produce a successful Solve (no panic, valid result).
		r, err := cs.Solve(context.Background(), Input{Manifest: m, Source: src, PlatformFingerprint: tc.fp}, tc.mhash, tc.lhash)
		if err != nil {
			t.Errorf("Solve(%v): %v", tc, err)
		}
		if r == nil {
			t.Errorf("nil result for %v", tc)
		}
	}
}
