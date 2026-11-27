//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// "Çalışmayan cihazı bildir": collects what the app saw (shown devices and vendor receivers it
// could not read) and helps the user send it over Discord or e-mail.

const (
	discordURL   = "https://discord.gg/nvqc84AHZf"
	supportEmail = "volkan.sezer@outlook.com.tr"
)

var (
	unknownMu    sync.Mutex
	unknownRecvs []string

	pOpenClipboard    = user32.NewProc("OpenClipboard")
	pEmptyClipboard   = user32.NewProc("EmptyClipboard")
	pSetClipboardData = user32.NewProc("SetClipboardData")
	pCloseClipboard   = user32.NewProc("CloseClipboard")
	pGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	pGlobalLock       = kernel32.NewProc("GlobalLock")
	pGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
)

func setUnknownReceivers(list []string) {
	seen := map[string]bool{}
	var out []string
	for _, l := range list {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	unknownMu.Lock()
	unknownRecvs = out
	unknownMu.Unlock()
}

// buildReport returns a short, plain-text report without personal data.
func (a *App) buildReport() string {
	var b strings.Builder
	v := windows.RtlGetVersion()
	fmt.Fprintf(&b, "ZETX BATTERY v%s - çalışmayan cihaz bildirimi\n", appVersion)
	fmt.Fprintf(&b, "Windows %d.%d (derleme %d), %s\n\n", v.MajorVersion, v.MinorVersion, v.BuildNumber, time.Now().Format("02.01.2006 15:04"))
	b.WriteString("Çalışmayan cihaz(lar)ın marka ve modeli: [BURAYA YAZIN]\n")
	b.WriteString("Bağlantı şekli (2.4 GHz alıcı / Bluetooth / kablo): [BURAYA YAZIN]\n\n")
	a.mu.Lock()
	devs := append([]Device{}, a.all...)
	a.mu.Unlock()
	b.WriteString("Programın gördüğü cihazlar:\n")
	if len(devs) == 0 {
		b.WriteString("  (hiç cihaz bulunamadı)\n")
	}
	for _, d := range devs {
		pct := "--"
		if d.Percent >= 0 {
			pct = fmt.Sprintf("%%%d", d.Percent)
		}
		fmt.Fprintf(&b, "  %s | %s | %s | %s\n", d.Kind.Label(), d.Name, pct, d.Source)
	}
	unknownMu.Lock()
	unk := append([]string{}, unknownRecvs...)
	unknownMu.Unlock()
	b.WriteString("\nTanınmayan kablosuz alıcılar (VID:PID):\n")
	if len(unk) == 0 {
		b.WriteString("  (yok)\n")
	}
	for i, l := range unk {
		if i == 25 {
			fmt.Fprintf(&b, "  ... ve %d tane daha (tanilama.txt dosyasında)\n", len(unk)-25)
			break
		}
		b.WriteString("  " + l + "\n")
	}
	return b.String()
}

func setClipboard(hwnd uintptr, text string) bool {
	u, err := windows.UTF16FromString(text)
	if err != nil {
		return false
	}
	if r, _, _ := pOpenClipboard.Call(hwnd); r == 0 {
		return false
	}
	defer pCloseClipboard.Call()
	pEmptyClipboard.Call()
	size := uintptr(len(u) * 2)
	hMem, _, _ := pGlobalAlloc.Call(0x0002, size) // GMEM_MOVEABLE
	if hMem == 0 {
		return false
	}
	ptr, _, _ := pGlobalLock.Call(hMem)
	if ptr == 0 {
		return false
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(u)), u)
	pGlobalUnlock.Call(hMem)
	r, _, _ := pSetClipboardData.Call(13, hMem) // CF_UNICODETEXT
	return r != 0
}

// mailtoLink builds a mailto: URL with the report as body (kept short: mail clients cap the URL).
func mailtoLink(report string) string {
	body := report
	if len(body) > 1500 {
		body = body[:1500] + "\n...\n"
	}
	body += "\n(Daha ayrıntılı bilgi için tanilama.txt dosyasını bu e-postaya ekleyebilirsiniz.)\n"
	q := "subject=" + url.PathEscape("ZETX BATTERY - Çalışmayan cihaz") + "&body=" + url.PathEscape(body)
	return "mailto:" + supportEmail + "?" + q
}

