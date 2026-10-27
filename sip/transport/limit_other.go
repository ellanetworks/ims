//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package transport

// openFileLimit is the process's limit of open files, 0 if unknown.
func openFileLimit() int { return 0 }
