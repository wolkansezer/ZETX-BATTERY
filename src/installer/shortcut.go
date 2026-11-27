//go:build windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Shortcuts are created with the Windows Shell's own IShellLink COM object.

var (
	ole32             = windows.NewLazySystemDLL("ole32.dll")
	pCoCreateInstance = ole32.NewProc("CoCreateInstance")

	clsidShellLink  = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIShellLinkW  = windows.GUID{Data1: 0x000214F9, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIPersistFile = windows.GUID{Data1: 0x0000010B, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

func comCall(obj uintptr, index int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(index)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

func ptr(s string) uintptr { return uintptr(unsafe.Pointer(u16(s))) }

func createShortcut(lnk, target, dir, icon, desc string) error {
	windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
	var sl uintptr
	if hr, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, 1,
		uintptr(unsafe.Pointer(&iidIShellLinkW)), uintptr(unsafe.Pointer(&sl))); hr != 0 {
		return syscall.Errno(hr)
	}
	defer comCall(sl, 2)          // Release
	comCall(sl, 20, ptr(target))  // SetPath
	comCall(sl, 9, ptr(dir))      // SetWorkingDirectory
	comCall(sl, 17, ptr(icon), 0) // SetIconLocation
	comCall(sl, 7, ptr(desc))     // SetDescription
	var pf uintptr
	if hr := comCall(sl, 0, uintptr(unsafe.Pointer(&iidIPersistFile)), uintptr(unsafe.Pointer(&pf))); hr != 0 {
		return syscall.Errno(hr)
	}
	defer comCall(pf, 2)
	if hr := comCall(pf, 6, ptr(lnk), 1); hr != 0 { // IPersistFile::Save
		return syscall.Errno(hr)
	}
	return nil
}

func shortcutPaths() []string {
	var out []string
	for _, id := range []*windows.KNOWNFOLDERID{windows.FOLDERID_Programs, windows.FOLDERID_Desktop} {
		if p, err := windows.KnownFolderPath(id, 0); err == nil {
			out = append(out, filepath.Join(p, appName+".lnk"))
		}
	}
	return out
}

func shortcuts(create bool, exe, ico, dir string) error {
	var last error
	for _, lnk := range shortcutPaths() {
		if create {
			if err := createShortcut(lnk, exe, dir, ico, "Kablosuz cihaz pil göstergesi"); err != nil {
				last = err
			}
		} else {
			os.Remove(lnk)
		}
	}
	return last
}
