//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"image"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func init() { runtime.LockOSThread() }

const (
	wmScanDone = WM_APP + 1
	wmTray     = WM_APP + 2
	timerTick  = 1

	idDevBase     = 1000
	idTaskbar     = 200
	idDesktop     = 201
	idClock       = 202
	idBackground  = 203
	idThrBase     = 210
	idNotifyFull  = 220
	idAutostart   = 230
	idRefresh     = 240
	idTestNotify  = 241
	idNotifyPopup = 242
	idNotifyWin   = 243
	idSoundOff    = 244
	idSoundChime  = 245
	idSoundSpeak  = 246
	idSizeBase    = 300
	idAbout       = 250
	idDiag        = 251
	idExit        = 260
)

var thresholds = []int{0, 10, 15, 20, 30}
var sizes = []float64{0.8, 1.0, 1.25, 1.5, 1.75, 2.0}

type App struct {
	hwnd        uintptr
	hinst       uintptr
	s           Settings
	fs          *fontSet
	mu          sync.Mutex
	pendingDevs []Device
	pendingOff  []string
	hasPending  bool
	all         []Device // all detected (incl. hidden)
	shown       []Device
	refresh     chan struct{}
	lowN, critN map[string]bool
	fullN       map[string]bool
	trayIcon    uintptr
	trayAdded   bool
	msgTaskbar  uint32
	lastSig     string
	hidden      bool
	exTransp    bool
	scannedOnce bool
	light       bool
	traySig     string
	toastHwnd   uintptr
	toastImg    *image.RGBA
	toastStart  time.Time
	toastAlpha  float64
}

var app *App

func main() {
	name := u16("Local\\ZETXBatteryInstance")
	h, _, err := pCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h != 0 && err == windows.ERROR_ALREADY_EXISTS {
		msgBox("ZETX BATTERY zaten çalışıyor. Sistem tepsisindeki (saatin yanındaki) pil simgesine sağ tıklayın.", appName, MB_ICONINFORMATION)
		return
	}
	enableVisualStyles()
	if pSetProcessDpiAwarenessCtx.Find() == nil {
		pSetProcessDpiAwarenessCtx.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2 (-4)
	} else {
		pSetProcessDPIAware.Call()
	}

	_, statErr := os.Stat(settingsPath())
	firstRun := statErr != nil

	app = &App{
		s:       loadSettings(),
		fs:      loadFonts(),
		refresh: make(chan struct{}, 1),
		lowN:    map[string]bool{}, critN: map[string]bool{}, fullN: map[string]bool{},
		light: lightTheme(),
	}
	app.hinst, _, _ = pGetModuleHandleW.Call(0)
	if firstRun {
		if !installedBySetup() { // the setup program already applied the user's autostart choice
			setAutostart(true)
		}
		app.s.save()
	}
	app.createWindow()
	app.addTray()
	pSetTimer.Call(app.hwnd, timerTick, 1000, 0)
	go app.scanLoop()
	app.render(true)
	if firstRun {
		app.balloon("ZETX BATTERY çalışıyor", "Pil yüzdeleri görev çubuğunda, saatin solunda görünür. Ayarlar için tepsideki pil simgesine sağ tıklayın.", NIIF_INFO)
	}

	var m MSG
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	app.removeTray()
}

// ---------------- window ----------------

func (a *App) createWindow() {
	cls := u16("ZETXBatteryBar")
	wc := WNDCLASSEXW{
		CbSize:        uint32(unsafe.Sizeof(WNDCLASSEXW{})),
		LpfnWndProc:   syscall.NewCallback(wndProc),
		HInstance:     a.hinst,
		LpszClassName: cls,
	}
	wc.HIcon, _, _ = pLoadIconW.Call(a.hinst, uintptr(unsafe.Pointer(u16("APP"))))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	ex := uintptr(WS_EX_LAYERED | WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE)
	a.hwnd, _, _ = pCreateWindowExW.Call(ex, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(u16(appName))),
		WS_POPUP, 0, 0, 10, 10, 0, 0, a.hinst, 0)
	a.msgTaskbar = registerMsg("TaskbarCreated")
	a.applyClickThrough()
	pShowWindow.Call(a.hwnd, SW_SHOWNOACTIVATE)
}

func registerMsg(s string) uint32 {
	r, _, _ := pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(u16(s))))
	return uint32(r)
}

