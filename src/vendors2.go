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
)

func compxVendor(vid uint16) bool {
	return vid == vidCompx || vid == vidATK || vid == vidLamzu
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
	case h.UsagePage >= 0xFF00 && h.OutLen == 17 && (h.InLen == 17 || compxVendor(h.VID)) && h.VID != 0x046D && (compxVendor(h.VID) || !knownVendor(h.VID)):
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

var pCompx = ssProto{kind: KindMouse, iface: -1, cmd: compxFrame(0x04), echo: []byte{0x08, 0x04},
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
