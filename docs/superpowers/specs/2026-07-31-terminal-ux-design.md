# Adaptive terminal UX design

## Goal

Make install and update understandable while they run, readable in CI, and
actionable when they fail. Human output belongs on stderr; stdout and cache
command formats remain script-safe.

## Modes

- TTY: colored persistent checklist with one animated foreground row.
- Non-TTY: one stable line per completed phase, with no terminal controls.
- Quiet: errors only.
- Verbose: normal mode followed by the existing timing table.

The ordered phases are Prepare, Resolve, Download, Install, Autoload, and
Finalize. Download state may update concurrently during Resolve; it is buffered
until the resolver row completes.

## Rendering

The implementation uses `x/term` plus a small internal ANSI layer. It adds no
TUI dependency. Bars are responsive, warnings clear/redraw the active row, and
color follows `--color=auto|always|never`, `NO_COLOR`, and `TERM=dumb`.

## Errors

The reporter owns presentation so an error is printed once. Known failures get
a concise title and targeted hint; resolver derivations remain intact.
Cancellation uses exit status 130.

## Non-goals

- Full-screen terminal UI or user input.
- Changing dependency resolution or install semantics.
- Restyling cache command stdout.