func (a *App) applyClickThrough() {
	want := a.s.Mode == "taskbar"
	ex, _, _ := pGetWindowLongPtrW.Call(a.hwnd, ^uintptr(19))
	if want {
		ex |= WS_EX_TRANSPARENT
	} else {
		ex &^= WS_EX_TRANSPARENT
	}
	pSetWindowLongPtrW.Call(a.hwnd, ^uintptr(19), ex) // GWL_EXSTYLE = -20
	a.exTransp = want
}

func wndProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	a := app
	if a != nil && hwnd == a.toastHwnd && hwnd != 0 {
		switch msg {
		case WM_TIMER:
			a.toastStep()
			return 0
		case WM_LBUTTONUP, WM_RBUTTONUP:
			a.hideToast()
			return 0
		case WM_NCHITTEST:
			return 1 // HTCLIENT
		}
		r, _, _ := pDefWindowProcW.Call(hwnd, uintptr(msg), wp, lp)
		return r
	}
	switch msg {
	case WM_TIMER:
		a.onTick()
		return 0
	case wmScanDone:
		a.onScanDone()
		return 0
	case wmTray:
		switch uint32(lp & 0xFFFF) {
		case WM_RBUTTONUP, WM_LBUTTONUP, WM_CONTEXTMENU:
			a.showMenu()
		}
		return 0
	case WM_NCHITTEST:
		if a.s.Mode == "desktop" {
			return HTCAPTION
		}
	case WM_NCRBUTTONUP, WM_RBUTTONUP:
		a.showMenu()
		return 0
	case 0x020A: // WM_MOUSEWHEEL: resize in desktop mode
		if a.s.Mode == "desktop" {
			delta := int16(wp >> 16)
			i := 0
			for j, sz := range sizes {
				if sz == a.s.Size {
					i = j
				}
			}
			if delta > 0 && i < len(sizes)-1 {
				i++
			} else if delta < 0 && i > 0 {
				i--
			}
			a.s.Size = sizes[i]
			a.s.save()
			a.render(true)
		}
		return 0
	case WM_EXITSIZEMOVE:
		r := getRect(hwnd)
		a.s.X, a.s.Y, a.s.HasPos = r.Left, r.Top, true
		a.s.save()
		return 0
	case 0x0219: // WM_DEVICECHANGE: a receiver, cable or Bluetooth device was added/removed
		select {
		case a.refresh <- struct{}{}:
		default:
		}
		return 1
	case WM_SETTINGCHANGE, WM_DISPLAYCHANGE, WM_DPICHANGED:
		a.light = lightTheme()
		a.updateTray()
		a.render(true)
	case WM_DESTROY:
		pPostQuitMessage.Call(0)
		return 0
	}
	if a != nil && msg == a.msgTaskbar && msg != 0 {
		a.trayAdded = false
		a.addTray()
		a.render(true)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, uintptr(msg), wp, lp)
	return r
}

func (a *App) onTick() {
	a.render(false)
	if a.s.Mode == "taskbar" && !a.hidden {
		pSetWindowPos.Call(a.hwnd, HWND_TOPMOST, 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE|SWP_NOACTIVATE)
	}
}

// fullscreenActive reports whether a game/video covers the whole monitor.
func (a *App) fullscreenActive() bool {
	fg, _, _ := pGetForegroundWindow.Call()
	if fg == 0 || fg == a.hwnd {
		return false
	}
	switch windowClass(fg) {
	case "Progman", "WorkerW", "Shell_TrayWnd", "Shell_SecondaryTrayWnd", "NotifyIconOverflowWindow", "Windows.UI.Core.CoreWindow":
		return false
	}
	r := getRect(fg)
	mon, _, _ := pMonitorFromWindow.Call(fg, MONITOR_DEFAULTTONEAREST)
	mi := MONITORINFO{CbSize: uint32(unsafe.Sizeof(MONITORINFO{}))}
	pGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
	m := mi.RcMonitor
	return r.Left <= m.Left && r.Top <= m.Top && r.Right >= m.Right && r.Bottom >= m.Bottom
}

func (a *App) dpiScale() float64 {
	if pGetDpiForWindow.Find() == nil {
		if d, _, _ := pGetDpiForWindow.Call(a.hwnd); d > 0 {
			return float64(d) / 96
		}
	}
	return 1
}

