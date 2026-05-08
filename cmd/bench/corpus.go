package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Fixture is a single benchmark project: a directory containing composer.json.
type Fixture struct {
	// Name is the directory's base name (e.g. "tiny-psrlog").
	Name string
	// Path is the absolute path to the fixture directory under testdata/corpus.
	Path string
}

// LoadCorpus walks root and returns one Fixture per immediate subdirectory
// that contains a composer.json. Hidden directories (names starting with '.')
// are skipped. Results are sorted by Name for determinism.
func LoadCorpus(root string) ([]Fixture, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("bench: read corpus dir %q: %w", root, err)
	}
	var fixtures []Fixture
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "composer.json")); err != nil {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("bench: abs path for %q: %w", dir, err)
		}
		fixtures = append(fixtures, Fixture{Name: e.Name(), Path: abs})
	}
	sort.Slice(fixtures, func(i, j int) bool {
		return fixtures[i].Name < fixtures[j].Name
	})
	return fixtures, nil
}
