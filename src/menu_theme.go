//go:build windows

package main

import (
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

// Native popup menus follow the Windows light/dark setting through two unnamed uxtheme
// exports (ordinal 135 SetPreferredAppMode, 136 FlushMenuThemes; Windows 10 1903 and later).
var (
	menuThemeOnce   sync.Once
	pSetAppMode     uintptr
	pFlushMenuTheme uintptr

	pSetMenuDefaultItem = user32.NewProc("SetMenuDefaultItem")
)

func applyMenuTheme(light bool) {
	menuThemeOnce.Do(func() {
		h, err := windows.LoadLibraryEx("uxtheme.dll", 0, windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
		if err != nil {
			return
		}
		pSetAppMode, _ = windows.GetProcAddressByOrdinal(h, 135)
		pFlushMenuTheme, _ = windows.GetProcAddressByOrdinal(h, 136)
	})
	if pSetAppMode == 0 {
		return
	}
	mode := uintptr(2) // ForceDark
	if light {
		mode = 3 // ForceLight
	}
	syscall.SyscallN(pSetAppMode, mode)
	if pFlushMenuTheme != 0 {
		syscall.SyscallN(pFlushMenuTheme)
	}
}
