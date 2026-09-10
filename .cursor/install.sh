#!/usr/bin/env bash
# Cloud Agent bootstrap for gomposer.
#
# Installs the Go and PHP toolchains with mise (versions pinned in mise.toml) and
# warms the Go build cache. Safe to run repeatedly.
set -euo pipefail

MISE_BIN="$HOME/.local/bin/mise"

# 1. Install mise (the tool-version manager) if it isn't already present.
if [ ! -x "$MISE_BIN" ]; then
    curl -fsSL https://mise.run | sh
fi
export PATH="$HOME/.local/bin:$PATH"

# 2. System libraries needed to compile PHP from source. mise builds PHP with the
#    vfox backend, which enables the gd/intl extensions (hence libgd, libicu and the
#    matching libstdc++ the intl C++ objects link against).
sudo apt-get update
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y \
    build-essential autoconf bison re2c pkg-config \
    libxml2-dev libssl-dev libcurl4-openssl-dev libonig-dev libsqlite3-dev \
    libzip-dev libicu-dev zlib1g-dev \
    libpng-dev libjpeg-dev libfreetype6-dev libwebp-dev libgd-dev \
    libstdc++-14-dev

# 3. Install the pinned toolchains. PHP is compiled from source; a few extensions
#    that probe for fork()/shared-memory at configure time do not build under the
#    sandboxed build VM and gomposer needs none of them, so they are turned off.
#    Forcing the fork cache variables keeps the CLI SAPI (which references HAVE_FORK)
#    buildable regardless of the sandbox's configure-time run-test restrictions.
export ac_cv_func_fork=yes ac_cv_func_vfork=yes ac_cv_func_fork_works=yes ac_cv_func_vfork_works=yes
export PHP_EXTRA_CONFIGURE_OPTIONS="--disable-opcache --disable-pcntl --without-readline --without-pdo-pgsql"
"$MISE_BIN" trust --quiet
"$MISE_BIN" install

# 4. Also register the toolchains as global defaults so `go`/`php` resolve from any
#    directory (gomposer is a CLI you run inside arbitrary PHP projects, not just this
#    repo). The in-repo mise.toml still pins the versions used when working in /workspace.
"$MISE_BIN" use -g "go@1.25" "php@8.3"

# 5. Activate mise for the agent's interactive shells (idempotent).
if ! grep -qF "mise activate bash" "$HOME/.bashrc" 2>/dev/null; then
    printf '\n# mise (Go + PHP toolchains)\nexport PATH="$HOME/.local/bin:$PATH"\neval "$(%s activate bash)"\n' "$MISE_BIN" >> "$HOME/.bashrc"
fi

# 6. Warm the Go module and build caches using the mise-managed Go.
"$MISE_BIN" exec -- go mod download
"$MISE_BIN" exec -- go build ./...
