//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// Notifications that work regardless of Windows' notification settings:
//   - an on-screen popup drawn by the app itself (bottom-right, above the taskbar)
//   - an alert sound (built-in chime) or spoken text (Windows speech, Turkish voice if installed)
//   - optionally the classic Windows (tray) notification as well

type alertLevel int

const (
	alertLow alertLevel = iota
	alertCritical
	alertFull
	alertInfo
)

type alert struct {
	level  alertLevel
	kind   Kind
	title  string
	text   string
	spoken string
}

var (
	winmm      = syscall.NewLazyDLL("winmm.dll")
	pPlaySound = winmm.NewProc("PlaySoundW")
	soundBufs  = map[alertLevel][]byte{}
)

const (
	sndAsync     = 0x0001
	sndNoDefault = 0x0002
	sndMemory    = 0x0004
)

// notify sends an alert through every channel the user enabled.
func (a *App) notify(al alert) {
	logf("BILDIRIM: %s - %s", al.title, al.text)
	a.toastCur, a.balloonCur = a.pendingAction, a.pendingAction
	a.pendingAction = nil
	if a.s.NotifyPopup {
		a.showToast(al)
	}
	if a.s.NotifyWindows {
		flags := uint32(NIIF_INFO)
		if al.level == alertLow || al.level == alertCritical {
			flags = NIIF_WARNING
		}
		if !a.balloon(al.title, al.text, flags) && !a.s.NotifyPopup {
			a.showToast(al) // Windows refused the notification: fall back to our popup
		}
	}
	switch a.s.Sound {
	case "chime":
		playChime(al.level)
	case "speak":
		spoken := al.spoken
		if spoken == "" {
			spoken = al.title + ". " + al.text
		}
		go speak(spoken, al.level)
	}
}

// ---------------- sound ----------------

func playChime(level alertLevel) {
	buf, ok := soundBufs[level]
	if !ok {
		buf = makeChime(level)
		soundBufs[level] = buf
	}
	pPlaySound.Call(uintptr(unsafe.Pointer(&buf[0])), 0, sndMemory|sndAsync|sndNoDefault)
}

// makeChime synthesizes a short two/three-note chime as a WAV file in memory.
func makeChime(level alertLevel) []byte {
	const sr = 22050
	type note struct{ f, d float64 }
	var notes []note
	switch level {
	case alertLow:
		notes = []note{{988, 0.14}, {740, 0.26}}
	case alertCritical:
		notes = []note{{988, 0.12}, {740, 0.12}, {988, 0.12}, {740, 0.28}}
	case alertFull:
		notes = []note{{660, 0.12}, {880, 0.12}, {1175, 0.3}}
	default:
		notes = []note{{880, 0.12}, {1175, 0.22}}
	}
	var pcm []int16
	for _, n := range notes {
		cnt := int(n.d * sr)
		for i := 0; i < cnt; i++ {
			t := float64(i) / sr
			env := math.Min(1, t/0.008) * math.Exp(-t/(n.d*0.55))
			v := (math.Sin(2*math.Pi*n.f*t) + 0.35*math.Sin(2*math.Pi*n.f*2*t)) * env * 0.42
			pcm = append(pcm, int16(v*32767))
		}
	}
	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("RIFF")
	binary.Write(&b, le, uint32(36+len(pcm)*2))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, le, uint32(16))
	binary.Write(&b, le, uint16(1))
	binary.Write(&b, le, uint16(1))
	binary.Write(&b, le, uint32(sr))
	binary.Write(&b, le, uint32(sr*2))
	binary.Write(&b, le, uint16(2))
	binary.Write(&b, le, uint16(16))
	b.WriteString("data")
	binary.Write(&b, le, uint32(len(pcm)*2))
	binary.Write(&b, le, pcm)
	return b.Bytes()
}

// speak reads the text aloud with a Turkish Windows voice; without one it plays the chime
// instead (other voices would mispronounce Turkish).
func speak(text string, level alertLevel) {
	if err := sapiSpeak(text); err != nil {
		logf("sesli okuma yapilamadi: %v", err)
		playChime(level)
	}
}

// ---------------- popup ----------------

const (
	toastTimer    = 2
	toastShowSecs = 7
)

func (a *App) createToastWindow() {
	cls := u16("ZETXBatteryToast")
	wc := WNDCLASSEXW{CbSize: uint32(unsafe.Sizeof(WNDCLASSEXW{})), LpfnWndProc: syscall.NewCallback(wndProc), HInstance: a.hinst, LpszClassName: cls}
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	ex := uintptr(WS_EX_LAYERED | WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE)
	a.toastHwnd, _, _ = pCreateWindowExW.Call(ex, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(u16("ZETX BATTERY Bildirim"))),
		WS_POPUP, 0, 0, 10, 10, 0, 0, a.hinst, 0)
}

