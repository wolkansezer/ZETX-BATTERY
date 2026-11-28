//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// Keep the taskbar-mode bar above Shell_TrayWnd when the user clicks the taskbar or taskbar buttons.
// Clicking the taskbar raises Shell_TrayWnd to the top of the TOPMOST z-order band; without hooks,
// the bar would stay behind it until the 1-second timer fired. Out-of-context WinEvent hooks catch
// foreground changes and Explorer interactions immediately and re-assert TOPMOST within a frame.

var (
	pSetWinEventHook          = user32.NewProc("SetWinEventHook")
	pUnhookWinEvent           = user32.NewProc("UnhookWinEvent")
	pGetWindow                = user32.NewProc("GetWindow")
	pGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	pKillTimer                = user32.NewProc("KillTimer")
)

const (
	gwHwndPrev = 3

	wineventOutOfContext   = 0x0000
	wineventSkipOwnProcess = 0x0002

	eventSystemForeground   = 0x0003
	eventSystemCaptureStart = 0x0008
	eventSystemCaptureEnd   = 0x0009
	eventObjectShow         = 0x8002
	eventObjectHide         = 0x8003
	eventObjectFocus        = 0x8005
	eventObjectInvoked      = 0x8013

	timerRaise       = 3
	raiseBurstMs     = 15
	raiseBurstTicks  = 12 // ~180 ms follow-up window for XAML taskbar click animations
)

func (a *App) installZHooks() {
	if a.zHookCb == 0 {
		a.zHookCb = syscall.NewCallback(winEventProc)
	}
	flags := uintptr(wineventOutOfContext | wineventSkipOwnProcess)
	addHook := func(minEv, maxEv, pid uintptr) {
		if h, _, _ := pSetWinEventHook.Call(minEv, maxEv, 0, a.zHookCb, pid, 0, flags); h != 0 {
			a.zHooks = append(a.zHooks, h)
		}
	}
	// Global foreground switch (clicking taskbar, switching apps, closing Start/Quick Settings)
	addHook(eventSystemForeground, eventSystemForeground, 0)

	// Explorer-scoped hooks for clicks/focus/flyouts on the taskbar itself
	if tb, _, _ := pFindWindowW.Call(uintptr(unsafe.Pointer(u16("Shell_TrayWnd"))), 0); tb != 0 {
		var pid uint32
		pGetWindowThreadProcessId.Call(tb, uintptr(unsafe.Pointer(&pid)))
		if pid != 0 {
			addHook(eventSystemCaptureStart, eventSystemCaptureEnd, uintptr(pid))
			addHook(eventObjectShow, eventObjectHide, uintptr(pid))
			addHook(eventObjectFocus, eventObjectFocus, uintptr(pid))
			addHook(eventObjectInvoked, eventObjectInvoked, uintptr(pid))
		}
	}
}

func (a *App) removeZHooks() {
	for _, h := range a.zHooks {
		if h != 0 {
			pUnhookWinEvent.Call(h)
		}
	}
	a.zHooks = nil
}

func (a *App) reinstallZHooks() {
	a.removeZHooks()
	a.installZHooks()
}

func winEventProc(hook, event, hwnd, idObj, idChild, thread, timeMs uintptr) uintptr {
	a := app
	if a == nil || a.hwnd == 0 || a.s.Mode != "taskbar" {
		return 0
	}
	if event == eventSystemForeground && a.hidden && !a.fullscreenActive() {
		a.render(false)
	}
	if !a.hidden {
		a.kickRaiseBurst()
	}
	return 0
}

// isTaskbarAbove reports whether Shell_TrayWnd currently sits higher in z-order than a.hwnd.
func (a *App) isTaskbarAbove() bool {
	tb, _, _ := pFindWindowW.Call(uintptr(unsafe.Pointer(u16("Shell_TrayWnd"))), 0)
	if tb == 0 {
		return false
	}
	h := a.hwnd
	for i := 0; i < 64; i++ {
		h, _, _ = pGetWindow.Call(h, gwHwndPrev)
		if h == 0 {
			return false
		}
		if h == tb {
			return true
		}
	}
	return true
}

func (a *App) ensureAboveTaskbar() {
	if a.s.Mode != "taskbar" || a.hidden {
		return
	}
	if a.isTaskbarAbove() {
		pSetWindowPos.Call(a.hwnd, HWND_TOPMOST, 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE|SWP_NOACTIVATE)
	}
}

func (a *App) kickRaiseBurst() {
	a.ensureAboveTaskbar()
	a.raiseLeft = raiseBurstTicks
	pSetTimer.Call(a.hwnd, timerRaise, raiseBurstMs, 0)
}

func (a *App) onRaiseTick() {
	if a.s.Mode != "taskbar" || a.hidden || a.raiseLeft <= 0 {
		a.raiseLeft = 0
		pKillTimer.Call(a.hwnd, timerRaise)
		return
	}
	a.ensureAboveTaskbar()
	a.raiseLeft--
	if a.raiseLeft <= 0 {
		pKillTimer.Call(a.hwnd, timerRaise)
	}
}