func (a *App) render(force bool) {
	if a.fullscreenActive() {
		if !a.hidden {
			pShowWindow.Call(a.hwnd, SW_HIDE)
			a.hidden = true
		}
		return
	}
	if a.hidden {
		pShowWindow.Call(a.hwnd, SW_SHOWNOACTIVATE)
		a.hidden = false
		force = true
	}

	mode := a.s.Mode
	var height int
	var scale float64
	var x, y int32
	tb, _, _ := pFindWindowW.Call(uintptr(unsafe.Pointer(u16("Shell_TrayWnd"))), 0)
	var tbR, trayR RECT
	horizontal := false
	if mode == "taskbar" && tb != 0 {
		tbR = getRect(tb)
		tray, _, _ := pFindWindowExW.Call(tb, 0, uintptr(unsafe.Pointer(u16("TrayNotifyWnd"))), 0)
		if tray != 0 {
			trayR = getRect(tray)
		} else {
			trayR = RECT{Left: tbR.Right - 200}
		}
		horizontal = tbR.Right-tbR.Left > tbR.Bottom-tbR.Top
	}
	if mode == "taskbar" && horizontal {
		height = int(tbR.Bottom - tbR.Top)
		scale = float64(height) / 40
		if scale < 0.75 {
			scale = 0.75
		}
		if scale > a.dpiScale()*1.25 {
			scale = a.dpiScale()
		}
		scale *= a.s.Size
		limit := 25.0
		if a.s.ShowClock {
			limit = 30
		}
		if maxS := float64(height) / limit; scale > maxS { // keep icons inside the taskbar
			scale = maxS
		}
	} else {
		scale = a.dpiScale() * a.s.Size
		height = int(40 * scale)
	}
	opts := renderOpts{
		Height: height, Scale: scale, Light: a.light,
		Bg:        mode == "desktop" && a.s.Background,
		Shadow:    mode == "desktop" && !a.s.Background,
		Clock:     a.s.ShowClock,
		Threshold: a.s.Threshold,
	}
	sig := fmt.Sprintf("%v|%v|%d|%v|%d,%d,%d,%d|%d", opts, a.shown, time.Now().Minute(), mode, tbR.Left, tbR.Top, trayR.Left, tbR.Bottom, a.s.X)
	if !force && sig == a.lastSig {
		return
	}
	a.lastSig = sig
	img := renderBar(a.fs, a.shown, opts)
	w := int32(img.Rect.Dx())
	if mode == "taskbar" && horizontal {
		x = trayR.Left - w - int32(2*scale)
		y = tbR.Top
	} else {
		if a.s.HasPos && mode == "desktop" {
			x, y = a.s.X, a.s.Y
		} else {
			mon, _, _ := pMonitorFromPoint.Call(0, MONITOR_DEFAULTTONEAREST)
			mi := MONITORINFO{CbSize: uint32(unsafe.Sizeof(MONITORINFO{}))}
			pGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
			x = mi.RcWork.Right - w - int32(12*scale)
			y = mi.RcWork.Bottom - int32(height) - int32(12*scale)
		}
	}
	a.present(img, x, y)
}

func (a *App) present(img *image.RGBA, x, y int32) { a.presentOn(a.hwnd, img, x, y, 255) }

func (a *App) presentOn(hwnd uintptr, img *image.RGBA, x, y int32, alpha byte) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	screen, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, screen)
	mem, _, _ := pCreateCompatibleDC.Call(screen)
	defer pDeleteDC.Call(mem)
	bmp, bits := dib(screen, w, h, img)
	if bmp == 0 {
		return
	}
	_ = bits
	old, _, _ := pSelectObject.Call(mem, bmp)
	pt := POINT{x, y}
	sz := SIZE{int32(w), int32(h)}
	src := POINT{}
	blend := BLENDFUNCTION{AC_SRC_OVER, 0, alpha, AC_SRC_ALPHA}
	pUpdateLayeredWindow.Call(hwnd, screen, uintptr(unsafe.Pointer(&pt)), uintptr(unsafe.Pointer(&sz)), mem,
		uintptr(unsafe.Pointer(&src)), 0, uintptr(unsafe.Pointer(&blend)), ULW_ALPHA)
	pSelectObject.Call(mem, old)
	pDeleteObject.Call(bmp)
}

// dib creates a 32bpp top-down DIB section holding premultiplied BGRA pixels.
func dib(hdc uintptr, w, h int, img *image.RGBA) (uintptr, unsafe.Pointer) {
	bi := BITMAPINFOHEADER{BiSize: 40, BiWidth: int32(w), BiHeight: -int32(h), BiPlanes: 1, BiBitCount: 32}
	var bits unsafe.Pointer
	bmp, _, _ := pCreateDIBSection.Call(hdc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		return 0, nil
	}
	dst := unsafe.Slice((*byte)(bits), w*h*4)
	for i := 0; i < w*h; i++ {
		p := img.Pix[i*4 : i*4+4]
		dst[i*4], dst[i*4+1], dst[i*4+2], dst[i*4+3] = p[2], p[1], p[0], p[3]
	}
	return bmp, bits
}

