//go:build windows

package ui

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	getConsoleMode = kernel32.NewProc("GetConsoleMode")
	setConsoleMode = kernel32.NewProc("SetConsoleMode")
)

// enableVT turns on ANSI escape handling in a Windows console. It reports
// false if the console can't do it, and Phantom then prints plain text.
func enableVT(f *os.File) bool {
	const virtualTerminalProcessing = 0x0004
	var mode uint32
	h := f.Fd()
	if r, _, _ := getConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode))); r == 0 {
		return false
	}
	if mode&virtualTerminalProcessing != 0 {
		return true
	}
	r, _, _ := setConsoleMode.Call(h, uintptr(mode|virtualTerminalProcessing))
	return r != 0
}

func termCols() int { return 0 }
