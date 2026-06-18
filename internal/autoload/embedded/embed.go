// Package embedded vendors Composer's ClassLoader.php and its LICENSE so
// gomposer can drop them into vendor/composer/ at install time. The
// vendored files are MIT-licensed; see LICENSE for the full notice.
//
// Sourced from composer/composer @ commit 3a184c1744373e7d8602f0d9a985fb546d41f53d
// (main branch, 2026-05-07). Do NOT modify ClassLoader.php locally; if a
// fix is required, file it upstream and re-vendor.
package embedded

import _ "embed"

//go:embed ClassLoader.php
var ClassLoaderPHP []byte

//go:embed LICENSE
var LicenseText []byte