// ---------------- scanning ----------------

// scanLoop: full scan (all sources) every Interval seconds or on device change;
// Logitech HID++ devices are re-read every 2 seconds over the open connections.
func (a *App) scanLoop() {
	mon := newLgMonitor()
	ss := newSsMonitor()
	go ghub.run()
	var other []Device
	var lastFull time.Time
	tick := 0
	for {
		tick++
		a.mu.Lock()
		interval := time.Duration(a.s.Interval) * time.Second
		a.mu.Unlock()
		if lastFull.IsZero() || time.Since(lastFull) >= interval {
			other = safeFull(mon, ss, other)
			lastFull = time.Now()
		} else {
			safePoll(mon)
			if tick%2 == 0 {
				safeSSPoll(ss)
			}
		}
		lg, _ := mon.devices()
		ssd, _ := ss.devices()
		devs := append(append(append([]Device{}, lg...), ssd...), other...)
		devs = applyGHub(devs)
		sortDevices(devs)
		a.mu.Lock()
		a.pendingDevs, a.pendingOff, a.hasPending = devs, nil, true
		a.mu.Unlock()
		pPostMessageW.Call(a.hwnd, wmScanDone, 0, 0)
		select {
		case <-time.After(2 * time.Second):
		case <-a.refresh:
			time.Sleep(1200 * time.Millisecond) // let Windows finish installing the new device
			lastFull = time.Time{}
		}
	}
}

func safeSSPoll(ss *ssMonitor) {
	defer func() { recover() }()
	ss.poll()
}

func safeFull(mon *lgMonitor, ss *ssMonitor, prev []Device) (out []Device) {
	defer func() {
		if r := recover(); r != nil {
			logf("tarama hatasi: %v", r)
			out = prev
		}
	}()
	return FullScan(mon, ss)
}

func safePoll(mon *lgMonitor) {
	defer func() { recover() }()
	mon.poll()
}

func (a *App) onScanDone() {
	a.mu.Lock()
	if !a.hasPending {
		a.mu.Unlock()
		return
	}
	devs, off := a.pendingDevs, a.pendingOff
	a.hasPending = false
	a.mu.Unlock()

	now := time.Now()
	dirty := false
	for _, d := range devs {
		if !d.Online || d.Percent < 0 {
			continue
		}
		old, ok := a.s.Cache[d.Key]
		if !ok || old.Percent != d.Percent || old.Name != d.Name || old.Kind != d.Kind || now.Sub(old.Seen) > 10*time.Minute {
			a.s.Cache[d.Key] = CachedDev{Name: d.Name, Kind: d.Kind, Percent: d.Percent, Seen: now}
			dirty = true
		}
	}
	for i := range devs {
		d := &devs[i]
		if d.Online {
			continue
		}
		if c, ok := a.s.Cache[d.Key]; ok && now.Sub(c.Seen) < 14*24*time.Hour {
			d.Percent, d.Name = c.Percent, c.Name
			if c.Kind != KindOther {
				d.Kind = c.Kind
			}
		}
	}
	_ = off
	// A mouse plugged in with its charging cable shows up as a wired device; hide its sleeping receiver slot.
	norm := func(s string) string { return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "logitech ") }
	online := map[string]bool{}
	for _, d := range devs {
		if d.Online {
			online[norm(d.Name)] = true
		}
	}
	kept := devs[:0]
	for _, d := range devs {
		if !d.Online && online[norm(d.Name)] {
			continue
		}
		kept = append(kept, d)
	}
	devs = kept
	// stable order: kind, then key
	for i := 1; i < len(devs); i++ {
		for j := i; j > 0 && (devs[j].Kind < devs[j-1].Kind || (devs[j].Kind == devs[j-1].Kind && devs[j].Key < devs[j-1].Key)); j-- {
			devs[j], devs[j-1] = devs[j-1], devs[j]
		}
	}
	a.all = devs
	a.rebuildShown()
	if dirty {
		a.s.save()
	}
	a.checkAlerts()
	if sig := fmt.Sprintf("%v|%v", a.shown, a.light); sig != a.traySig || !a.trayAdded {
		a.traySig = sig
		a.updateTray()
	}
	a.render(false)
}

