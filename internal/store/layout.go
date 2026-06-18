package store

import "path/filepath"

// ProjectStoreDir returns the conventional store path for a project rooted
// at projectDir. Co-locating the store under .gomposer/ keeps it on the
// same filesystem as vendor/, which is a precondition for reflink and
// hardlink to succeed.
//
// Users who want a shared cache across projects can pass an explicit path
// to New() instead.
func ProjectStoreDir(projectDir string) string {
	return filepath.Join(projectDir, ".gomposer", "store")
}