// reportDialog shows the "Çalışmayan cihazı bildir" window.
func (a *App) reportDialog() {
	report := a.buildReport()
	const (
		btnDiscord = 101
		btnMail    = 102
		btnCopy    = 103
		btnLog     = 104
	)
	if pTaskDialogIndirect.Find() != nil {
		setClipboard(a.hwnd, report)
		msgBox("Rapor panoya kopyalandı.\n\nDiscord: "+discordURL+"\nE-posta: "+supportEmail+
			"\n\nDiscord sunucusuna veya e-postaya yapıştırıp cihazınızın marka ve modelini yazın.", appName+" — Çalışmayan cihazı bildir", MB_ICONINFORMATION)
		return
	}
	title := u16(appName + " — Çalışmayan cihazı bildir")
	main := u16("Cihazınız görünmüyor ya da yüzdesi yanlış mı?")
	body := u16("Cihazın marka ve modelini, programın hazırladığı kısa raporla birlikte bize gönderin; desteğini ekleyelim.\n\n" +
		"Rapor yalnızca Windows sürümünü ve kablosuz alıcıların model kodlarını (VID:PID) içerir, kişisel bilgi içermez.")
	footer := u16("Discord: <a href=\"" + discordURL + "\">discord.gg/nvqc84AHZf</a>    E-posta: <a href=\"mailto:" + supportEmail + "\">" + supportEmail + "</a>")
	labels := []struct {
		id   int32
		text *uint16
	}{
		{btnDiscord, u16("Discord'dan bildir\nRapor panoya kopyalanır ve ZETX Discord sunucusu açılır. Mesaj kutusuna yapıştırıp (Ctrl+V) cihaz modelinizi yazın.")},
		{btnMail, u16("E-posta ile bildir\n" + supportEmail + " adresine rapor hazır bir e-posta açılır. Cihaz modelinizi yazıp gönderin.")},
		{btnCopy, u16("Raporu panoya kopyala\nİstediğiniz yere kendiniz yapıştırın.")},
		{btnLog, u16("Ayrıntılı tanılama dosyasını aç\nE-postaya eklemek için tanilama.txt dosyası açılır.")},
	}
	// TASKDIALOG_BUTTON is packed: int id + pointer = 12 bytes.
	btns := make([]byte, 12*len(labels))
	for i, l := range labels {
		binary.LittleEndian.PutUint32(btns[i*12:], uint32(l.id))
		binary.LittleEndian.PutUint64(btns[i*12+4:], uint64(uintptr(unsafe.Pointer(l.text))))
	}
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
	le.PutUint32(cfg[20:], 0x0001|0x0008|0x0010) // hyperlinks | cancellation | command links
	le.PutUint32(cfg[24:], 0x0020)               // close button
	le.PutUint64(cfg[28:], uint64(uintptr(unsafe.Pointer(title))))
	le.PutUint64(cfg[36:], 0xFFFD) // TD_INFORMATION_ICON
	le.PutUint64(cfg[44:], uint64(uintptr(unsafe.Pointer(main))))
	le.PutUint64(cfg[52:], uint64(uintptr(unsafe.Pointer(body))))
	le.PutUint32(cfg[60:], uint32(len(labels)))
	le.PutUint64(cfg[64:], uint64(uintptr(unsafe.Pointer(&btns[0]))))
	le.PutUint64(cfg[132:], uint64(uintptr(unsafe.Pointer(footer))))
	le.PutUint64(cfg[140:], uint64(cb))
	var pressed int32
	r, _, _ := pTaskDialogIndirect.Call(uintptr(unsafe.Pointer(&cfg[0])), uintptr(unsafe.Pointer(&pressed)), 0, 0)
	runtime.KeepAlive(title)
	runtime.KeepAlive(main)
	runtime.KeepAlive(body)
	runtime.KeepAlive(footer)
	runtime.KeepAlive(labels)
	runtime.KeepAlive(btns)
	if r != 0 {
		return
	}
	switch pressed {
	case btnDiscord:
		setClipboard(a.hwnd, report)
		shellOpen(discordURL)
	case btnMail:
		shellOpen(mailtoLink(report))
	case btnCopy:
		if setClipboard(a.hwnd, report) {
			msgBox("Rapor panoya kopyalandı. Discord'a veya e-postaya yapıştırabilirsiniz (Ctrl+V).", appName, MB_ICONINFORMATION)
		}
	case btnLog:
		shellOpen(logPath())
	}
}
