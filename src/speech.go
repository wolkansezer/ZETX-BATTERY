//go:build windows

package main

import (
	"errors"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Text-to-speech through the Windows Speech API (SAPI) COM objects, using a Turkish voice.

var (
	ole32             = windows.NewLazySystemDLL("ole32.dll")
	pCoCreateInstance = ole32.NewProc("CoCreateInstance")

	clsidSpVoice       = mustGUID("{96749377-3391-11D2-9EE3-00C04F797396}")
	iidISpVoice        = mustGUID("{6C44DF74-72B9-4992-A1EC-EF996E0422D4}")
	clsidSpObjectToken = mustGUID("{EF411752-3736-4CB4-9C8C-8EF4CCB58EFE}")
	iidISpObjectToken  = mustGUID("{14056589-E16C-11D2-BB90-00C04F8EE6C0}")

	errNoTurkishVoice = errors.New("Türkçe konuşma sesi yüklü değil")
)

func comCall(obj uintptr, index int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(index)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

// turkishVoiceTokens returns registry token IDs of installed Turkish (LCID 041F) voices.
func turkishVoiceTokens() []string {
	var out []string
	for _, base := range []string{`SOFTWARE\Microsoft\Speech\Voices\Tokens`, `SOFTWARE\Microsoft\Speech_OneCore\Voices\Tokens`} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, base, registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		names, _ := k.ReadSubKeyNames(-1)
		k.Close()
		for _, n := range names {
			ak, err := registry.OpenKey(registry.LOCAL_MACHINE, base+`\`+n+`\Attributes`, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			lang, _, _ := ak.GetStringValue("Language")
			ak.Close()
			for _, l := range strings.Split(strings.ToLower(lang), ";") {
				if strings.TrimLeft(strings.TrimSpace(l), "0") == "41f" {
					out = append(out, `HKEY_LOCAL_MACHINE\`+base+`\`+n)
				}
			}
		}
	}
	return out
}

func sapiSpeak(text string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
	defer windows.CoUninitialize()

	tokens := turkishVoiceTokens()
	if len(tokens) == 0 {
		return errNoTurkishVoice
	}
	var voice uintptr
	if hr, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidSpVoice)), 0, 0x17,
		uintptr(unsafe.Pointer(&iidISpVoice)), uintptr(unsafe.Pointer(&voice))); hr != 0 {
		return syscall.Errno(hr)
	}
	defer comCall(voice, 2)
	selected := false
	for _, id := range tokens {
		var tok uintptr
		if hr, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidSpObjectToken)), 0, 0x17,
			uintptr(unsafe.Pointer(&iidISpObjectToken)), uintptr(unsafe.Pointer(&tok))); hr != 0 {
			continue
		}
		ok := comCall(tok, 15, 0, uintptr(unsafe.Pointer(u16(id))), 0) == 0 && // ISpObjectToken::SetId
			comCall(voice, 18, tok) == 0 // ISpVoice::SetVoice
		comCall(tok, 2)
		if ok {
			selected = true
			break
		}
	}
	if !selected {
		return errNoTurkishVoice
	}
	if hr := comCall(voice, 20, uintptr(unsafe.Pointer(u16(text))), 0, 0); hr != 0 { // ISpVoice::Speak (synchronous)
		return syscall.Errno(hr)
	}
	return nil
}
