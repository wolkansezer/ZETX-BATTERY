//go:build windows

package main

import (
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// More 2.4 GHz receiver protocols. All are documented by open-source projects:
//
//   - Compx / Nordic family (Pulsar, ATK, VXE, VGN, Kysona, LAMZU, G-Wolves Compx and many
//     other PAW3395/3950 mice): Linux hid-kysona driver (mainline) and the "HID: pulsar" driver,
//     protocol from andrewrabert/python-pulsar-mouse-tool. 17-byte output report 0x08,
//     command 0x04 = power details; reply: level [6], on-cable [7], millivolts [8..9] (big-endian),
//     checksum [16] = 0x55 - sum(bytes 0..15).
//   - Sinowealth family (Glorious Model O/D Wireless, WLmouse, G-Wolves 8K): feature report 0
//     "00 00 02 02 00 83" (65 bytes), answered by "a1 00 02 02 00 83 <charging> <level>";
//     status a4 = asleep. OpenRGB SinowealthGMOWController, korkje/mow, HaloBattery.
//   - ASUS ROG / TUF wireless mice: G-Helper (seerge/g-helper) AsusMouse.cs - output report
//     "00 12 07" (65 bytes; "03 12 07" on the ROG OMNI receiver), reply echoes 12 07 with the
//     battery at +4 and charging at +9; "ff aa" = unknown command.

const (
	vidCompx      = 0x3554 // Compx: Pulsar, VXE, Kysona and others
	vidATK        = 0x373B // ATK / VXE
	vidLamzu      = 0x373E // LAMZU
	vidSinowealth = 0x258A // Glorious and other Sinowealth based mice
	vidWLmouse    = 0x36A7
	vidASUS       = 0x0B05
	vidAreson     = 0x25A7 // Fantech and other Areson based mice
	vidDareu      = 0x260D
	vidDragonfly  = 0x391D // VGN Dragonfly 3 (Compx reference design)
	vidZowie      = 0x04A5 // BenQ ZOWIE
	vidGravion    = 0xA8A6 // Gravion keyboards ("WirelessDevice" receiver)
)

// compxKbdReceiver: the shared keyboard receivers that speak the report 0x13 protocol.
func compxKbdReceiver(vid, pid uint16) bool {
	return (vid == vidCompx && pid == 0xFA09) || (vid == 0x41E4 && pid == 0x2001)
}

// compxFeatVendor: mice that take the Compx frame as a feature report and answer with input
// report 0x09 (Fantech Helios Go XD5 / Aria XD7, DAREU, Endgame Gear 4K).
func compxFeatVendor(vid uint16) bool {
	return vid == vidAreson || vid == vidDareu
}

func compxVendor(vid uint16) bool {
	return vid == vidCompx || vid == vidATK || vid == vidLamzu || vid == vidDragonfly
}

func sinowealthVendor(vid uint16) bool {
	return vid == vidSinowealth || vid == vidWLmouse
}

// lookupModel2 handles the protocol families above. Unknown brands whose receiver has the Compx
// shape (vendor collection with 17-byte input and output reports) are tried too: many unbranded
// 2.4 GHz mice use the same chips.
func lookupModel2(h hidInfo) (ssModel, bool) {
	name := strings.TrimSpace(h.Product)
	kindOf := func(def Kind) Kind {
		if k := kindFromName(name); k != KindOther {
			return k
		}
		return def
	}
	switch {
	case h.VID == vidASUS:
		if h.UsagePage == 0xFF01 && (h.OutLen == 65 || h.OutLen == 64) {
			if name == "" {
				name = "ROG kablosuz mouse"
			}
			p := &pAsus
			if h.OutLen == 64 {
				p = &pAsusOmni
			}
			return ssModel{name: name, brand: "ASUS", p: p, kind: kindOf(KindMouse), kindSet: true}, true
		}
	case sinowealthVendor(h.VID):
		if h.UsagePage >= 0xFF00 && h.FeatLen == 65 {
			if name == "" {
				name = "kablosuz mouse"
			}
			brand := "Glorious"
			if h.VID == vidWLmouse {
				brand = "WLmouse"
			}
			return ssModel{name: name, brand: brand, p: &pSinowealth, kind: kindOf(KindMouse), kindSet: true}, true
		}
	case h.VID == vidZowie:
		return zowieModel(h)
	case h.VID == vidGravion && h.UsagePage == 0xFF01 && h.InLen >= 16:
		if name == "" || strings.Contains(strings.ToLower(name), "wireless") {
			name = "Gravion kablosuz klavye"
		}
		return ssModel{name: name, brand: "", p: &pGravion, kind: kindOf(KindKeyboard), kindSet: true}, true
	case compxKbdReceiver(h.VID, h.PID) && h.UsagePage == 0xFF02 && h.OutLen == 20 && h.InLen == 20:
		// Common "2.4G Wireless Receiver" for keyboards (Compx 3554:FA09: AULA, MCHOSE, KZZI,
		// LEOBOG, MARVO, YUNZII, Redragon, MageGee and many other brands).
		if name == "" || strings.Contains(strings.ToLower(name), "receiver") {
			name = "2.4G kablosuz klavye"
		}
		return ssModel{name: name, brand: "", p: &pCompxKbd, kind: kindOf(KindKeyboard), kindSet: true}, true
	case compxFeatVendor(h.VID) && h.UsagePage >= 0xFF00 && h.FeatLen == 17 && h.OutLen != 17:
		if name == "" {
			name = "2.4 GHz mouse"
		}
		brand := ""
		if h.VID == vidAreson && !strings.Contains(strings.ToLower(name), "fantech") {
			brand = "Fantech"
		}
		return ssModel{name: name, brand: brand, p: &pCompxFeat, kind: kindOf(KindMouse), kindSet: true}, true
	case compxFeatVendor(h.VID) && h.UsagePage >= 0xFF00 && h.InLen == 17:
		// the collection the 0x09 answer arrives on
		return ssModel{name: name, brand: "Fantech", p: &pCompxFeat, kind: kindOf(KindMouse), kindSet: true}, true
	case compxVendor(h.VID) && h.UsagePage >= 0xFF00 && h.OutLen == 17:
		// Only receivers of known Compx-family vendors are asked: sending this frame to
		// arbitrary devices that merely have the same report size is not safe.
		if name == "" {
			name = "2.4 GHz mouse"
		}
		return ssModel{name: name, brand: compxBrand(h.VID, name), p: &pCompx, kind: kindOf(KindMouse), kindSet: true}, true
	}
	return ssModel{}, false
}

func compxBrand(vid uint16, name string) string {
	n := strings.ToLower(name)
	for _, b := range []string{"Pulsar", "VXE", "VGN", "ATK", "Kysona", "LAMZU", "G-Wolves", "MCHOSE", "Attack Shark", "Darmoshark", "Scyrox", "Incott", "Rapoo", "Ajazz", "Redragon", "Fantech", "Dareu"} {
		if strings.Contains(n, strings.ToLower(b)) {
			return "" // the product name already carries the brand
		}
	}
	switch vid {
	case vidCompx:
		return "Pulsar / VXE"
	case vidATK:
		return "ATK"
	case vidLamzu:
		return "LAMZU"
	}
	return ""
}

// ---------------- Compx / Nordic ----------------

func compxFrame(cmd byte) []byte {
	f := make([]byte, 17)
	f[0], f[1] = 0x08, cmd
	sum := 0
	for _, b := range f[:16] {
		sum += int(b)
	}
	f[16] = byte(0x55 - sum)
	return f
}

var pCompx = ssProto{kind: KindMouse, iface: -1, page: 0xFF02, cmd: compxFrame(0x04), echo: []byte{0x08, 0x04},
	parse: func(d []byte) (ssResult, bool) {
		if len(d) < 17 {
			return ssResult{}, false
		}
		sum := 0
		for _, b := range d[:16] {
			sum += int(b)
		}
		if byte(0x55-sum) != d[16] {
			return ssResult{}, false
		}
		level, cable := int(d[6]), d[7] != 0
		mv := int(d[8])<<8 | int(d[9])
		if level > 100 {
			return ssResult{}, false
		}
		if level == 0 && mv == 0 && !cable {
			return ssResult{offline: true}, true // dongle answers, mouse is off
		}
		return ssResult{pct: level, charging: cable && level < 100}, true
	}}

// ---------------- ASUS ROG / TUF ----------------

func asusParse(d []byte) (ssResult, bool) {
	o := -1
	for i := 0; i <= 1 && i+1 < len(d); i++ {
		if d[i] == 0x12 && d[i+1] == 0x07 {
			o = i
			break
		}
	}
	if o < 0 || at(d, o+9) < 0 {
		return ssResult{}, false
	}
	pct, chg := at(d, o+4), at(d, o+9) != 0
	if pct > 100 {
		return ssResult{}, false
	}
	if pct == 0 && !chg {
		return ssResult{offline: true}, true // standby / switched off
	}
	return ssResult{pct: pct, charging: chg}, true
}

var (
	pAsus     = ssProto{kind: KindMouse, iface: 0, page: 0xFF01, cmd: []byte{0x00, 0x12, 0x07}, parse: asusParse}
	pAsusOmni = ssProto{kind: KindMouse, iface: 2, page: 0xFF01, cmd: []byte{0x03, 0x12, 0x07}, parse: asusParse}
)

// ---------------- Sinowealth ----------------

var pSinowealth = ssProto{kind: KindMouse, iface: -1, noTransport: true, query: sinowealthQuery}

func sinowealthQuery(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
	h, err := openHID(c.Path, windows.GENERIC_READ|windows.GENERIC_WRITE, false)
	if err != nil {
		h, err = openHID(c.Path, 0, false)
		if err != nil {
			return ssResult{}, nil, false
		}
	}
	defer windows.CloseHandle(h)
	for try := 0; try < 3; try++ {
		req := make([]byte, 65)
		copy(req[1:], []byte{0x00, 0x00, 0x02, 0x02, 0x00, 0x83})
		if ok, _, _ := pHidD_SetFeature.Call(uintptr(h), uintptr(unsafe.Pointer(&req[0])), 65); ok == 0 {
			return ssResult{}, nil, false
		}
		time.Sleep(110 * time.Millisecond) // radio round trip to the mouse
		resp := make([]byte, 65)
		if ok, _, _ := pHidD_GetFeature.Call(uintptr(h), uintptr(unsafe.Pointer(&resp[0])), 65); ok == 0 {
			return ssResult{}, nil, false
		}
		r := resp[1:] // a1 00 02 02 00 83 <charging> <level>
		switch {
		case r[5] != 0x83:
			continue
		case r[0] == 0xA4:
			return ssResult{offline: true}, r[:9], true
		case r[0] == 0xA2: // waking up
			time.Sleep(150 * time.Millisecond)
			continue
		case r[0] == 0xA1 && r[7] <= 100:
			return ssResult{pct: int(r[7]), charging: r[6] != 0}, r[:9], true
		}
	}
	return ssResult{}, nil, false
}

// ---------------- Compx keyboard receiver (3554:FA09 "2.4G Wireless Receiver") ----------------
//
// 20-byte output/input report 0x13 on FF02:0002: "13 4A 01 00 ... <sum>" asks for the battery,
// the last byte being the sum of the others. Reply: "13 4A|80 01 00 xx LL SS": LL = level %,
// SS high nibble 1 = cable in / charging. The keyboard also pushes "13 0A 01 00 04 05 LL FF".
// Sources: qu1ncky/BatteryTray #16, ushineko/sanshoku #36, PyFlat/Device-Battery-Info.
// Some keyboards on this receiver answer the Compx 0x08 frame instead, which is tried second.

func compxKbdFrame(cmd byte) []byte {
	f := make([]byte, 20)
	f[0], f[1], f[2] = 0x13, cmd, 0x01
	sum := 0
	for _, b := range f[:19] {
		sum += int(b)
	}
	f[19] = byte(sum)
	return f
}

// compxKbdState is what is known about one keyboard receiver. Some keyboards on this receiver
// (Redragon K673 and others) never answer the 0x4A question, and questions sent again and again
// have been seen to make such keyboards blink or reconnect. So the receiver is asked ONCE; after
// that it is only listened to (keyboards push "13 0A ..." frames when their level changes), and
// asked again every few minutes only if it has answered a question before.
type compxKbdState struct {
	asked, answers bool
	have           bool
	last           ssResult
	raw            []byte
	nextAsk        time.Time
}

var compxKbd = map[string]*compxKbdState{}

func compxKbdParse(r []byte) (ssResult, bool) {
	if len(r) < 8 || r[0] != 0x13 {
		return ssResult{}, false
	}
	switch r[1] & 0x7F {
	case 0x4A:
		lv, st := int(r[5]), r[6]
		if lv < 1 || lv > 100 {
			return ssResult{}, false
		}
		return ssResult{pct: lv, charging: st>>4 == 1 && lv < 100}, true
	case 0x0A:
		if r[4] != 0x04 || r[5] != 0x05 || r[6] < 1 || r[6] > 100 {
			return ssResult{}, false
		}
		return ssResult{pct: int(r[6]), charging: r[7]>>4 == 1 && r[6] < 100}, true
	}
	return ssResult{}, false
}

var pCompxKbd = ssProto{kind: KindKeyboard, iface: -1, page: 0xFF02, passive: true,
	query: func(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
		st := compxKbd[d.key]
		if st == nil {
			st = &compxKbdState{}
			compxKbd[d.key] = st
		}
		// 1) frames the keyboard sent by itself since the last look
		for {
			raw, ok := d.t.recv(time.Millisecond, func([]byte) bool { return true })
			if !ok {
				break
			}
			if res, ok := compxKbdParse(raw); ok {
				st.last, st.raw, st.have = res, append([]byte{}, raw[:8]...), true
			}
		}
		// 2) one question; repeated only for keyboards that answer questions
		if !st.asked || (st.answers && time.Now().After(st.nextAsk)) {
			first := !st.asked
			st.asked = true
			st.nextAsk = time.Now().Add(5 * time.Minute)
			if raw, ok := d.xfer(c, compxKbdFrame(0x4A), func(r []byte) bool { _, ok := compxKbdParse(r); return ok }, 900*time.Millisecond); ok {
				res, _ := compxKbdParse(raw)
				st.last, st.raw, st.have, st.answers = res, append([]byte{}, raw[:8]...), true, true
			} else if first {
				logf("    2.4G klavye alicisi pil sorusuna yanit vermedi: bundan sonra yalnizca dinleniyor")
			}
		}
		if st.have {
			return st.last, st.raw, true
		}
		return ssResult{}, nil, false
	}}

func padTo(b []byte, n int) []byte {
	if len(b) >= n {
		return b[:n]
	}
	out := make([]byte, n)
	copy(out, b)
	return out
}

// ---------------- Compx frame as a feature report (Fantech Helios Go XD5 and others) ----------------
//
// Feature report 0x08 "08 04 00 ... <0x55 - sum>" (17 bytes), answered by input report 0x09
// "09 04 00 00 00 02 LL CC": LL = battery %, CC = charging. Sources: jarjut/xd5-go-web (reverse
// engineered from Fantech's OemDrv.exe), hnxzl/ariaxd7, krisgfx9/Wireless-Device-Battery-Monitor #167.

var pCompxFeat = ssProto{kind: KindMouse, iface: -1, noTransport: true,
	query: func(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
		if c.FeatLen != 17 {
			return ssResult{}, nil, false // only the feature collection starts an exchange
		}
		// The answer may come on another collection of the same device: listen on the one with
		// 17-byte input reports (this one if it has them).
		in := c
		if c.InLen != 17 {
			for _, o := range d.cands {
				if o.InLen == 17 {
					in = o
					break
				}
			}
		}
		if in.InLen <= 0 {
			return ssResult{}, nil, false
		}
		if d.t == nil || d.tpath != in.Path {
			if d.t != nil {
				d.t.Close()
			}
			t, err := newTransport("", 0, 0, in.Path, in.InLen, in.OutLen)
			if err != nil {
				d.t, d.tpath = nil, ""
				return ssResult{}, nil, false
			}
			d.t, d.tpath = t, in.Path
		}
		h, err := openHID(c.Path, windows.GENERIC_READ|windows.GENERIC_WRITE, false)
		if err != nil {
			if h, err = openHID(c.Path, 0, false); err != nil {
				return ssResult{}, nil, false
			}
		}
		defer windows.CloseHandle(h)
		d.t.drain()
		req := compxFrame(0x04)
		if ok, _, _ := pHidD_SetFeature.Call(uintptr(h), uintptr(unsafe.Pointer(&req[0])), uintptr(len(req))); ok == 0 {
			return ssResult{}, nil, false
		}
		raw, ok := d.t.recv(900*time.Millisecond, func(r []byte) bool {
			return len(r) > 7 && r[0] == 0x09 && r[1] == 0x04
		})
		if !ok {
			return ssResult{}, nil, false
		}
		lv := int(raw[6])
		if lv > 100 {
			return ssResult{}, nil, false
		}
		if lv == 0 && raw[7] == 0 {
			return ssResult{offline: true}, raw[:8], true
		}
		return ssResult{pct: lv, charging: raw[7] != 0 && lv < 100}, raw[:8], true
	}}

// ---------------- ZOWIE DW / CW wireless mice ----------------
//
// Receiver 04A5:800A (or the mouse itself on its cable, other PIDs). Output report 0x08
// "08 B4 15" (16 bytes) on the FF03 collection is answered by input report 0x09 on the FF04
// collection: "09 HH LL 00 C8": HHLL = battery voltage in hundredths of a volt. Read-only.
// Source: IgnacioBarb/zowie-battery-tray (MIT), taken from ZOWIE's firmware update tool.

func zowieModel(h hidInfo) (ssModel, bool) {
	if (h.UsagePage == 0xFF03 && h.OutLen == 16) || (h.UsagePage == 0xFF04 && h.InLen == 16) {
		name := strings.TrimSpace(h.Product)
		if name == "" || strings.Contains(strings.ToLower(name), "receiver") || strings.Contains(strings.ToLower(name), "dongle") {
			name = "kablosuz mouse"
		}
		return ssModel{name: name, brand: "ZOWIE", p: &pZowie, kind: KindMouse, kindSet: true}, true
	}
	return ssModel{}, false
}

func zowiePercent(mv int) int {
	curve := [][2]int{{3500, 0}, {3680, 10}, {3760, 25}, {3890, 50}, {4000, 75}, {4200, 100}}
	if mv <= curve[0][0] {
		return 0
	}
	for i := 1; i < len(curve); i++ {
		if mv <= curve[i][0] {
			lo, hi := curve[i-1], curve[i]
			return lo[1] + (mv-lo[0])*(hi[1]-lo[1])/(hi[0]-lo[0])
		}
	}
	return 100
}

func zowieRead(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
	if c.UsagePage != 0xFF03 || c.OutLen != 16 {
		return ssResult{}, nil, false // only the command collection starts an exchange
	}
	var in *hidInfo
	for i := range d.cands {
		if d.cands[i].UsagePage == 0xFF04 && d.cands[i].InLen == 16 {
			in = &d.cands[i]
			break
		}
	}
	if in == nil {
		return ssResult{}, nil, false
	}
	t, err := newTransport("", 0, 0, in.Path, in.InLen, in.OutLen)
	if err != nil {
		return ssResult{}, nil, false
	}
	defer t.Close()
	h, err := openHID(c.Path, windows.GENERIC_WRITE, false)
	if err != nil {
		return ssResult{}, nil, false
	}
	defer windows.CloseHandle(h)
	req := make([]byte, 16)
	req[0], req[1], req[2] = 0x08, 0xB4, 0x15
	time.Sleep(20 * time.Millisecond) // let the reader start before the answer can arrive
	var n uint32
	if windows.WriteFile(h, req, &n, nil) != nil {
		return ssResult{}, nil, false
	}
	raw, ok := t.recv(1500*time.Millisecond, func(r []byte) bool { return len(r) >= 5 && r[0] == 0x09 })
	if !ok || raw[1] == 0xE6 || raw[4] != 0xC8 {
		return ssResult{}, nil, false
	}
	mv := (int(raw[1])<<8 | int(raw[2])) * 10
	if mv == 0 {
		return ssResult{offline: true}, raw[:5], true // receiver has no reading: mouse off
	}
	if mv < 2500 || mv > 4700 {
		return ssResult{}, nil, false
	}
	onCable := d.pid != 0x800A
	return ssResult{pct: zowiePercent(mv), charging: onCable || mv >= 4250}, raw[:5], true
}

var pZowie = ssProto{kind: KindMouse, iface: -1, page: 0xFF03, noTransport: true}

func init() { pZowie.query = zowieThrottled } // asked once a minute: a slowly changing voltage

func zowieThrottled(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
	if c.UsagePage != 0xFF03 {
		return ssResult{}, nil, false
	}
	g435Mu.Lock()
	e, have := g435Cache[d.key]
	g435Mu.Unlock()
	if have && time.Now().Before(e.next) {
		return e.res, e.raw, e.ok
	}
	res, raw, ok := zowieRead(d, c)
	wait := time.Minute
	if ok && res.offline {
		wait = 30 * time.Second
	}
	if !ok && have && e.ok && !e.res.offline {
		res, raw, ok = e.res, e.raw, true
	}
	g435Mu.Lock()
	g435Cache[d.key] = g435Entry{res: res, raw: raw, ok: ok, next: time.Now().Add(wait)}
	g435Mu.Unlock()
	return res, raw, ok
}

// ---------------- Gravion keyboards (receiver A8A6:xxxx) ----------------
//
// The receiver sends status frames by itself on its FF01 collection: "AA ED" header with the
// battery level in byte 3 (reported for the Gravion VEXOR 82, receiver A8A6:3353). Nothing is
// written to the receiver: the program only listens. Other Gravion models (Lunor MK68) are
// assumed to use the same frames; if they do not, nothing is shown.

var gravionLast = map[string]ssResult{}

var pGravion = ssProto{kind: KindKeyboard, iface: -1, page: 0xFF01, passive: true,
	query: func(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
		var seen []byte
		for {
			raw, ok := d.t.recv(time.Millisecond, func([]byte) bool { return true })
			if !ok {
				break
			}
			for o := 0; o <= 1 && o+4 <= len(raw); o++ {
				if raw[o] == 0xAA && raw[o+1] == 0xED && raw[o+3] >= 1 && raw[o+3] <= 100 {
					gravionLast[d.key] = ssResult{pct: int(raw[o+3])}
					seen = append([]byte{}, raw[:min(len(raw), 8)]...)
					break
				}
			}
		}
		if res, ok := gravionLast[d.key]; ok {
			return res, seen, true
		}
		return ssResult{}, nil, false
	}}
