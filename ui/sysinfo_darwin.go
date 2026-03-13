//go:build darwin

package ui

import (
	"syscall"
	"unsafe"
)

// totalSystemRAM returns total physical memory in bytes on macOS.
func totalSystemRAM() uint64 {
	val, err := syscall.Sysctl("hw.memsize")
	if err != nil || len(val) < 8 {
		return 0
	}
	return *(*uint64)(unsafe.Pointer(&[]byte(val)[0]))
}
