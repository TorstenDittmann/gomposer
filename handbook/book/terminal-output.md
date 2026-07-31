# Terminal Output

`gomposer install` and `gomposer update` adapt their output to the destination.

## Interactive terminals

A terminal receives a live checklist. Completed phases remain visible while
the active phase shows a spinner, package count, progress bar, and current
package. Speculative downloads are collected in the background without
replacing the resolver row.

```text
gomposer install  /work/acme/app

✓ Prepare     ready                                      4ms
✓ Resolve     47 packages · cached                      18ms
✓ Download    12 downloaded · 35 cached · 18.2 MB      820ms
⠹ Install     31/47 [█████████░░░░░] symfony/console
```

Warnings clear the active line, print once, and then redraw progress. The
layout drops the bar and truncates long package names on narrow terminals.

## CI and redirected output

Non-terminal stderr contains no animation or ANSI control sequences. Each
completed phase produces one stable line:

```text
gomposer: prepare: ready (4ms)
gomposer: resolve: 47 packages · cached (18ms)
gomposer: download: 12 downloaded · 35 cached · 18.2 MB (820ms)
gomposer: install: 47 packages (310ms)
gomposer: autoload: generated (8ms)
gomposer: finalize: lockfile written (12ms)
gomposer: installed 47 packages in 1.19s
```

## Color

`--color=auto` is the default. It enables color only for a capable TTY and
honors `NO_COLOR` and `TERM=dumb`. Use `--color=always` or `--color=never` to
override automatic color selection. Animation is never emitted to redirected
output.

## Quiet and verbose

`--quiet` suppresses progress, successful summaries, and warnings, but errors
remain visible. `--verbose` keeps the normal output and appends the detailed
timing table.

## Failures

Failures mark the active phase and present the useful cause without repeating
internal prefixes. Known recovery paths—missing PHP, invalid manifests,
dependency conflicts, authentication, checksum failures, and scripts—include
a targeted hint. Cancellation exits with status 130.
