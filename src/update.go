//go:build windows

package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Update check: asks GitHub which release is the latest. If it is newer than this build, the user
// gets a notification with the release notes and a download link. Nothing is downloaded or run
// automatically: the link opens in the browser and the user starts the setup.

const (
	repoAPI      = "https://api.github.com/repos/wolkansezer/ZETX-BATTERY/releases/latest"
	notesURL     = "https://raw.githubusercontent.com/wolkansezer/ZETX-BATTERY/main/guncelleme.json"
	downloadURL  = "https://github.com/wolkansezer/ZETX-BATTERY/releases/latest/download/ZETX_BATTERY_Kurulum.exe"
	releasesPage = "https://github.com/wolkansezer/ZETX-BATTERY/releases/latest"
	wmUpdate     = WM_APP + 4
)

// releaseNotes are this build's own notes, shown once after an update ("Yenilikler").
var releaseNotes = []string{
	"Görev çubuğuna tıklayınca göstergenin kısa süre kaybolması giderildi.",
	"Sağ tık menüsü yenilendi: Windows'un koyu/açık temasına uyar, göstergeye doğrudan sağ tıklanır.",
	"Yeni görünüm: \"Gizli simgelerde\" (saatin yanındaki ^ alanında her cihaz için ayrı simge).",
	"Oyun kolları ayrı tür olarak gösterilir; kapalı Bluetooth LE cihazları eski yüzdeyle görünmez.",
	"Bu sürümdeki değişiklikler Selahattintoy'un katkısıdır.",
}

type updateInfo struct {
	Version string
	Notes   []string
	Page    string
}

// newerVersion reports whether version a ("1.4.2" or "v1.4.2") is newer than b.
func newerVersion(a, b string) bool {
	pa, pb := verParts(a), verParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func verParts(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(v)), "v")
	var out [3]int
	for i, p := range strings.SplitN(v, ".", 3) {
		n := 0
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				break
			}
			n = n*10 + int(ch-'0')
		}
		out[i] = n
	}
	return out
}

func httpGet(url string) ([]byte, error) {
	c := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ZETX-BATTERY/"+appVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, io.ErrUnexpectedEOF
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512*1024))
}

// fetchUpdate returns the latest release if it is newer than this build.
func fetchUpdate() (*updateInfo, error) {
	body, err := httpGet(repoAPI)
	if err != nil {
		return nil, err
	}
	var rel struct {
		Tag        string `json:"tag_name"`
		Body       string `json:"body"`
		URL        string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, err
	}
	if rel.Draft || rel.Prerelease || !newerVersion(rel.Tag, appVersion) {
		return nil, nil
	}
	u := &updateInfo{Version: strings.TrimPrefix(rel.Tag, "v"), Page: rel.URL}
	if u.Page == "" {
		u.Page = releasesPage
	}
	// Notes: guncelleme.json in the repository (kept by the developer), else the release text.
	if nb, err := httpGet(notesURL); err == nil {
		var m map[string][]string
		if json.Unmarshal(nb, &m) == nil {
			u.Notes = m[u.Version]
		}
	}
	if len(u.Notes) == 0 {
		for _, l := range strings.Split(rel.Body, "\n") {
			l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "-*#> "))
			l = strings.ReplaceAll(l, "**", "")
			if l != "" && len(u.Notes) < 8 {
				u.Notes = append(u.Notes, l)
			}
		}
	}
	return u, nil
}

// updateLoop checks shortly after start and then every hour.
func (a *App) updateLoop() {
	time.Sleep(25 * time.Second)
	for {
		a.mu.Lock()
		off := a.s.NoUpdateCheck
		a.mu.Unlock()
		if !off {
			a.checkUpdateNow(false)
		}
		time.Sleep(time.Hour)
	}
}

// checkUpdateNow runs in a goroutine; manual=true also reports "up to date".
func (a *App) checkUpdateNow(manual bool) {
	u, err := fetchUpdate()
	if err != nil {
		logf("guncelleme denetimi basarisiz: %v", err)
	}
	a.mu.Lock()
	if u != nil {
		a.update = u
	}
	a.manualCheck = manual
	a.updateErr = err != nil
	a.mu.Unlock()
	pPostMessageW.Call(a.hwnd, wmUpdate, 0, 0)
}

// onUpdateMsg runs on the UI thread after a check.
func (a *App) onUpdateMsg() {
	a.mu.Lock()
	u, manual, failed := a.update, a.manualCheck, a.updateErr
	a.manualCheck = false
	a.mu.Unlock()
	if u == nil {
		if manual {
			if failed {
				msgBox("Güncelleme denetlenemedi. İnternet bağlantınızı kontrol edip tekrar deneyin.", appName, MB_ICONINFORMATION)
			} else {
				msgBox(appName+" güncel (v"+appVersion+").", appName, MB_ICONINFORMATION)
			}
		}
		return
	}
	if manual {
		a.updateDialog()
		return
	}
	if a.s.UpdateSeen == u.Version {
		return // already told about this version; the menu still offers it
	}
	a.s.UpdateSeen = u.Version
	a.s.save()
	text := "Tıklayın: yenilikleri görün ve indirin."
	if len(u.Notes) > 0 {
		text = u.Notes[0] + " Tıklayın: tüm yenilikler ve indirme bağlantısı."
	}
	a.pendingAction = a.updateDialog
	a.notify(alert{level: alertInfo, title: "Yeni sürüm: " + appName + " v" + u.Version, text: text,
		spoken: appName + " için yeni sürüm yayınlandı."})
}

