//go:build linux || darwin

package ui

import (
	"os"
	"syscall"
	"unsafe"
)

func termCols() int {
	var ws struct{ Row, Col, X, Y uint16 }
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if e != 0 {
		return 0
	}
	return int(ws.Col)
}

func enableVT(*os.File) bool { return true }
