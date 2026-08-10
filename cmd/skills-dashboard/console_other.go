//go:build !windows

package main

// ownsConsole is a Windows concern. Everywhere else the binary is started from
// a shell that outlives it, so an error printed on exit stays on screen.
func ownsConsole() bool { return false }
