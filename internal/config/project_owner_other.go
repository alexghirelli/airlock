//go:build !unix

package config

import "os"

// ownedByUser cannot be checked here, so no symlink is followed in the projects
// folder: a symlink there is ignored with a warning.
func ownedByUser(fi os.FileInfo) bool { return false }
