// Package multisource aggregates multiple registry.SourceLookup
// implementations behind a single SourceLookup. Lookups are tried in
// declaration order; the first hit wins. ErrPackageNotFound from one source
// causes fallthrough; any other error stops the search and is returned.
package multisource

import (
	"context"
	"errors"
	"fmt"

	"github.com/torstendittmann/composer-go/internal/registry"
)

// Aggregator implements registry.SourceLookup over an ordered list of
// children. It is safe for concurrent use as long as every child is.
type Aggregator struct {
	children []registry.SourceLookup
}

// New returns an Aggregator over the given children.
func New(children ...registry.SourceLookup) *Aggregator {
	return &Aggregator{children: children}
}

// NewWithLookups is identical to New but takes a slice. Useful when the
// caller already has a typed slice (e.g. []*vcs.Client converted via the
// SourceLookup interface).
func NewWithLookups(children []registry.SourceLookup) *Aggregator {
	return &Aggregator{children: children}
}

// Lookup implements registry.SourceLookup.
func (a *Aggregator) Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error) {
	if len(a.children) == 0 {
		return nil, fmt.Errorf("%s: %w", name, registry.ErrPackageNotFound)
	}
	for _, c := range a.children {
		md, err := c.Lookup(ctx, name)
		if err == nil {
			return md, nil
		}
		if errors.Is(err, registry.ErrPackageNotFound) {
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("%s: %w", name, registry.ErrPackageNotFound)
}
