//go:build windows

// ZETX BATTERY setup: installs the app for the current user (no admin rights needed),
// creates Start menu / desktop shortcuts, registers an uninstaller in "Apps & features"
// and optionally starts the app with Windows. Run with /uninstall to remove it.
package main

import (
	_ "embed"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

//go:embed payload/app.exe
var appExe []byte

//go:embed payload/app.ico
var appIco []byte

const (
	appName    = "ZETX BATTERY"
	appVersion = "1.2.1"
	exeName    = "ZETX BATTERY.exe"
	uninstName = "Kaldir.exe"
	publisher  = "Volkan Sezer (ZETX)"
	repoURL    = "https://github.com/wolkansezer/ZETX-BATTERY"
	uninstKey  = `Software\Microsoft\Windows\CurrentVersion\Uninstall\ZETXBattery`
	runKey     = `Software\Microsoft\Windows\CurrentVersion\Run`
	appKey     = `Software\ZETX BATTERY`
)

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")

	pMessageBoxW        = user32.NewProc("MessageBoxW")
	pShellExecuteW      = shell32.NewProc("ShellExecuteW")
	pTaskDialogIndirect = comctl32.NewProc("TaskDialogIndirect")
)

func init() { runtime.LockOSThread() }

func u16(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }

func msgBox(text string, flags uintptr) {
	pMessageBoxW.Call(0, uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16(appName+" Kurulum"))), flags)
}

func installDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", appName)
}

func main() {
	enableVisualStyles()
	for _, a := range os.Args[1:] {
		if strings.EqualFold(a, "/uninstall") || strings.EqualFold(a, "--uninstall") {
			if relaunchFromTemp() {
				return
			}
			uninstall()
			return
		}
	}
	install()
}

// ---------------- TaskDialog ----------------

type tdButton struct {
	id   int32
	text string
}

type tdOptions struct {
	title, main, content, verify, footer string
	verifyChecked                        bool
	buttons                              []tdButton
	common                               uint32
	icon                                 uint16 // 0xFFFD info, 0xFFFC shield, 0xFFFE error, 0xFFFF warning
	commandLinks                         bool
}

// taskDialog shows a Common Controls v6 TaskDialog. ok=false when TaskDialog is unavailable.
func taskDialog(o tdOptions) (button int32, verified bool, ok bool) {
	if pTaskDialogIndirect.Find() != nil {
		return 0, false, false
	}
	title, mainT, content := u16(o.title), u16(o.main), u16(o.content)
	var verify, footer *uint16
	if o.verify != "" {
		verify = u16(o.verify)
	}
	if o.footer != "" {
		footer = u16(o.footer)
	}
	btnBuf := make([]byte, 12*len(o.buttons)+1)
	keep := []*uint16{}
	for i, b := range o.buttons {
		t := u16(b.text)
		keep = append(keep, t)
		binary.LittleEndian.PutUint32(btnBuf[i*12:], uint32(b.id))
		binary.LittleEndian.PutUint64(btnBuf[i*12+4:], uint64(uintptr(unsafe.Pointer(t))))
	}
	cb := syscall.NewCallback(func(hwnd, msg, wp, lp, ref uintptr) uintptr {
		if msg == 3 && lp != 0 { // TDN_HYPERLINK_CLICKED
			pShellExecuteW.Call(0, uintptr(unsafe.Pointer(u16("open"))), lp, 0, 0, 1)
		}
		return 0
	})
	flags := uint32(0x0001 | 0x0008) // hyperlinks, allow cancel
	if o.verifyChecked {
		flags |= 0x0100
	}
	if o.commandLinks {
		flags |= 0x0010
	}
	cfg := make([]byte, 160) // TASKDIALOGCONFIG is packed(1); 160 bytes on x64
	le := binary.LittleEndian
	le.PutUint32(cfg[0:], 160)
	le.PutUint32(cfg[20:], flags)
	le.PutUint32(cfg[24:], o.common)
	le.PutUint64(cfg[28:], uint64(uintptr(unsafe.Pointer(title))))
	le.PutUint64(cfg[36:], uint64(o.icon))
	le.PutUint64(cfg[44:], uint64(uintptr(unsafe.Pointer(mainT))))
	le.PutUint64(cfg[52:], uint64(uintptr(unsafe.Pointer(content))))
	if len(o.buttons) > 0 {
		le.PutUint32(cfg[60:], uint32(len(o.buttons)))
		le.PutUint64(cfg[64:], uint64(uintptr(unsafe.Pointer(&btnBuf[0]))))
		le.PutUint32(cfg[72:], uint32(o.buttons[0].id))
	}
	if verify != nil {
		le.PutUint64(cfg[92:], uint64(uintptr(unsafe.Pointer(verify))))
	}
	if footer != nil {
		le.PutUint64(cfg[124:], uint64(0xFFFD))
		le.PutUint64(cfg[132:], uint64(uintptr(unsafe.Pointer(footer))))
	}
	le.PutUint64(cfg[140:], uint64(cb))
	var btn int32
	var ver int32
	r, _, _ := pTaskDialogIndirect.Call(uintptr(unsafe.Pointer(&cfg[0])), uintptr(unsafe.Pointer(&btn)), 0, uintptr(unsafe.Pointer(&ver)))
	runtime.KeepAlive(keep)
	runtime.KeepAlive(btnBuf)
	runtime.KeepAlive([]*uint16{title, mainT, content, verify, footer})
	if r != 0 {
		return 0, false, false
	}
	return btn, ver != 0, true
}

