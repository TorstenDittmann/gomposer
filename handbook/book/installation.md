# Installation

## Prebuilt binary (macOS + Linux, amd64 + arm64)

The recommended path. One command:

```sh
curl -fsSL https://raw.githubusercontent.com/TorstenDittmann/gomposer/main/install.sh | sh
```

The script:

1. Detects your OS (`darwin` / `linux`) and architecture (`amd64` / `arm64`).
2. Resolves the latest release tag from the GitHub API (or `$GOMPOSER_VERSION` if set).
3. Downloads the matching `.tar.gz` archive plus the `checksums.txt` file.
4. Verifies the archive's SHA-256 against the checksum.
5. Extracts the binary and moves it into `/usr/local/bin/gomposer`, falling back to `$HOME/.local/bin` if the default isn't writable without sudo.

### Environment overrides

| Variable | Effect |
|---|---|
| `GOMPOSER_INSTALL_DIR` | Target directory. Default: `/usr/local/bin`, falling back to `$HOME/.local/bin`. |
| `GOMPOSER_VERSION` | Release tag to install (e.g. `v0.0.2`). Default: latest published release. |

## From source

Requires Go 1.25 or newer.

```sh
go install github.com/torstendittmann/gomposer/cmd/gomposer@latest
```

## Manual download

Every release on the [Releases page](https://github.com/TorstenDittmann/gomposer/releases) ships:

- One `.tar.gz` per platform: `gomposer_<version>_<os>_<arch>.tar.gz`.
- A `checksums.txt` covering every archive.

Verify with `sha256sum -c`, extract, and drop the `gomposer` binary anywhere on your `PATH`.

## What's not planned

- Homebrew tap.
- Windows build.
- APT / YUM / pacman packaging.

Open an issue if any of these become important to you.

## Verify

After install:

```sh
gomposer --version
gomposer --help
```

The version prints `<tag> (commit <short>, built <date>)` for release builds, and `dev` for local `go install` builds.
