# Reading composer.json

gomposer reads the same `composer.json` that Composer 2 does. This page lists exactly which fields are honored and what special handling applies.

## Package identity

- `name` — package name (`vendor/pkg`).
- `version` — declared version. Usually omitted for application-shaped projects; required on workspaces that participate in a `workspace:<constraint>` require (see [The workspace: Protocol](./workspaces-protocol.md)).
- `type` — used to detect Composer plugins so gomposer can emit a warning (they are never executed — see [Vendor Layout](./vendor-layout.md#plugins)).

## Dependencies

- `require` — production dependencies.
- `require-dev` — dev dependencies. Skipped under `--no-dev`.
- Constraints follow Composer syntax: `^1.0`, `~1.2`, `>=1.0 <2.0`, `1.2.3`, `dev-main`, `1.x-dev`. AND is expressed by whitespace between clauses; OR by `|` or `||`.

### Per-require stability flags

A common Composer pattern like `"utopia-php/http": "^2.0@RC"` is supported: gomposer parses the `@<stability>` suffix and lowers the stability floor **for that require only**. The global `minimum-stability` is unchanged.

Accepted suffixes and their ranks:

| Suffix | Rank |
|---|---|
| `@dev` | 20 |
| `@alpha` | 15 |
| `@beta` | 10 |
| `@RC` | 5 (case-insensitive on `RC`) |
| `@stable` | 0 |

## Repositories

`repositories` is honored in its array form. The map form is a hard error (CG203).

Supported types:

- `composer` — a Packagist-shaped registry. Default is `https://repo.packagist.org`.
- `vcs`, `git` — a git URL that gomposer clones on demand and treats as a versioned source.

Support for `type: "path"` (Composer's traditional monorepo mechanism) is deliberately not implemented; workspaces cover that role — see [Workspaces](./workspaces.md).

## Stability

- `minimum-stability` — project-wide stability floor. Default: `stable`.
- `prefer-stable` — when a package publishes both stable and pre-release versions satisfying a constraint, prefer stable. Default: `false`.

## Autoload

- `autoload` and `autoload-dev` — `psr-4`, `psr-0`, `classmap`, `files`, `exclude-from-classmap` are all honored.
- The classmap scanner uses PHP's token stream (not a regex), so it correctly handles class-in-string literals and other PHP-syntax edge cases that regex scanners miss.

## Scripts

`scripts` maps events (`post-install-cmd`, `pre-update-cmd`, etc.) to one or more script bodies. Composer's wire format accepts either a single string or an array of strings per event; gomposer normalizes both into an array internally.

Scripts run in the order declared. Skip everything with `--no-scripts`.

## Platform

Platform requirements in `require` / `require-dev` map to runtime checks:

- `php` and `php-64bit`, `php-ipv6`, `php-zts`, `php-debug` — checked against the runtime PHP version via a probe.
- `ext-*` — checked against loaded extensions.
- `lib-*` — presence is checked but versions cannot be evaluated; treated as a warning.

`--no-dev` enforces platform requirements strictly (mismatch → error). Otherwise mismatches are warnings that stream to stderr.

Names containing a `/` (e.g. `php-http/discovery`) are correctly treated as ordinary package requires, not platform requirements.

## Extra

`extra.gomposer.*` is gomposer's own namespace for project-specific configuration:

- `extra.gomposer.suppress-plugin-warnings` — an array of package names to silence in the plugin-detection warning stream.

Other `extra.*` keys are read as-is by anything that needs them (typically test fixtures); they don't influence the install.

## Workspaces

The top-level `workspaces` array declares a monorepo. Full details in [Workspaces](./workspaces.md).

## Silently ignored

Unknown top-level fields (from future Composer versions or third-party tooling) are ignored, matching Composer's own forward-compat behavior.