// ---------------- helpers ----------------

// stopApp asks a running ZETX BATTERY to close (WM_CLOSE), then ends it if it is still running.
func stopApp() {
	findWindow := user32.NewProc("FindWindowW")
	postMessage := user32.NewProc("PostMessageW")
	cls := u16("ZETXBatteryBar")
	if h, _, _ := findWindow.Call(uintptr(unsafe.Pointer(cls)), 0); h != 0 {
		postMessage.Call(h, 0x0010, 0, 0) // WM_CLOSE
		for i := 0; i < 30; i++ {
			time.Sleep(100 * time.Millisecond)
			if h, _, _ := findWindow.Call(uintptr(unsafe.Pointer(cls)), 0); h == 0 {
				break
			}
		}
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if strings.EqualFold(windows.UTF16ToString(pe.ExeFile[:]), exeName) {
			if p, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pe.ProcessID); err == nil {
				windows.TerminateProcess(p, 0)
				windows.WaitForSingleObject(p, 3000)
				windows.CloseHandle(p)
			}
		}
	}
	time.Sleep(300 * time.Millisecond)
}

func writeRetry(path string, data []byte) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = os.WriteFile(path, data, 0o755); err == nil {
			return nil
		}
		time.Sleep(400 * time.Millisecond)
	}
	return err
}

// ---------------- install ----------------

func install() {
	dir := installDir()
	btn, autostart, ok := taskDialog(tdOptions{
		title: appName + " Kurulum",
		main:  appName + " v" + appVersion + " kurulacak",
		content: "Kablosuz kulaklık, mouse ve klavyenin pil yüzdelerini görev çubuğunda gösterir.\n\n" +
			"Kurulum yeri: " + dir + "\nYönetici izni gerekmez. Başlat menüsüne ve masaüstüne kısayol eklenir.",
		verify:        "Windows açılınca otomatik başlat",
		verifyChecked: true,
		buttons:       []tdButton{{100, "Kur\nProgramı kur ve başlat"}},
		common:        0x0008, // Cancel
		icon:          0xFFFD,
		commandLinks:  true,
		footer:        "Açık kaynak (MIT): <a href=\"" + repoURL + "\">github.com/wolkansezer/ZETX-BATTERY</a>",
	})
	if !ok {
		autostart = true
		r, _, _ := pMessageBoxW.Call(0, uintptr(unsafe.Pointer(u16(appName+" v"+appVersion+" kurulsun mu?\n\nKurulum yeri: "+dir))),
			uintptr(unsafe.Pointer(u16(appName+" Kurulum"))), 0x4|0x40)
		if r != 6 {
			return
		}
		btn = 100
	}
	if btn != 100 {
		return
	}
	if err := doInstall(dir, autostart); err != nil {
		msgBox("Kurulum tamamlanamadı:\n\n"+err.Error()+"\n\nProgram açıksa kapatıp tekrar deneyin.", 0x10)
		return
	}
	exe := filepath.Join(dir, exeName)
	start := exec.Command(exe)
	start.Dir = dir
	start.Start()
	if _, _, ok := taskDialog(tdOptions{
		title:   appName + " Kurulum",
		main:    "Kurulum tamamlandı",
		content: appName + " kuruldu ve başlatıldı.\n\nPil yüzdeleri görev çubuğunda, saatin solunda görünür. Ayarlar için saatin yanındaki pil simgesine sağ tıklayın.\n\nKaldırmak için: Ayarlar → Uygulamalar → " + appName,
		common:  0x0020, // Close
		icon:    0xFFFD,
	}); !ok {
		msgBox(appName+" kuruldu ve başlatıldı.", 0x40)
	}
}