func (a *App) showToast(al alert) {
	if a.toastHwnd == 0 {
		a.createToastWindow()
	}
	a.toastImg = a.renderToast(al)
	a.toastStart = time.Now()
	a.toastAlpha = 0
	pShowWindow.Call(a.toastHwnd, SW_SHOWNOACTIVATE)
	pSetTimer.Call(a.toastHwnd, toastTimer, 30, 0)
	a.toastStep()
}

func (a *App) hideToast() {
	pShowWindow.Call(a.toastHwnd, SW_HIDE)
	user32.NewProc("KillTimer").Call(a.toastHwnd, toastTimer)
	a.toastImg = nil
}

// toastStep animates fade in, hold and fade out.
func (a *App) toastStep() {
	if a.toastImg == nil {
		return
	}
	el := time.Since(a.toastStart).Seconds()
	alpha := 1.0
	switch {
	case el < 0.25:
		alpha = el / 0.25
	case el > toastShowSecs:
		alpha = 1 - (el-toastShowSecs)/0.4
	}
	if alpha <= 0 {
		a.hideToast()
		return
	}
	img := a.toastImg
	w, h := int32(img.Rect.Dx()), int32(img.Rect.Dy())
	mon, _, _ := pMonitorFromPoint.Call(0, MONITOR_DEFAULTTONEAREST)
	mi := MONITORINFO{CbSize: uint32(unsafe.Sizeof(MONITORINFO{}))}
	pGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
	s := a.dpiScale()
	slide := int32((1 - math.Min(1, el/0.25)) * 24 * s)
	x := mi.RcWork.Right - w - int32(16*s)
	y := mi.RcWork.Bottom - h - int32(16*s) + slide
	a.presentOn(a.toastHwnd, img, x, y, byte(alpha*255))
}

func (a *App) renderToast(al alert) *image.RGBA {
	return renderToastImg(a.fs, a.light, a.dpiScale(), al)
}

func renderToastImg(fs *fontSet, light bool, s float64, al alert) *image.RGBA {
	W, H := int(420*s), int(124*s)
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	bg, fg, sub := color.NRGBA{30, 31, 36, 248}, color.NRGBA{255, 255, 255, 255}, color.NRGBA{185, 190, 200, 255}
	if light {
		bg, fg, sub = color.NRGBA{250, 250, 250, 250}, color.NRGBA{20, 20, 20, 255}, color.NRGBA{80, 85, 95, 255}
	}
	accent := color.NRGBA{232, 72, 60, 255}
	switch al.level {
	case alertFull:
		accent = color.NRGBA{60, 200, 110, 255}
	case alertInfo:
		accent = color.NRGBA{63, 224, 208, 255}
	}
	fillRoundRect(img, bg, 12*s)
	// accent strip
	draw.Draw(img, image.Rect(0, int(14*s), int(5*s), H-int(14*s)), image.NewUniform(accent), image.Point{}, draw.Over)
	// icon badge
	cx, cy, r := 50*s, float64(H)/2, 28*s
	for y := int(cy - r); y <= int(cy+r); y++ {
		for x := int(cx - r); x <= int(cx+r); x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			if k := math.Max(0, math.Min(1, r-d+0.5)); k > 0 && x >= 0 && y >= 0 && x < W && y < H {
				c := accent
				c.A = uint8(255 * k)
				draw.Draw(img, image.Rect(x, y, x+1, y+1), image.NewUniform(c), image.Point{}, draw.Over)
			}
		}
	}
	white := color.NRGBA{255, 255, 255, 255}
	text := func(face font.Face, c color.NRGBA, x, y float64, str string) {
		d := font.Drawer{Dst: img, Src: image.NewUniform(c), Face: face, Dot: fixed.Point26_6{X: fx(x), Y: fx(y)}}
		d.DrawString(str)
	}
	if fs.hasIcons {
		g := string(kindGlyph(al.kind))
		if al.level == alertInfo {
			g = string(glyphBolt)
		}
		iface := fs.face(fs.icon, "ti", 26*s)
		b, adv := font.BoundString(iface, g)
		gx := cx - float64(adv)/128
		gy := cy - float64(b.Min.Y+b.Max.Y)/128
		text(iface, white, gx, gy, g)
	}
	small := fs.face(fs.text, "ts", 12*s)
	titleF := fs.face(fs.bold, "tt", 17*s)
	body := fs.face(fs.text, "tb", 14*s)
	left := 96 * s
	text(small, sub, left, 26*s, "ZETX BATTERY")
	text(titleF, fg, left, 50*s, al.title)
	maxW := float64(W) - left - 16*s
	lines := wrapText(body, al.text, maxW)
	if len(lines) > 3 {
		lines = lines[:3]
	}
	for i, l := range lines {
		text(body, sub, left, (74+float64(i)*19)*s, l)
	}
	return img
}

func wrapText(face font.Face, s string, maxW float64) []string {
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		try := w
		if cur != "" {
			try = cur + " " + w
		}
		if measure(face, try) > maxW && cur != "" {
			lines = append(lines, cur)
			cur = w
		} else {
			cur = try
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
