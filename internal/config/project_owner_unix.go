//go:build unix

package config

import (
	"os"
	"syscall"
)

// ownedByUser reports whether the file belongs to the user running airlock.
func ownedByUser(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
