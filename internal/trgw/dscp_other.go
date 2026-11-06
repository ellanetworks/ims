//go:build !linux

package trgw

import "syscall"

func markEF(string, string, syscall.RawConn) error {
	return nil
}
