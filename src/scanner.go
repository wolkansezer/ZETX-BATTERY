//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	logMu  sync.Mutex
	logBuf strings.Builder
)

func logf(format string, a ...any) {
	logMu.Lock()
	fmt.Fprintf(&logBuf, format+"\r\n", a...)
	logMu.Unlock()
}

func logPath() string { return filepath.Join(filepath.Dir(settingsPath()), "tanilama.txt") }

// flushLog appends to the report and keeps only the last ~200 KB.
func flushLog() {
	logMu.Lock()
	defer logMu.Unlock()
	old, _ := os.ReadFile(logPath())
	data := append(old, []byte(logBuf.String())...)
	if len(data) > 200<<10 {
		data = data[len(data)-150<<10:]
	}
	os.WriteFile(logPath(), data, 0o644)
	logBuf.Reset()
}

type Kind int

const (
	KindHeadset Kind = iota
	KindMouse
	KindKeyboard
	KindOther
)

func (k Kind) Label() string {
	switch k {
	case KindHeadset:
		return "Kulaklık"
	case KindMouse:
		return "Mouse"
	case KindKeyboard:
		return "Klavye"
	}
	return "Cihaz"
}

type Device struct {
	Key      string
	Name     string
	Kind     Kind
	Percent  int
	Charging bool
	Online   bool
	Source   string
	Index    int // subscript number when several devices of the same kind exist
}

// Battery sources other than Logitech HID++ are read in FullScan below.
// FullScan refreshes the Logitech sessions and reads every other (non-HID++) source.
func FullScan(mon *lgMonitor, ss *ssMonitor) (other []Device) {
	logf("ZETX BATTERY %s tarama: %s", appVersion, time.Now().Format("02.01.2006 15:04:05"))
	defer flushLog()
	hids := enumHID()
	conts := enumContainers()
	logf("HID koleksiyon sayisi: %d, cihaz grubu: %d", len(hids), len(conts))

	mon.refresh(hids)
	lg, lgConts := mon.devices()
	ss.refresh(hids)
	ss.poll(true)
	ssDevs, ssConts := ss.devices()
	lg = append(lg, ssDevs...)
	var devs []Device
	used := map[string]bool{}
	for k := range lgConts {
		used[k] = true
	}
	for k := range ssConts {
		used[k] = true
	}

	addFromContainer := func(cid string, pct int, src, extraName string) {
		if used[cid] {
			return
		}
		name := extraName
		kind := kindFromName(extraName)
		for id, c := range conts {
			if id.String() == cid {
				if n := c.bestName(); n != "" {
					name = n
				}
				kind = c.kind(extraName)
				break
			}
		}
		if name == "" {
			name = "Kablosuz cihaz"
		}
		used[cid] = true
		devs = append(devs, Device{Key: "c:" + strings.ToLower(cid), Name: name, Kind: kind, Percent: clampPct(pct), Online: true, Source: src})
		logf("  %s pil: %s %%%d", src, name, pct)
	}

	// 1) Battery level Windows already keeps for Bluetooth devices (no radio traffic at all).
	for id, c := range conts {
		if c.BtBattery >= 0 {
			addFromContainer(id.String(), c.BtBattery, "Bluetooth", "")
		}
	}
	// 2) Bluetooth LE GATT battery service, read from Windows' cache (see bt.go).
	for _, it := range enumInterfaces(batteryServiceGUID) {
		if used[it.Container.String()] {
			continue
		}
		if pct, ok := readGattBattery(it.Path); ok {
			addFromContainer(it.Container.String(), pct, "Bluetooth LE", "")
		}
	}
	// 3) Standard HID "Battery Strength" from any USB dongle / device.
	for _, h := range hids {
		if (h.Battery == nil && h.FeatBatt == nil) || used[h.Container.String()] {
			continue
		}
		if pct, ok := readHIDBattery(h); ok {
			addFromContainer(h.Container.String(), pct, "HID", h.Product)
		}
	}

	// 4) Connected wireless (Bluetooth) keyboards / mice / headsets that do not report battery.
	for id, c := range conts {
		cid := id.String()
		if used[cid] || !c.HasBT || !(c.Kbd || c.Mouse || c.Audio) {
			continue
		}
		k := c.kind("")
		if k == KindOther {
			continue
		}
		used[cid] = true
		devs = append(devs, Device{Key: "c:" + strings.ToLower(cid), Name: c.bestName(), Kind: k, Percent: -1, Online: true, Source: "pil bilgisi vermiyor"})
	}
	for _, d := range append(append([]Device{}, lg...), devs...) {
		logf("CIHAZ: %s | %s | %%%d | sarj=%v | %s", d.Kind.Label(), d.Name, d.Percent, d.Charging, d.Source)
	}
	return devs
}

func sortDevices(devs []Device) {
	sort.SliceStable(devs, func(i, j int) bool {
		if devs[i].Kind != devs[j].Kind {
			return devs[i].Kind < devs[j].Kind
		}
		return devs[i].Key < devs[j].Key
	})
}