func (a *App) rebuildShown() {
	var shown []Device
	count := map[Kind]int{}
	for _, d := range a.all {
		if !a.s.Hidden[d.Key] {
			count[d.Kind]++
		}
	}
	idx := map[Kind]int{}
	for _, d := range a.all {
		if a.s.Hidden[d.Key] {
			continue
		}
		d.Index = 0
		if count[d.Kind] > 1 {
			idx[d.Kind]++
			d.Index = idx[d.Kind]
		}
		shown = append(shown, d)
	}
	a.shown = shown
}

// ---------------- notifications ----------------

func (a *App) checkAlerts() {
	thr := a.s.Threshold
	for _, d := range a.shown {
		if !d.Online || d.Percent < 0 {
			continue
		}
		k := d.Key
		label := fmt.Sprintf("%s (%s)", d.Kind.Label(), d.Name)
		if thr > 0 && !d.Charging && d.Percent <= thr {
			if !a.lowN[k] {
				a.lowN[k] = true
				a.notify(alert{level: alertLow, kind: d.Kind, title: "Düşük pil: " + d.Kind.Label(),
					text:   fmt.Sprintf("%s şarjı %%%d seviyesine düştü. Lütfen şarja takın.", label, d.Percent),
					spoken: fmt.Sprintf("%s pili yüzde %d. Lütfen şarja takın.", d.Kind.Label(), d.Percent)})
			} else if d.Percent <= 5 && !a.critN[k] {
				a.critN[k] = true
				a.notify(alert{level: alertCritical, kind: d.Kind, title: "Pil kritik seviyede!",
					text:   fmt.Sprintf("%s şarjı %%%d. Cihaz birazdan kapanabilir.", label, d.Percent),
					spoken: fmt.Sprintf("Dikkat! %s pili yüzde %d, birazdan kapanabilir.", d.Kind.Label(), d.Percent)})
			}
		}
		if d.Charging || d.Percent > thr+5 {
			a.lowN[k], a.critN[k] = false, false
		}
		if a.s.NotifyFull && d.Charging && d.Percent >= 100 && !a.fullN[k] {
			a.fullN[k] = true
			a.notify(alert{level: alertFull, kind: d.Kind, title: "Şarj tamamlandı",
				text:   fmt.Sprintf("%s tam dolu (%%100). Şarjdan çıkarabilirsiniz.", label),
				spoken: fmt.Sprintf("%s şarjı doldu.", d.Kind.Label())})
		}
		if !d.Charging {
			a.fullN[k] = false
		}
	}
}

// ---------------- tray ----------------

func (a *App) nid() NOTIFYICONDATAW {
	return NOTIFYICONDATAW{CbSize: uint32(unsafe.Sizeof(NOTIFYICONDATAW{})), HWnd: a.hwnd, UID: 1}
}

func (a *App) trayTip() string {
	var sb strings.Builder
	sb.WriteString(appName)
	if len(a.shown) == 0 {
		if a.scannedOnce || len(a.all) > 0 {
			sb.WriteString("\nKablosuz cihaz bulunamadı")
		} else {
			sb.WriteString("\nCihazlar taranıyor…")
		}
	}
	for _, d := range a.shown {
		val := fmt.Sprintf("%%%d", d.Percent)
		if d.Percent < 0 {
			val = "pil bilgisi yok"
		}
		line := fmt.Sprintf("\n%s: %s", d.Kind.Label(), val)
		if d.Index > 0 {
			line = fmt.Sprintf("\n%s %d: %s", d.Kind.Label(), d.Index, val)
		}
		if d.Charging {
			line += " ⚡"
		}
		if !d.Online {
			line += " (uykuda)"
		}
		sb.WriteString(line)
	}
	return sb.String()
}

func (a *App) makeTrayIcon() uintptr {
	low, any := 100, false
	for _, d := range a.shown {
		if d.Online && d.Percent >= 0 {
			any = true
			if d.Percent < low {
				low = d.Percent
			}
		}
	}
	img := renderTrayIcon(low, any, a.light)
	screen, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, screen)
	color, _ := dib(screen, 32, 32, img)
	maskBits := make([]byte, 128)
	mask, _, _ := pCreateBitmap.Call(32, 32, 1, 1, uintptr(unsafe.Pointer(&maskBits[0])))
	ii := ICONINFO{FIcon: 1, HbmMask: mask, HbmColor: color}
	icon, _, _ := pCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	pDeleteObject.Call(color)
	pDeleteObject.Call(mask)
	return icon
}

