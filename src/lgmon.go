//go:build windows

package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// lgMonitor keeps Logitech HID++ connections open so battery level and charging state
// can be re-read every couple of seconds without re-discovering devices.

type lgSlot struct {
	idx     byte
	name    string
	kind    Kind
	online  bool
	asleep  bool // paired but not answering (sleep / turned off)
	pct     int
	chg     bool
	hasBatt bool
	fails   int
	src     string
	smooth  float64 // smoothed percentage for voltage-based readings
}

type lgSession struct {
	lastAir time.Time // last over-the-air read of a Bluetooth session
	g      *lgGroup
	d      *hidppDev
	direct bool
	slots  map[byte]*lgSlot
}

type lgMonitor struct {
	sessions map[string]*lgSession
}

func newLgMonitor() *lgMonitor { return &lgMonitor{sessions: map[string]*lgSession{}} }

func lgGroups(hids []hidInfo) map[string]*lgGroup {
	groups := map[string]*lgGroup{}
	for _, h := range hids {
		if h.VID != 0x046D || h.UsagePage < 0xFF00 || (h.OutLen != 7 && h.OutLen != 20) {
			continue
		}
		k := groupKey(h.Path)
		if h.Container != (windowsGUID{}) && h.Container != nullContainer {
			k = h.Container.String() + "|" + miRe.FindString(strings.ToLower(h.Path))
		}
		logf("  HID 046D:%04X sayfa=%04X kullanim=%04X giris=%d cikis=%d grup=%s  %s", h.PID, h.UsagePage, h.Usage, h.InLen, h.OutLen, k, h.Product)
		g := groups[k]
		if g == nil {
			g = &lgGroup{key: k, product: h.Product, page: h.UsagePage, containerKey: h.Container.String()}
			groups[k] = g
		}
		if h.BT {
			g.bt = true
		}
		if h.OutLen == 7 {
			g.shortPath, g.sIn, g.sOut = h.Path, h.InLen, h.OutLen
		} else {
			g.longPath, g.lIn, g.lOut = h.Path, h.InLen, h.OutLen
		}
	}
	return groups
}

