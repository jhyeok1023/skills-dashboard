//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// ownsConsole reports whether this process is the only one attached to its
// console, which is what happens when the binary is double-clicked in Explorer:
// Windows creates a console for it alone and destroys the window the moment the
// process exits. An error printed on the way out is unreadable in that case
// unless something holds the window open.
//
// A process started from an existing terminal shares that terminal with the
// shell, so the count is above one and nothing needs to be held.
//
// GetConsoleProcessList is reached through kernel32 directly rather than
// golang.org/x/sys/windows: it is one call, and the module has no dependency on
// x/sys today.
func ownsConsole() bool {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList")
	if err := proc.Find(); err != nil {
		return false
	}
	// The call fills a buffer with process ids and returns how many there are.
	// Two slots is enough to tell "exactly one" from "more than one".
	var pids [2]uint32
	n, _, _ := proc.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}
