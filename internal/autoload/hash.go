package autoload

import (
	"crypto/sha256"
	"encoding/hex"
)

// InitHash returns the 32-hex-char identifier used to make the
// ComposerAutoloaderInit<HASH> class name unique per project on a given
// machine. The input must be the project's absolute path; the orchestrator
// is responsible for resolving it (e.g. via filepath.Abs) before calling.
//
// Composer itself uses md5 of a similar input; we use sha256 truncated to
// 32 hex chars (128 bits). Truncation is fine — collision resistance is
// not the threat model; uniqueness across a few projects on one machine is.
func InitHash(absProjectDir string) string {
	sum := sha256.Sum256([]byte(absProjectDir))
	return hex.EncodeToString(sum[:])[:32]
}

// InitClassName returns the full PHP class name for the autoloader init
// class for the given project.
func InitClassName(absProjectDir string) string {
	return "ComposerAutoloaderInit" + InitHash(absProjectDir)
}
