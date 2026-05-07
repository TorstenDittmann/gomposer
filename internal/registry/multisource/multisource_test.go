package multisource

import (
	"context"
	"errors"
	"testing"

	"github.com/torstendittmann/composer-go/internal/registry"
)

type stubSource struct {
	name string
	md   *registry.PackageMetadata
	err  error
}

func (s stubSource) Lookup(_ context.Context, name string) (*registry.PackageMetadata, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.md == nil || s.md.Name != name {
		return nil, registry.ErrPackageNotFound
	}
	return s.md, nil
}

func TestAggregatorReturnsFirstHit(t *testing.T) {
	a := stubSource{md: &registry.PackageMetadata{Name: "acme/x", Versions: []registry.PackageVersion{{Name: "acme/x", Version: "1.0.0"}}}}
	b := stubSource{md: &registry.PackageMetadata{Name: "acme/x", Versions: []registry.PackageVersion{{Name: "acme/x", Version: "2.0.0"}}}}
	agg := New(a, b)
	got, err := agg.Lookup(context.Background(), "acme/x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Versions[0].Version != "1.0.0" {
		t.Errorf("first hit should win; got %q", got.Versions[0].Version)
	}
}

func TestAggregatorFallsThroughNotFound(t *testing.T) {
	a := stubSource{}
	b := stubSource{md: &registry.PackageMetadata{Name: "acme/y"}}
	agg := New(a, b)
	got, err := agg.Lookup(context.Background(), "acme/y")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "acme/y" {
		t.Errorf("expected fallthrough to b; got %+v", got)
	}
}

func TestAggregatorAllMissReturnsNotFound(t *testing.T) {
	agg := New(stubSource{}, stubSource{})
	_, err := agg.Lookup(context.Background(), "acme/none")
	if !errors.Is(err, registry.ErrPackageNotFound) {
		t.Fatalf("err = %v, want ErrPackageNotFound", err)
	}
}

func TestAggregatorPropagatesHardError(t *testing.T) {
	hard := errors.New("network down")
	a := stubSource{err: hard}
	b := stubSource{md: &registry.PackageMetadata{Name: "acme/z"}}
	agg := New(a, b)
	_, err := agg.Lookup(context.Background(), "acme/z")
	if !errors.Is(err, hard) {
		t.Fatalf("expected hard error to propagate, got %v", err)
	}
}

func TestAggregatorEmpty(t *testing.T) {
	agg := New()
	_, err := agg.Lookup(context.Background(), "anything")
	if !errors.Is(err, registry.ErrPackageNotFound) {
		t.Fatalf("err = %v, want ErrPackageNotFound", err)
	}
}

type stubByName map[string]*registry.PackageMetadata

func (s stubByName) Lookup(_ context.Context, name string) (*registry.PackageMetadata, error) {
	if v, ok := s[name]; ok {
		return v, nil
	}
	return nil, registry.ErrPackageNotFound
}

func TestNewWithLookups(t *testing.T) {
	a := stubByName{"acme/x": {Name: "acme/x"}}
	b := stubByName{"acme/y": {Name: "acme/y"}}
	agg := NewWithLookups([]registry.SourceLookup{a, b})
	if _, err := agg.Lookup(context.Background(), "acme/y"); err != nil {
		t.Fatal(err)
	}
}
