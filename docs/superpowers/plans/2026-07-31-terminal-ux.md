# Adaptive Terminal UX Implementation Plan

1. Replace the single mutable progress row with ordered, concurrency-safe
   phase state and TTY/plain/quiet renderers.
2. Buffer speculative download updates behind Resolve and preserve deduped
   package counters.
3. Add `--color=auto|always|never`, responsive rendering, warning-safe redraws,
   and success summaries.
4. Route plugin and platform warnings through the reporter.
5. Present common failures once with targeted hints and map cancellation to
   exit status 130.
6. Add renderer, CLI, ordering, quiet, color, and diagnostic tests.
7. Document terminal and CI output in the README and handbook.

Acceptance: `go test ./...`, focused race tests, and `go vet ./...` pass; CI
output has no ANSI controls; quiet mode still shows failures; cache output is
unchanged.
