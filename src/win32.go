//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	setupapi = windows.NewLazySystemDLL("setupapi.dll")
	hiddll   = windows.NewLazySystemDLL("hid.dll")
	btapis   = windows.NewLazySystemDLL("BluetoothApis.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassExW          = user32.NewProc("RegisterClassExW")
	pCreateWindowExW           = user32.NewProc("CreateWindowExW")
	pDefWindowProcW            = user32.NewProc("DefWindowProcW")
	pGetMessageW               = user32.NewProc("GetMessageW")
	pTranslateMessage          = user32.NewProc("TranslateMessage")
	pDispatchMessageW          = user32.NewProc("DispatchMessageW")
	pPostMessageW              = user32.NewProc("PostMessageW")
	pPostQuitMessage           = user32.NewProc("PostQuitMessage")
	pSetTimer                  = user32.NewProc("SetTimer")
	pShowWindow                = user32.NewProc("ShowWindow")
	pSetWindowPos              = user32.NewProc("SetWindowPos")
	pGetWindowRect             = user32.NewProc("GetWindowRect")
	pFindWindowW               = user32.NewProc("FindWindowW")
	pFindWindowExW             = user32.NewProc("FindWindowExW")
	pUpdateLayeredWindow       = user32.NewProc("UpdateLayeredWindow")
	pGetDC                     = user32.NewProc("GetDC")
	pReleaseDC                 = user32.NewProc("ReleaseDC")
	pCreatePopupMenu           = user32.NewProc("CreatePopupMenu")
	pAppendMenuW               = user32.NewProc("AppendMenuW")
	pTrackPopupMenu            = user32.NewProc("TrackPopupMenu")
	pDestroyMenu               = user32.NewProc("DestroyMenu")
	pGetCursorPos              = user32.NewProc("GetCursorPos")
	pSetForegroundWindow       = user32.NewProc("SetForegroundWindow")
	pGetForegroundWindow       = user32.NewProc("GetForegroundWindow")
	pGetClassNameW             = user32.NewProc("GetClassNameW")
	pMonitorFromWindow         = user32.NewProc("MonitorFromWindow")
	pMonitorFromPoint          = user32.NewProc("MonitorFromPoint")
	pGetMonitorInfoW           = user32.NewProc("GetMonitorInfoW")
	pRegisterWindowMessageW    = user32.NewProc("RegisterWindowMessageW")
	pMessageBoxW               = user32.NewProc("MessageBoxW")
	pCreateIconIndirect        = user32.NewProc("CreateIconIndirect")
	pDestroyIcon               = user32.NewProc("DestroyIcon")
	pSetWindowLongPtrW         = user32.NewProc("SetWindowLongPtrW")
	pGetWindowLongPtrW         = user32.NewProc("GetWindowLongPtrW")
	pSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	pSetProcessDPIAware        = user32.NewProc("SetProcessDPIAware")
	pGetDpiForWindow           = user32.NewProc("GetDpiForWindow")
	pGetSystemMetrics          = user32.NewProc("GetSystemMetrics")
	pIsWindowVisible           = user32.NewProc("IsWindowVisible")
	pLoadIconW                 = user32.NewProc("LoadIconW")

	pCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	pSelectObject       = gdi32.NewProc("SelectObject")
	pDeleteObject       = gdi32.NewProc("DeleteObject")
	pDeleteDC           = gdi32.NewProc("DeleteDC")
	pCreateBitmap       = gdi32.NewProc("CreateBitmap")

	pShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	pShellExecuteW    = shell32.NewProc("ShellExecuteW")

	pTaskDialogIndirect = comctl32.NewProc("TaskDialogIndirect")
	pGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	pCreateMutexW       = kernel32.NewProc("CreateMutexW")
)

