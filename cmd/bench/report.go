package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// RenderMarkdown returns a markdown table with one row per (fixture, scenario).
// gomposer and composer medians are paired side by side; speed-up is
// composer / gomposer rounded to one decimal.
//
// Empty results produce a header-only table — useful for CI smoke tests.
func RenderMarkdown(results []Result) string {
	var b strings.Builder
	b.WriteString("| Fixture | Scenario | gomposer | composer | speed-up |\n")
	b.WriteString("|---------|----------|-------------|----------|----------|\n")

	type key struct {
		fixture  string
		scenario Scenario
	}
	type pair struct {
		cgo *Result
		co  *Result
	}
	pairs := map[key]*pair{}
	for i := range results {
		r := &results[i]
		k := key{fixture: r.Fixture, scenario: r.Scenario}
		p, ok := pairs[k]
		if !ok {
			p = &pair{}
			pairs[k] = p
		}
		switch r.Tool {
		case ToolGomposer:
			p.cgo = r
		case ToolComposer:
			p.co = r
		}
	}

	keys := make([]key, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	scenarioRank := map[Scenario]int{
		ScenarioCold:          0,
		ScenarioWarm:          1,
		ScenarioLockUnchanged: 2,
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].fixture != keys[j].fixture {
			return keys[i].fixture < keys[j].fixture
		}
		return scenarioRank[keys[i].scenario] < scenarioRank[keys[j].scenario]
	})

	for _, k := range keys {
		p := pairs[k]
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
			k.fixture, k.scenario,
			formatDuration(p.cgo), formatDuration(p.co),
			formatSpeedup(p.cgo, p.co))
	}
	return b.String()
}

func formatDuration(r *Result) string {
	if r == nil {
		return "n/a"
	}
	return humanDuration(r.Median)
}

// humanDuration prints a duration with a small number of significant digits.
// e.g. 1.234s, 234ms, 12.3ms, 4.5us.
func humanDuration(d time.Duration) string {
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.2gs", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d >= time.Microsecond:
		return fmt.Sprintf("%dus", d.Microseconds())
	default:
		return d.String()
	}
}

func formatSpeedup(cgo, co *Result) string {
	if cgo == nil || co == nil || cgo.Median <= 0 {
		return "n/a"
	}
	x := float64(co.Median) / float64(cgo.Median)
	return fmt.Sprintf("%.1fx", x)
}
