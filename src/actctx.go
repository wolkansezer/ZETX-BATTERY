//go:build windows

package main

import (
	"os"
	"path/filepath"
	"unsafe"
)

// enableVisualStyles activates a Common Controls v6 manifest at runtime so that modern dialogs
// (TaskDialog with links) are available without an embedded resource.
func enableVisualStyles() {
	manifest := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
<dependency><dependentAssembly><assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/></dependentAssembly></dependency>
</assembly>`
	path := filepath.Join(os.TempDir(), "zetx-battery-cc6.manifest")
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
	create := kernel32.NewProc("CreateActCtxW")
	activate := kernel32.NewProc("ActivateActCtx")
	h, _, _ := create.Call(uintptr(unsafe.Pointer(&ac)))
	if h == 0 || h == ^uintptr(0) {
		return
	}
	var cookie uintptr
	activate.Call(h, uintptr(unsafe.Pointer(&cookie)))
}
