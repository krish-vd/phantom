//go:build !linux && !darwin && !windows

package ui

import "os"

func termCols() int { return 0 }

func enableVT(*os.File) bool { return true }