const (
	WS_POPUP                 = 0x80000000
	WS_EX_LAYERED            = 0x00080000
	WS_EX_TOPMOST            = 0x00000008
	WS_EX_TOOLWINDOW         = 0x00000080
	WS_EX_NOACTIVATE         = 0x08000000
	WS_EX_TRANSPARENT        = 0x00000020
	GWL_EXSTYLE              = -20
	WM_DESTROY               = 0x0002
	WM_TIMER                 = 0x0113
	WM_COMMAND               = 0x0111
	WM_NCHITTEST             = 0x0084
	WM_EXITSIZEMOVE          = 0x0232
	WM_RBUTTONUP             = 0x0205
	WM_LBUTTONUP             = 0x0202
	WM_LBUTTONDBLCLK         = 0x0203
	WM_CONTEXTMENU           = 0x007B
	WM_NCRBUTTONUP           = 0x00A5
	WM_DISPLAYCHANGE         = 0x007E
	WM_SETTINGCHANGE         = 0x001A
	WM_DPICHANGED            = 0x02E0
	WM_APP                   = 0x8000
	WM_USER                  = 0x0400
	HTCAPTION                = 2
	SW_SHOWNOACTIVATE        = 4
	SW_HIDE                  = 0
	SWP_NOACTIVATE           = 0x0010
	SWP_NOSIZE               = 0x0001
	SWP_NOMOVE               = 0x0002
	SWP_SHOWWINDOW           = 0x0040
	HWND_TOPMOST             = ^uintptr(0) // -1
	MF_STRING                = 0x0000
	MF_SEPARATOR             = 0x0800
	MF_CHECKED               = 0x0008
	MF_GRAYED                = 0x0001
	MF_POPUP                 = 0x0010
	TPM_RIGHTBUTTON          = 0x0002
	TPM_RETURNCMD            = 0x0100
	TPM_BOTTOMALIGN          = 0x0020
	ULW_ALPHA                = 0x02
	AC_SRC_OVER              = 0x00
	AC_SRC_ALPHA             = 0x01
	NIM_ADD                  = 0
	NIM_MODIFY               = 1
	NIM_DELETE               = 2
	NIM_SETVERSION           = 4
	NIF_MESSAGE              = 0x01
	NIF_ICON                 = 0x02
	NIF_TIP                  = 0x04
	NIF_INFO                 = 0x10
	NIF_SHOWTIP              = 0x80
	NIIF_INFO                = 0x01
	NIIF_WARNING             = 0x02
	NIIF_USER                = 0x04
	NIIF_LARGE_ICON          = 0x20
	NOTIFYICON_VERSION4      = 4
	MONITOR_DEFAULTTONEAREST = 2
	MB_ICONINFORMATION       = 0x40
	MB_ICONWARNING           = 0x30
	MB_TOPMOST               = 0x40000
)

type POINT struct{ X, Y int32 }
type RECT struct{ Left, Top, Right, Bottom int32 }
type SIZE struct{ CX, CY int32 }

type MSG struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      POINT
	Private uint32
}

type WNDCLASSEXW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type BLENDFUNCTION struct {
	BlendOp             byte
	BlendFlags          byte
	SourceConstantAlpha byte
	AlphaFormat         byte
}

type BITMAPINFOHEADER struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type MONITORINFO struct {
	CbSize    uint32
	RcMonitor RECT
	RcWork    RECT
	DwFlags   uint32
}

type NOTIFYICONDATAW struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

type ICONINFO struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	HbmMask  uintptr
	HbmColor uintptr
}

func u16(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }

func copyU16(dst []uint16, s string) {
	src, _ := windows.UTF16FromString(s)
	if len(src) > len(dst) {
		src = src[:len(dst)]
		src[len(src)-1] = 0
	}
	copy(dst, src)
}

func loword(v uintptr) uint16 { return uint16(v & 0xffff) }

func msgBox(text, title string, flags uintptr) {
	pMessageBoxW.Call(0, uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16(title))), flags|MB_TOPMOST)
}

func shellOpen(target string) {
	pShellExecuteW.Call(0, uintptr(unsafe.Pointer(u16("open"))), uintptr(unsafe.Pointer(u16(target))), 0, 0, 1)
}

func windowClass(h uintptr) string {
	buf := make([]uint16, 256)
	n, _, _ := pGetClassNameW.Call(h, uintptr(unsafe.Pointer(&buf[0])), 256)
	return syscall.UTF16ToString(buf[:n])
}

func getRect(h uintptr) RECT {
	var r RECT
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
	return r
}
