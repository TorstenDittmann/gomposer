# The `workspace:` Protocol

Cross-workspace dependencies are declared with a `workspace:` prefix in any workspace's `require` or `require-dev` map. The root manifest may use it too.

## Two forms

| Syntax | Semantics |
|---|---|
| `workspace:*` | Match the local workspace at any version. Never checks the target's `version` field. |
| `workspace:<constraint>` | Match the local workspace only if its declared `version` satisfies the constraint. Standard Composer constraint syntax on the tail. |

`<constraint>` accepts anything the constraint parser accepts elsewhere:

- Caret and tilde: `workspace:^1.0`, `workspace:~1.2`.
- Ranges: `workspace:>=1.0 <2.0`.
- Exact: `workspace:1.2.3`.
- Wildcards: `workspace:1.*`.

## Example

```json
{
    "name": "acme/api",
    "require": {
        "acme/shared": "workspace:^1.0",
        "psr/log": "^3.0"
    }
}
```

`acme/shared` here must be one of the workspaces the root discovered. `psr/log` is a normal external package — it flows through the resolver to Packagist and gets fetched.

## Validation

Every `workspace:` require is validated **before** the resolver runs. Errors surface at aggregate time with clear, actionable messages:

| Situation | Result |
|---|---|
| Target name not in the workspace set | Hard error: `workspaces: workspace:<owner> require "<name>" not found in workspace set`. |
| `workspace:*` | Always accepted (no version check). |
| `workspace:<constraint>` but target workspace has no `version` field | Hard error: `workspaces: <owner> requires <name> (workspace:<constraint>) but workspace has no version field`. |
| `workspace:<constraint>` and target version doesn't satisfy | Hard error: `workspaces: <owner> requires <name> (workspace:<constraint>) but workspace has version "X.Y.Z"`. |

Validation runs early so you get a clear diagnostic before any HTTP requests fire.

## Two workspaces requiring the same external package

If two workspaces both require the same external package with different constraints, gomposer **intersects** them using Composer's AND syntax (whitespace-joined). Compatible constraints solve together; incompatible ones surface as a normal PubGrub derivation naming both workspace owners.

Example:

```json
// packages/a/composer.json
{ "require": { "symfony/console": "^6.0" } }

// packages/b/composer.json
{ "require": { "symfony/console": ">=6.2" } }
```

The aggregate manifest fed to the resolver contains `"symfony/console": "^6.0 >=6.2"`, which reduces to `>=6.2 <7.0` at solve time. If the constraints had been `^6.0` and `^7.0` (incompatible), the derivation would name both workspaces as the conflicting owners.

## Not in Scope 1

- `workspace:./relative/path` — pin a workspace by directory rather than name. Both pnpm and bun support this; gomposer's POC does not. If you need it, please [open an issue](https://github.com/TorstenDittmann/gomposer/issues).
