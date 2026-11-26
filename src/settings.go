//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/registry"
)

const appName = "ZETX BATTERY"
const appVersion = "1.1.0"

type CachedDev struct {
	Name    string    `json:"name"`
	Kind    Kind      `json:"kind"`
	Percent int       `json:"percent"`
	Seen    time.Time `json:"seen"`
}

type Settings struct {
	Mode       string               `json:"mode"` // "taskbar" | "desktop"
	X          int32                `json:"x"`
	Y          int32                `json:"y"`
	HasPos     bool                 `json:"hasPos"`
	ShowClock  bool                 `json:"showClock"`
	Threshold  int                  `json:"threshold"` // 0 = off
	NotifyFull bool                 `json:"notifyFull"`
	Interval   int                  `json:"interval"` // seconds
	Background bool                 `json:"background"`
	Size       float64              `json:"size"` // icon/text size multiplier
	Hidden     map[string]bool      `json:"hidden"`
	Cache      map[string]CachedDev `json:"cache"`
}

func defaultSettings() Settings {
	return Settings{Mode: "taskbar", Threshold: 20, NotifyFull: true, Interval: 30, Hidden: map[string]bool{}, Cache: map[string]CachedDev{}}
}

func settingsPath() string {
	dir := filepath.Join(os.Getenv("APPDATA"), "ZETX BATTERY")
	os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "settings.json")
}

func loadSettings() Settings {
	s := defaultSettings()
	if b, err := os.ReadFile(settingsPath()); err == nil {
		json.Unmarshal(b, &s)
	}
	if s.Hidden == nil {
		s.Hidden = map[string]bool{}
	}
	if s.Cache == nil {
		s.Cache = map[string]CachedDev{}
	}
	if s.Interval < 15 || s.Interval >= 60 {
		s.Interval = 30
	}
	if s.Size < 0.7 || s.Size > 2 {
		s.Size = 1
	}
	if s.Mode != "desktop" {
		s.Mode = "taskbar"
	}
	return s
}

func (s *Settings) save() {
	b, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(settingsPath(), b, 0o644)
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(appName)
	return err == nil
}

func setAutostart(on bool) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	if on {
		exe, _ := os.Executable()
		k.SetStringValue(appName, `"`+exe+`"`)
	} else {
		k.DeleteValue(appName)
	}
}

func lightTheme() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("SystemUsesLightTheme")
	return err == nil && v == 1
}

func installedBySetup() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\ZETX BATTERY`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetIntegerValue("Installed")
	return err == nil
}