// showWhatsNew tells the user once what changed after updating to this build.
func (a *App) showWhatsNew(firstRun bool) {
	prev := a.s.LastVersion
	a.s.LastVersion = appVersion
	a.s.save()
	// prev == "" on a computer that had a version before this setting existed (<= 1.4.1)
	if firstRun || prev == appVersion {
		return
	}
	a.pendingAction = a.whatsNewDialog
	a.notify(alert{level: alertInfo, title: appName + " v" + appVersion + " kuruldu",
		text: releaseNotes[0] + " Tıklayın: tüm yenilikler.", spoken: appName + " güncellendi."})
}

func bulletList(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("• " + l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (a *App) whatsNewDialog() {
	a.simpleDialog(appName+" — Yenilikler", appName+" v"+appVersion+" yenilikleri", bulletList(releaseNotes), nil)
}

func (a *App) updateDialog() {
	a.mu.Lock()
	u := a.update
	a.mu.Unlock()
	if u == nil {
		return
	}
	notes := "Yenilikler sürüm sayfasında."
	if len(u.Notes) > 0 {
		notes = "Yenilikler:\n" + bulletList(u.Notes)
	}
	content := "Kullandığınız sürüm: v" + appVersion + "\nYeni sürüm: v" + u.Version + "\n\n" + notes +
		"\n\nKurulum dosyasını indirip çalıştırın; ayarlarınız korunur."
	pressed := a.simpleDialog(appName+" — Güncelleme", appName+" v"+u.Version+" yayında", content, []dlgButton{
		{101, "Şimdi indir\nKurulum dosyası (ZETX_BATTERY_Kurulum.exe) tarayıcıda indirilir."},
		{102, "Sürüm sayfasını aç\nGitHub'da yenilikleri ve dosyaları görün."},
		{103, "Daha sonra"},
	})
	switch pressed {
	case 101:
		shellOpen(downloadURL)
	case 102:
		shellOpen(u.Page)
	}
}

type dlgButton struct {
	id   int32
	text string
}

// simpleDialog shows a TaskDialog with optional command-link buttons and returns the pressed id.
func (a *App) simpleDialog(title, mainText, content string, buttons []dlgButton) int32 {
	if pTaskDialogIndirect.Find() != nil {
		msgBox(mainText+"\n\n"+content, title, MB_ICONINFORMATION)
		return 0
	}
	t, m, c := u16(title), u16(mainText), u16(content)
	texts := make([]*uint16, len(buttons))
	var btns []byte
	if len(buttons) > 0 {
		btns = make([]byte, 12*len(buttons))
		for i, b := range buttons {
			texts[i] = u16(b.text)
			binary.LittleEndian.PutUint32(btns[i*12:], uint32(b.id))
			binary.LittleEndian.PutUint64(btns[i*12+4:], uint64(uintptr(unsafe.Pointer(texts[i]))))
		}
	}
	cb := syscall.NewCallback(func(hwnd, msg, wp, lp, ref uintptr) uintptr {
		if msg == 3 && lp != 0 {
			shellOpen(windows.UTF16PtrToString((*uint16)(unsafe.Pointer(lp))))
		}
		return 0
	})
	cfg := make([]byte, 160)
	le := binary.LittleEndian
	le.PutUint32(cfg[0:], 160)
	le.PutUint64(cfg[4:], uint64(a.hwnd))
	le.PutUint64(cfg[12:], uint64(a.hinst))
	flags := uint32(0x0001 | 0x0008)
	if len(buttons) > 0 {
		flags |= 0x0010 // command links
	}
	le.PutUint32(cfg[20:], flags)
	if len(buttons) == 0 {
		le.PutUint32(cfg[24:], 0x0001) // OK button
	}
	le.PutUint64(cfg[28:], uint64(uintptr(unsafe.Pointer(t))))
	le.PutUint64(cfg[36:], 0xFFFD)
	le.PutUint64(cfg[44:], uint64(uintptr(unsafe.Pointer(m))))
	le.PutUint64(cfg[52:], uint64(uintptr(unsafe.Pointer(c))))
	if len(buttons) > 0 {
		le.PutUint32(cfg[60:], uint32(len(buttons)))
		le.PutUint64(cfg[64:], uint64(uintptr(unsafe.Pointer(&btns[0]))))
	}
	le.PutUint64(cfg[140:], uint64(cb))
	var pressed int32
	pTaskDialogIndirect.Call(uintptr(unsafe.Pointer(&cfg[0])), uintptr(unsafe.Pointer(&pressed)), 0, 0)
	runtime.KeepAlive(t)
	runtime.KeepAlive(m)
	runtime.KeepAlive(c)
	runtime.KeepAlive(texts)
	runtime.KeepAlive(btns)
	return pressed
}