func doInstall(dir string, autostart bool) error {
	stopApp()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	exe := filepath.Join(dir, exeName)
	ico := filepath.Join(dir, "app.ico")
	if err := writeRetry(exe, appExe); err != nil {
		return fmt.Errorf("program dosyası yazılamadı: %w", err)
	}
	os.WriteFile(ico, appIco, 0o644)
	if self, err := os.Executable(); err == nil {
		if b, err := os.ReadFile(self); err == nil && !strings.EqualFold(filepath.Clean(self), filepath.Join(dir, uninstName)) {
			writeRetry(filepath.Join(dir, uninstName), b)
		}
	}
	shortcuts(true, exe, ico, dir)

	if k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstKey, registry.SET_VALUE); err == nil {
		k.SetStringValue("DisplayName", appName)
		k.SetStringValue("DisplayVersion", appVersion)
		k.SetStringValue("Publisher", publisher)
		k.SetStringValue("DisplayIcon", ico)
		k.SetStringValue("InstallLocation", dir)
		k.SetStringValue("URLInfoAbout", repoURL)
		k.SetStringValue("UninstallString", `"`+filepath.Join(dir, uninstName)+`" /uninstall`)
		k.SetDWordValue("NoModify", 1)
		k.SetDWordValue("NoRepair", 1)
		k.SetDWordValue("EstimatedSize", uint32((len(appExe)+len(appIco))/1024+2048))
		k.Close()
	}
	if k, _, err := registry.CreateKey(registry.CURRENT_USER, appKey, registry.SET_VALUE); err == nil {
		k.SetDWordValue("Installed", 1)
		k.SetStringValue("InstallDir", dir)
		k.Close()
	}
	if k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE); err == nil {
		if autostart {
			k.SetStringValue(appName, `"`+exe+`"`)
		} else {
			k.DeleteValue(appName)
		}
		k.Close()
	}
	return nil
}

// ---------------- uninstall ----------------

func uninstall() {
	dir := installDir()
	btn, wipe, ok := taskDialog(tdOptions{
		title:   appName + " Kaldır",
		main:    appName + " kaldırılsın mı?",
		content: "Program, kısayolları ve Windows ile başlatma ayarı silinecek.",
		verify:  "Ayarlarımı da sil",
		common:  0x0002 | 0x0004, // Yes | No
		icon:    0xFFFF,
	})
	if !ok {
		r, _, _ := pMessageBoxW.Call(0, uintptr(unsafe.Pointer(u16(appName+" kaldırılsın mı?"))), uintptr(unsafe.Pointer(u16(appName+" Kaldır"))), 0x4|0x30)
		if r != 6 {
			return
		}
		btn = 6
	}
	if btn != 6 { // IDYES
		return
	}
	stopApp()
	shortcuts(false, "", "", "")
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE); err == nil {
		k.DeleteValue(appName)
		k.Close()
	}
	registry.DeleteKey(registry.CURRENT_USER, uninstKey)
	registry.DeleteKey(registry.CURRENT_USER, appKey)
	if wipe {
		os.RemoveAll(filepath.Join(os.Getenv("APPDATA"), appName))
	}
	os.Remove(filepath.Join(dir, exeName))
	os.Remove(filepath.Join(dir, "app.ico"))
	os.Remove(filepath.Join(dir, uninstName))
	os.Remove(dir)
	msgBox(appName+" kaldırıldı.", 0x40)
}

// relaunchFromTemp: the uninstaller lives in the install folder and cannot delete itself while
// running, so it copies itself to %TEMP% and continues from there. Returns true if relaunched.
func relaunchFromTemp() bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	if !strings.EqualFold(filepath.Dir(filepath.Clean(self)), filepath.Clean(installDir())) {
		return false
	}
	tmp := filepath.Join(os.TempDir(), "ZETX_BATTERY_Kaldir.exe")
	b, err := os.ReadFile(self)
	if err != nil || os.WriteFile(tmp, b, 0o755) != nil {
		return false
	}
	if exec.Command(tmp, "/uninstall").Start() != nil {
		return false
	}
	return true
}

// ---------------- visual styles ----------------

func enableVisualStyles() {
	manifest := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
<dependency><dependentAssembly><assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/></dependentAssembly></dependency>
</assembly>`
	path := filepath.Join(os.TempDir(), "zetx-battery-setup-cc6.manifest")
	if os.WriteFile(path, []byte(manifest), 0o644) != nil {
		return
	}
	type actctx struct {
		cbSize                 uint32
		dwFlags                uint32
		lpSource               *uint16
		wProcessorArchitecture uint16
		wLangID                uint16
		lpAssemblyDirectory    *uint16
		lpResourceName         *uint16
		lpApplicationName      *uint16
		hModule                uintptr
	}
	ac := actctx{lpSource: u16(path)}
	ac.cbSize = uint32(unsafe.Sizeof(ac))
	h, _, _ := kernel32.NewProc("CreateActCtxW").Call(uintptr(unsafe.Pointer(&ac)))
	if h == 0 || h == ^uintptr(0) {
		return
	}
	var cookie uintptr
	kernel32.NewProc("ActivateActCtx").Call(h, uintptr(unsafe.Pointer(&cookie)))
}
