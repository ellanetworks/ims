//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package transport

func openFileLimit() int { return 0 }
