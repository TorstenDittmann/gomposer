# Composer compatibility

gomposer reads `composer.json` and produces the same `vendor/` layout Composer does. The intent is that you can point gomposer at any project that installs cleanly with Composer 2 and get a byte-equivalent (or as-close-as-practical) install.

## Input: `composer.json`

Read and honored:

- `name`, `version`, `type`.
- `require`, `require-dev`.
- `repositories` — supported types: `composer`, `vcs`, `git`. Legacy map form is a hard error (CG203).
- `minimum-stability`.
- `prefer-stable`.
- `stability-flags` (implicitly via per-require `@dev` / `@RC` / `@beta` / `@alpha` / `@stable` suffixes).
- `scripts` — every event runs (see below for opt-out).
- `platform` requirements: `php`, `php-64bit`, `ext-*`, `lib-*` (lib-* is checked but its version cannot be evaluated; treated as warning).
- `autoload` and `autoload-dev` — `psr-4`, `psr-0`, `classmap`, `files`, `exclude-from-classmap`.
- `extra.gomposer.*` — gomposer's own namespace for project-specific config (currently: suppression of plugin warnings).
- `workspaces` — see [Workspaces](./workspaces.md).

## Output: `vendor/` and `gomposer.lock`

- `vendor/autoload.php`, `vendor/composer/autoload_*.php`, `vendor/composer/installed.php` — the same shapes Composer generates. `installed.php` currently is a Stage-1 stub; a fuller implementation is on the roadmap.
- `vendor/composer/ClassLoader.php` — vendored verbatim from Composer under its MIT license.
- Autoload coverage: PSR-4, PSR-0, classmap (token-stream PHP scanner, not regex), files, `exclude-from-classmap`. Each is byte-compared against Composer's output in the in-tree test suite.
- `gomposer.lock` — gomposer's own lockfile. See below.

## Lockfile

gomposer keeps its own `gomposer.lock` with a different schema than `composer.lock`. If both exist they are independent — you can run Composer alongside gomposer safely.

Round-trip with `composer.lock` (so `composer install` can consume gomposer's output) is deliberately out of scope right now. It was prototyped and abandoned; see the "How this project makes decisions" section under [Contributing](./contributing.md).

## What it does not do

- **Composer plugins are never executed.** `--allow-plugins` is accepted for CLI compatibility and is a no-op. If a project depends on plugin-side effects (installer scripts, custom `PackageEvent` hooks) gomposer will not run them.
- **`composer.lock` is not read.** gomposer reads only `gomposer.lock`.
- **No interactive prompts.** gomposer is strictly non-interactive.
- **Some Stage-4 items are pending:** signed releases (cosign), Homebrew tap, migration guide, small docs site.

## Per-require stability flags

A common Composer pattern like `"utopia-php/http": "^2.0@RC"` is supported: gomposer parses the `@<stability>` suffix and lowers the stability floor for that require only. Global `minimum-stability` is unchanged.

Accepted suffixes: `@dev` (rank 20), `@alpha` (15), `@beta` (10), `@RC` (5), `@stable` (0). Case-insensitive on `RC`.

## Non-standard extensions

- **`workspaces`** — a top-level array in the root `composer.json` that declares a monorepo. See [Workspaces](./workspaces.md).
- **`workspace:*` / `workspace:<constraint>` protocol** — accepted anywhere Composer accepts a version constraint. Composer will reject these strings if you point it at the same `composer.json` — gomposer's workspaces feature is genuinely new syntax.
- **`extra.gomposer.suppress-plugin-warnings`** — a boolean that silences plugin-detection warnings for approved packages.