func (a *App) addTray() {
	if a.trayIcon == 0 {
		a.trayIcon = a.makeTrayIcon()
	}
	n := a.nid()
	n.UFlags = NIF_MESSAGE | NIF_ICON | NIF_TIP
	n.UCallbackMessage = wmTray
	n.HIcon = a.trayIcon
	copyU16(n.SzTip[:], a.trayTip())
	r, _, _ := pShellNotifyIconW.Call(NIM_ADD, uintptr(unsafe.Pointer(&n)))
	a.trayAdded = r != 0
}

func (a *App) updateTray() {
	a.scannedOnce = true
	old := a.trayIcon
	a.trayIcon = a.makeTrayIcon()
	if !a.trayAdded {
		a.addTray()
	} else {
		n := a.nid()
		n.UFlags = NIF_ICON | NIF_TIP
		n.HIcon = a.trayIcon
		copyU16(n.SzTip[:], a.trayTip())
		pShellNotifyIconW.Call(NIM_MODIFY, uintptr(unsafe.Pointer(&n)))
	}
	if old != 0 {
		pDestroyIcon.Call(old)
	}
}

func (a *App) removeTray() {
	n := a.nid()
	pShellNotifyIconW.Call(NIM_DELETE, uintptr(unsafe.Pointer(&n)))
}

func (a *App) balloon(title, text string, flags uint32) bool {
	n := a.nid()
	n.UFlags = NIF_INFO
	copyU16(n.SzInfoTitle[:], title)
	copyU16(n.SzInfo[:], text)
	n.DwInfoFlags = flags | NIIF_LARGE_ICON
	r, _, err := pShellNotifyIconW.Call(NIM_MODIFY, uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		logf("Windows bildirimi gonderilemedi: %v", err)
	}
	return r != 0
}

// ---------------- menu ----------------

func appendItem(m uintptr, flags uintptr, id int, text string) {
	pAppendMenuW.Call(m, flags, uintptr(id), uintptr(unsafe.Pointer(u16(text))))
}

func check(b bool) uintptr {
	if b {
		return MF_CHECKED
	}
	return 0
}