// refresh syncs open sessions with the currently present HID interfaces and re-discovers slots.
func (m *lgMonitor) refresh(hids []hidInfo) {
	groups := lgGroups(hids)
	for k, s := range m.sessions {
		g, ok := groups[k]
		if !ok || g.shortPath != s.g.shortPath || g.longPath != s.g.longPath {
			s.d.t.Close()
			delete(m.sessions, k)
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := m.sessions[k]
		if s == nil {
			g := groups[k]
			t, err := newTransport(g.shortPath, g.sIn, g.sOut, g.longPath, g.lIn, g.lOut)
			if err != nil {
				logf("  logitech grup %s acilamadi: %v", g.product, err)
				continue
			}
			s = &lgSession{g: g, d: &hidppDev{t: t, featIdx: map[[2]uint16]byte{}, product: g.product, vendorPg: g.page}, slots: map[byte]*lgSlot{}}
			m.sessions[k] = s
			s.discover()
			s.lastAir = time.Now()
		} else if !s.g.bt || time.Since(s.lastAir) >= btEvery {
			// re-check slots that are not online (new pairing, woke up)
			s.discoverOffline()
			if s.g.bt {
				s.lastAir = time.Now()
			}
		}
	}
}

func (s *lgSession) discover() {
	d := s.d
	d.timeout = 1500 * time.Millisecond
	v, err := d.ping(0xFF)
	logf("  logitech grup %s  kisa=%v uzun=%v  ping(0xFF): v=%d err=%v", s.g.product, s.g.shortPath != "", s.g.longPath != "", v, err)
	if err == nil && v >= 2 {
		s.direct = true
		sl := &lgSlot{idx: 0xFF}
		s.slots[0xFF] = sl
		s.readSlot(sl, true)
		return
	}
	for idx := byte(1); idx <= 6; idx++ {
		s.probeSlot(idx)
	}
}

func (s *lgSession) discoverOffline() {
	s.d.timeout = 1500 * time.Millisecond
	if s.direct {
		if sl := s.slots[0xFF]; sl != nil && !sl.online {
			s.readSlot(sl, true)
		}
		return
	}
	for idx := byte(1); idx <= 6; idx++ {
		if sl := s.slots[idx]; sl != nil && sl.online {
			continue
		}
		s.probeSlot(idx)
	}
}

// probeSlot pings a receiver slot and records whether a device is online, asleep or absent.
func (s *lgSession) probeSlot(idx byte) {
	d := s.d
	v, err := d.ping(idx)
	logf("    slot %d: v=%d err=%v", idx, v, err)
	sl := s.slots[idx]
	if err == nil && v >= 2 {
		if sl == nil {
			sl = &lgSlot{idx: idx}
			s.slots[idx] = sl
		}
		if !sl.online {
			// a different device may now use this slot: forget cached feature indexes
			for k := range d.featIdx {
				if k[0] == uint16(idx) {
					delete(d.featIdx, k)
				}
			}
			sl.name = ""
		}
		s.readSlot(sl, true)
		return
	}
	if e, ok := err.(hidppErr); ok && (e.code == 0x09 || e.code == 0x04) {
		if sl == nil {
			sl = &lgSlot{idx: idx, pct: -1}
			s.slots[idx] = sl
		}
		sl.online, sl.asleep = false, true
		if sl.name == "" {
			sl.name, sl.kind = s.pairingInfo(idx)
		}
		logf("    slot %d uykuda: %s (%s)", idx, sl.name, sl.kind.Label())
		return
	}
	if e, ok := err.(hidppErr); ok && e.code == 0x08 { // not paired
		delete(s.slots, idx)
	}
}

func (s *lgSession) pairingInfo(idx byte) (string, Kind) {
	d := s.d
	name, kind := "Logitech kablosuz cihaz", KindOther
	if r, ok := d.readLongReg(0xB5, 0x20+idx-1); ok && len(r) >= 8 {
		switch r[7] & 0x0F {
		case 1, 3:
			kind = KindKeyboard
		case 2, 8, 9:
			kind = KindMouse
		}
	}
	if r, ok := d.readLongReg(0xB5, 0x40+idx-1); ok && len(r) >= 3 {
		n := int(r[1])
		if n > len(r)-2 {
			n = len(r) - 2
		}
		if nm := strings.TrimSpace(strings.TrimRight(string(r[2:2+n]), "\x00")); nm != "" {
			name = "Logitech " + nm
		}
	}
	return name, kind
}

// readSlot reads battery (and name/kind when unknown). Returns false when the device stopped answering.
// A single failed read never hides the device; it is marked asleep only after several misses in a row.
func (s *lgSession) readSlot(sl *lgSlot, withName bool) bool {
	b, ok := s.d.batteryRead(sl.idx)
	if !ok {
		sl.fails++
		if sl.fails >= 4 || !sl.hasBatt {
			sl.online = false
			sl.asleep = !s.direct
		}
		return false
	}
	sl.fails = 0
	sl.online, sl.asleep, sl.hasBatt = true, false, true
	pct := b.pct
	if b.voltage {
		// Voltage jumps with load (sound, RGB). Smooth it, and restart smoothing when charging state flips.
		if sl.smooth <= 0 || b.charging != sl.chg || sl.src != b.src[:4] {
			sl.smooth = float64(pct)
		} else {
			sl.smooth = sl.smooth*0.75 + float64(pct)*0.25
		}
		pct = int(sl.smooth + 0.5)
	} else {
		sl.smooth = 0
	}
	if sl.chg != b.charging || sl.pct != pct {
		logf("    %s [%s ham=% X] -> %%%d sarj=%v", sl.name, b.src, b.raw, pct, b.charging)
	}
	sl.pct, sl.chg = clampPct(pct), b.charging
	if len(b.src) >= 4 {
		sl.src = b.src[:4]
	}
	if withName && (sl.name == "" || sl.kind == KindOther) {
		sl.name, sl.kind = s.d.nameAndKind(sl.idx)
		s.logFeatures(sl)
	}
	return true
}

// logFeatures writes every battery feature's raw answer to the diagnostics report.
func (s *lgSession) logFeatures(sl *lgSlot) {
	d := s.d
	for _, f := range []uint16{0x1004, 0x1001, 0x1F20, 0x1000} {
		fi := d.feature(sl.idx, f)
		if fi == 0 {
			continue
		}
		fn := byte(0)
		if f == 0x1004 {
			fn = 1
		}
		if r, err := d.request(sl.idx, fi, fn, nil, d.to()); err == nil {
			logf("    %s ozellik %04X ham=% X", sl.name, f, r[:6])
		} else {
			logf("    %s ozellik %04X hata=%v", sl.name, f, err)
		}
	}
}

// poll quickly re-reads every known device. Called every ~2 seconds.
func (m *lgMonitor) poll() {
	for _, s := range m.sessions {
		// A Bluetooth Logitech device shares its radio link with the mouse movement reports:
		// asking it every 2 s lowers its polling rate, so it is read only every few minutes.
		if s.g.bt {
			if time.Since(s.lastAir) < btEvery {
				continue
			}
			s.lastAir = time.Now()
		}
		s.d.timeout = 700 * time.Millisecond
		for _, sl := range s.slots {
			if sl.online {
				s.readSlot(sl, false)
			} else if !s.direct {
				// asleep: a quick ping tells us when it wakes up
				if v, err := s.d.ping(sl.idx); err == nil && v >= 2 {
					s.readSlot(sl, true)
				}
			}
		}
	}
}

func (m *lgMonitor) devices() (devs []Device, containers map[string]bool) {
	containers = map[string]bool{}
	for k, s := range m.sessions {
		for _, sl := range s.slots {
			if !sl.hasBatt && !sl.asleep {
				continue
			}
			containers[s.g.containerKey] = true
			d := Device{Key: fmt.Sprintf("lg:%s:%d", k, sl.idx), Name: sl.name, Kind: sl.kind, Percent: sl.pct, Charging: sl.chg && sl.online, Online: sl.online, Source: "Logitech HID++"}
			if !sl.online {
				d.Percent, d.Source = -1, "uykuda / kapalı"
			}
			devs = append(devs, d)
		}
	}
	return
}

func (m *lgMonitor) close() {
	for k, s := range m.sessions {
		s.d.t.Close()
		delete(m.sessions, k)
	}
}
