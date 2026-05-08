// Command bench measures composer-go install vs composer install over a fixed
// corpus and prints a markdown report. It is a manual tool: nothing in CI
// invokes the real binaries.
//
// Usage:
//
//	go run ./cmd/bench \
//	  --corpus cmd/bench/testdata/corpus \
//	  --composer-go ./composer-go \
//	  --composer /usr/local/bin/composer \
//	  --runs 5 \
//	  --scenarios cold,warm,lock-unchanged
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// flagPlan is the parsed CLI form. We separate it from runner.Plan so flag
// parsing has no dependency on the corpus loader.
type flagPlan struct {
	Corpus     string
	ComposerGo string
	Composer   string
	Runs       int
	Scenarios  []Scenario
}

func parseFlags(argv []string) (*flagPlan, error) {
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	corpus := fs.String("corpus", "", "directory of fixtures (each subdirectory must contain composer.json)")
	composerGo := fs.String("composer-go", "composer-go", "path to the composer-go binary")
	composer := fs.String("composer", "composer", "path to the composer binary")
	runs := fs.Int("runs", 3, "number of timed runs per (fixture, scenario, tool); median is reported")
	scenariosCSV := fs.String("scenarios", "cold,warm,lock-unchanged",
		"comma-separated subset of cold,warm,lock-unchanged")

	if err := fs.Parse(argv); err != nil {
		return nil, err
	}
	if *corpus == "" {
		return nil, fmt.Errorf("--corpus is required")
	}
	if *runs < 1 {
		return nil, fmt.Errorf("--runs must be >=1")
	}

	var scs []Scenario
	for _, raw := range strings.Split(*scenariosCSV, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		sc, err := ParseScenario(raw)
		if err != nil {
			return nil, err
		}
		scs = append(scs, sc)
	}
	if len(scs) == 0 {
		return nil, fmt.Errorf("--scenarios produced empty list")
	}

	return &flagPlan{
		Corpus:     *corpus,
		ComposerGo: *composerGo,
		Composer:   *composer,
		Runs:       *runs,
		Scenarios:  scs,
	}, nil
}

func main() {
	if err := mainImpl(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(1)
	}
}

func mainImpl(argv []string) error {
	fp, err := parseFlags(argv)
	if err != nil {
		return err
	}
	fixtures, err := LoadCorpus(fp.Corpus)
	if err != nil {
		return err
	}
	if len(fixtures) == 0 {
		return fmt.Errorf("no fixtures found under %q", fp.Corpus)
	}

	plan := Plan{
		Fixtures:       fixtures,
		Scenarios:      fp.Scenarios,
		Runs:           fp.Runs,
		ComposerGoPath: fp.ComposerGo,
		ComposerPath:   fp.Composer,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	results, err := Run(ctx, plan, execCmdRunner{})
	if err != nil {
		return err
	}
	fmt.Print(RenderMarkdown(results))
	return nil
}