func (a *App) showMenu() {
	m, _, _ := pCreatePopupMenu.Call()
	defer pDestroyMenu.Call(m)
	appendItem(m, MF_STRING|MF_GRAYED, 1, appName+"  v"+appVersion)
	pAppendMenuW.Call(m, MF_SEPARATOR, 0, 0)

	devMenu, _, _ := pCreatePopupMenu.Call()
	if len(a.all) == 0 {
		appendItem(devMenu, MF_STRING|MF_GRAYED, 2, "Kablosuz cihaz bulunamadı")
	}
	for i, d := range a.all {
		state := fmt.Sprintf("%%%d", d.Percent)
		if d.Percent < 0 {
			state = "pil bilgisi yok"
		}
		if d.Charging {
			state += " şarj oluyor"
		}
		if !d.Online {
			state += " (uykuda)"
		}
		appendItem(devMenu, MF_STRING|check(!a.s.Hidden[d.Key]), idDevBase+i, fmt.Sprintf("%s — %s   %s   [%s]", d.Kind.Label(), d.Name, state, d.Source))
	}
	pAppendMenuW.Call(m, MF_POPUP, devMenu, uintptr(unsafe.Pointer(u16("Cihazlar (işaretliler gösterilir)"))))
	pAppendMenuW.Call(m, MF_SEPARATOR, 0, 0)

	appendItem(m, MF_STRING|check(a.s.Mode == "taskbar"), idTaskbar, "Görev çubuğunda göster (saatin solu)")
	appendItem(m, MF_STRING|check(a.s.Mode == "desktop"), idDesktop, "Masaüstünde serbest (sürükle, tekerlekle büyüt/küçült)")
	sizeMenu, _, _ := pCreatePopupMenu.Call()
	for i, sz := range sizes {
		label := fmt.Sprintf("%%%d", int(sz*100+0.5))
		if sz == 1 {
			label += " (normal)"
		}
		appendItem(sizeMenu, MF_STRING|check(a.s.Size == sz), idSizeBase+i, label)
	}
	pAppendMenuW.Call(m, MF_POPUP, sizeMenu, uintptr(unsafe.Pointer(u16("Simge boyutu"))))
	appendItem(m, MF_STRING|check(a.s.ShowClock), idClock, "Saat ve tarihi göster")
	bgFlags := uintptr(MF_STRING) | check(a.s.Background)
	if a.s.Mode != "desktop" {
		bgFlags |= MF_GRAYED
	}
	appendItem(m, bgFlags, idBackground, "Koyu arka plan (masaüstü modu)")
	pAppendMenuW.Call(m, MF_SEPARATOR, 0, 0)

	nMenu, _, _ := pCreatePopupMenu.Call()
	thrMenu, _, _ := pCreatePopupMenu.Call()
	for i, t := range thresholds {
		label := "Kapalı"
		if t > 0 {
			label = fmt.Sprintf("%%%d ve altı", t)
		}
		appendItem(thrMenu, MF_STRING|check(a.s.Threshold == t), idThrBase+i, label)
	}
	pAppendMenuW.Call(nMenu, MF_POPUP, thrMenu, uintptr(unsafe.Pointer(u16("Düşük pil uyarısı"))))
	appendItem(nMenu, MF_STRING|check(a.s.NotifyFull), idNotifyFull, "Şarj dolunca bildir")
	pAppendMenuW.Call(nMenu, MF_SEPARATOR, 0, 0)
	appendItem(nMenu, MF_STRING|check(a.s.NotifyPopup), idNotifyPopup, "Metin: ekranda açılır kutu")
	appendItem(nMenu, MF_STRING|check(a.s.NotifyWindows), idNotifyWin, "Metin: Windows bildirimi")
	pAppendMenuW.Call(nMenu, MF_SEPARATOR, 0, 0)
	appendItem(nMenu, MF_STRING|check(a.s.Sound == "none"), idSoundOff, "Ses: kapalı")
	appendItem(nMenu, MF_STRING|check(a.s.Sound == "chime"), idSoundChime, "Ses: uyarı sesi")
	appendItem(nMenu, MF_STRING|check(a.s.Sound == "speak"), idSoundSpeak, "Ses: sesli okuma (Türkçe)")
	pAppendMenuW.Call(nMenu, MF_SEPARATOR, 0, 0)
	appendItem(nMenu, MF_STRING, idTestNotify, "Bildirimi test et")
	pAppendMenuW.Call(m, MF_POPUP, nMenu, uintptr(unsafe.Pointer(u16("Bildirimler"))))
	appendItem(m, MF_STRING, idTestNotify, "Bildirimi test et")
	pAppendMenuW.Call(m, MF_SEPARATOR, 0, 0)
	appendItem(m, MF_STRING|check(autostartEnabled()), idAutostart, "Windows ile başlat")
	appendItem(m, MF_STRING, idRefresh, "Şimdi yenile")
	appendItem(m, MF_STRING, idDiag, "Tanılama raporunu aç")
	appendItem(m, MF_STRING, idAbout, "Hakkında")
	pAppendMenuW.Call(m, MF_SEPARATOR, 0, 0)
	appendItem(m, MF_STRING, idExit, "Çıkış")

	var pt POINT
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(a.hwnd)
	cmd, _, _ := pTrackPopupMenu.Call(m, TPM_RIGHTBUTTON|TPM_RETURNCMD|TPM_BOTTOMALIGN, uintptr(pt.X), uintptr(pt.Y), 0, a.hwnd, 0)
	pPostMessageW.Call(a.hwnd, 0, 0, 0)
	a.onCommand(int(cmd))
}

func (a *App) onCommand(id int) {
	switch {
	case id == 0:
		return
	case id >= idDevBase && id < idDevBase+len(a.all):
		k := a.all[id-idDevBase].Key
		if a.s.Hidden[k] {
			delete(a.s.Hidden, k)
		} else {
			a.s.Hidden[k] = true
		}
		a.rebuildShown()
		a.updateTray()
	case id == idTaskbar:
		a.s.Mode = "taskbar"
		a.applyClickThrough()
	case id == idDesktop:
		a.s.Mode = "desktop"
		a.applyClickThrough()
	case id >= idSizeBase && id < idSizeBase+len(sizes):
		a.s.Size = sizes[id-idSizeBase]
	case id == idClock:
		a.s.ShowClock = !a.s.ShowClock
	case id == idBackground:
		a.s.Background = !a.s.Background
	case id >= idThrBase && id < idThrBase+len(thresholds):
		a.s.Threshold = thresholds[id-idThrBase]
		a.lowN, a.critN = map[string]bool{}, map[string]bool{}
		a.checkAlerts()
	case id == idNotifyPopup:
		a.s.NotifyPopup = !a.s.NotifyPopup
		if !a.s.NotifyPopup && !a.s.NotifyWindows {
			a.s.NotifyWindows = true // keep at least one text notification
		}
	case id == idNotifyWin:
		a.s.NotifyWindows = !a.s.NotifyWindows
		if !a.s.NotifyPopup && !a.s.NotifyWindows {
			a.s.NotifyPopup = true
		}
	case id == idSoundOff:
		a.s.Sound = "none"
	case id == idSoundChime:
		a.s.Sound = "chime"
		playChime(alertInfo)
	case id == idSoundSpeak:
		a.s.Sound = "speak"
	case id == idNotifyFull:
		a.s.NotifyFull = !a.s.NotifyFull
	case id == idTestNotify:
		a.notify(alert{level: alertLow, kind: KindMouse, title: "Düşük pil: Mouse (deneme)",
			text:   "Mouse şarjı %15 seviyesine düştü. Lütfen şarja takın.",
			spoken: "Mouse pili yüzde 15. Lütfen şarja takın."})
	case id == idAutostart:
		setAutostart(!autostartEnabled())
	case id == idRefresh:
		select {
		case a.refresh <- struct{}{}:
		default:
		}
	case id == idDiag:
		shellOpen(logPath())
	case id == idAbout:
		a.about()
	case id == idExit:
		a.s.save()
		a.removeTray()
		pPostQuitMessage.Call(0)
		return
	}
	a.s.save()
	a.render(true)
}

// ---------------- about ----------------

func (a *App) about() {
	content := "Kablosuz kulaklık, mouse ve klavye pil göstergesi.\n\n" +
		"Sahibi: Volkan Sezer\n" +
		"Discord: <a href=\"https://discord.gg/nvqc84AHZf\">discord.gg/nvqc84AHZf</a>\n" +
		"Instagram: <a href=\"https://instagram.com/zetxtv\">@zetxtv</a>\n" +
		"E-posta: <a href=\"mailto:volkan.sezer@outlook.com.tr\">volkan.sezer@outlook.com.tr</a>\n\n" +
		"Desteklenenler: Logitech HID++ (Lightspeed, Unifying, Bolt alıcıları ve G serisi kulaklıklar), " +
		"tüm markaların Bluetooth / Bluetooth LE cihazları ve standart HID pil raporu gönderen USB alıcılar."
	if pTaskDialogIndirect.Find() == nil {
		title := u16(appName + " — Hakkında")
		main := u16(appName + "  v" + appVersion)
		body := u16(content)
		cb := syscall.NewCallback(func(hwnd, msg, wp, lp, ref uintptr) uintptr {
			if msg == 3 && lp != 0 { // TDN_HYPERLINK_CLICKED
				shellOpen(windows.UTF16PtrToString((*uint16)(unsafe.Pointer(lp))))
			}
			return 0
		})
		cfg := make([]byte, 160)
		le := binary.LittleEndian
		le.PutUint32(cfg[0:], 160)
		le.PutUint64(cfg[4:], uint64(a.hwnd))
		le.PutUint64(cfg[12:], uint64(a.hinst))
		le.PutUint32(cfg[20:], 0x0001|0x0008) // TDF_ENABLE_HYPERLINKS | TDF_ALLOW_DIALOG_CANCELLATION
		le.PutUint32(cfg[24:], 0x0020)        // close button
		le.PutUint64(cfg[28:], uint64(uintptr(unsafe.Pointer(title))))
		le.PutUint64(cfg[36:], 0xFFFD) // TD_INFORMATION_ICON
		le.PutUint64(cfg[44:], uint64(uintptr(unsafe.Pointer(main))))
		le.PutUint64(cfg[52:], uint64(uintptr(unsafe.Pointer(body))))
		le.PutUint64(cfg[140:], uint64(cb))
		r, _, _ := pTaskDialogIndirect.Call(uintptr(unsafe.Pointer(&cfg[0])), 0, 0, 0)
		runtime.KeepAlive(title)
		runtime.KeepAlive(main)
		runtime.KeepAlive(body)
		if r == 0 {
			return
		}
	}
	plain := strings.NewReplacer("<a href=\"https://discord.gg/nvqc84AHZf\">", "", "<a href=\"https://instagram.com/zetxtv\">", "", "<a href=\"mailto:volkan.sezer@outlook.com.tr\">", "", "</a>", "").Replace(content)
	msgBox(appName+" v"+appVersion+"\n\n"+plain, appName+" — Hakkında", MB_ICONINFORMATION)
}
