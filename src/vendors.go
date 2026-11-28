//go:build windows

package main

import (
	"encoding/binary"
	"math"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Battery protocols of other brands' wireless receivers. Sources (open-source, documented):
//   - Razer: OpenRazer (github.com/openrazer/openrazer) - 90-byte feature report, class 0x07
//   - Corsair: OpenLinkHub (github.com/jurkovic-nikola/OpenLinkHub) and HeadsetControl - Bragi
//     property 0x0F (battery, per mille) / 0x10 (charge state); Void family command 0xC9 0x64
//   - HyperX, Sony INZONE: HeadsetControl (github.com/Sapd/HeadsetControl)

const (
	vidRazer    = 0x1532
	vidCorsair  = 0x1B1C
	vidHP       = 0x03F0 // HyperX (HP)
	vidKingston = 0x0951 // HyperX (Kingston era)
	vidSony     = 0x054C
)

func knownVendor(vid uint16) bool {
	switch vid {
	case ssVID, vidRazer, vidCorsair, vidHP, vidKingston, vidSony,
		vidCompx, vidATK, vidLamzu, vidSinowealth, vidWLmouse, vidASUS, vidAreson, vidDareu, vidDragonfly, vidZowie, vidGravion:
		return true
	}
	return false
}

// lookupModel picks the battery protocol for one HID collection, if any.
func lookupModel(h hidInfo) (ssModel, bool) {
	switch h.VID {
	case 0x046D: // Logitech: only the G435, which has no HID++ (everything else: lgmon.go)
		return g435Model(h)
	case ssVID:
		return steelSeriesModel(h)
	case vidRazer:
		// Every Razer device with the 90-byte control feature report understands the
		// battery query; wired-only devices simply answer "not supported".
		if h.FeatLen == 91 {
			name := strings.TrimSpace(h.Product)
			if name == "" {
				name = "Razer kablosuz cihaz"
			}
			k := kindFromName(name)
			if k == KindOther {
				k = KindMouse
			}
			return ssModel{name: name, brand: "Razer", p: &pRazer, kind: k, kindSet: true}, true
		}
	case vidCorsair:
		if m, ok := corsairVoid[h.PID]; ok && h.UsagePage >= 0xFF00 && h.OutLen >= 2 {
			return ssModel{name: m, brand: "Corsair", p: &pCorsairVoid}, true
		}
		if h.UsagePage == 0xFF42 && h.OutLen >= 64 { // Bragi / Slipstream
			name := strings.TrimSpace(strings.TrimPrefix(h.Product, "CORSAIR"))
			if name == "" {
				name = "kablosuz cihaz"
			}
			k := kindFromName(name)
			if k == KindOther {
				k = KindMouse
			}
			return ssModel{name: name, brand: "Corsair", p: &pCorsairBragi, kind: k, kindSet: true}, true
		}
	case vidHP, vidKingston:
		if m, ok := hyperxModels[uint32(h.VID)<<16|uint32(h.PID)]; ok && h.OutLen >= 2 && hyperxCollectionOK(m.p, h) {
			m.brand = "HyperX"
			return m, true
		}
	case vidSony:
		if m, ok := sonyModels[h.PID]; ok && h.UsagePage == 0xFF04 && h.OutLen >= 64 {
			return ssModel{name: m, brand: "Sony", p: &pSonyInzone}, true
		}
	}
	return lookupModel2(h)
}

// ---------------- Razer ----------------

var (
	pHidD_SetFeature = hiddll.NewProc("HidD_SetFeature")
	pHidD_GetFeature = hiddll.NewProc("HidD_GetFeature")
	razerMu          sync.Mutex
	razerTID         = map[string]byte{} // transaction ID that worked, per device path
)

func razerRequest(h windows.Handle, tid, class, id byte) ([]byte, bool) {
	buf := make([]byte, 91) // report ID 0 + 90-byte razer_report
	r := buf[1:]
	r[1] = tid
	r[5] = 0x02 // data size
	r[6] = class
	r[7] = id
	var crc byte
	for i := 2; i < 88; i++ {
		crc ^= r[i]
	}
	r[88] = crc
	if ok, _, _ := pHidD_SetFeature.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), 91); ok == 0 {
		return nil, false
	}
	for try := 0; try < 8; try++ {
		time.Sleep(time.Duration(60+try*40) * time.Millisecond)
		resp := make([]byte, 91)
		if ok, _, _ := pHidD_GetFeature.Call(uintptr(h), uintptr(unsafe.Pointer(&resp[0])), 91); ok == 0 {
			return nil, false
		}
		rr := resp[1:]
		switch rr[0] { // status
		case 0x01: // busy: ask again
			continue
		case 0x02: // success
			if rr[6] == class && rr[7] == id {
				return rr, true
			}
			return nil, false
		default: // 0x03 failure, 0x04 timeout (device asleep/off), 0x05 not supported
			return rr, false
		}
	}
	return nil, false
}

var pRazer = ssProto{kind: KindMouse, noTransport: true,
	query: func(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
		razerMu.Lock()
		defer razerMu.Unlock()
		h, err := openHID(c.Path, 0, false) // feature reports work without read/write access
		if err != nil {
			return ssResult{}, nil, false
		}
		defer windows.CloseHandle(h)
		tids := []byte{0x1F, 0x3F, 0xFF, 0x08}
		if t, ok := razerTID[c.Path]; ok {
			tids = append([]byte{t}, tids...)
		}
		for _, tid := range tids {
			rr, ok := razerRequest(h, tid, 0x07, 0x80) // get battery level
			if !ok {
				if rr != nil && rr[0] == 0x04 { // receiver says the device is not answering
					return ssResult{offline: true}, rr[:8], true
				}
				continue
			}
			razerTID[c.Path] = tid
			res := ssResult{pct: int(math.Round(float64(rr[9]) * 100 / 255))}
			if cr, ok := razerRequest(h, tid, 0x07, 0x84); ok { // charging status
				res.charging = cr[9] == 1
			}
			return res, rr[:12], true
		}
		return ssResult{}, nil, false
	}}

// ---------------- Corsair ----------------

var corsairVoid = map[uint16]string{
	0x1B1C: "Void", 0x1B27: "Void Wireless", 0x0A14: "Void Pro Wireless", 0x0A16: "Void Pro Wireless",
	0x0A17: "Void Pro", 0x0A1D: "Void Pro", 0x0A1A: "Void Pro Wireless", 0x1B2A: "Void Pro",
	0x1B23: "Void", 0x1B29: "Void Pro", 0x0A55: "Void Elite Wireless", 0x0A51: "Void Elite Wireless",
	0x0A52: "Void Elite", 0x0A38: "HS70 Wireless", 0x0A4F: "HS70 Pro Wireless", 0x0A2B: "Void Pro Wireless",
	0x0A75: "Void Elite Wireless", 0x0A56: "Void Elite Wireless",
}

// Void family: {C9 64} -> [2]=battery (bit7 = mic up), [4]=status 0 off, 1/2 normal, 4/5 charging
var pCorsairVoid = ssProto{kind: KindHeadset, cmd: []byte{0xC9, 0x64},
	parse: func(d []byte) (ssResult, bool) {
		if at(d, 4) < 0 {
			return ssResult{}, false
		}
		st := at(d, 4)
		if st == 0 {
			return ssResult{offline: true}, true
		}
		if st > 5 {
			return ssResult{}, false
		}
		return ssResult{pct: clampPct(at(d, 2) & 0x7F), charging: st == 4 || st == 5}, true
	}}

// Bragi: read property 0x0F (battery, per mille) and 0x10 (1 = charging) from the device endpoint.
// Two framings exist: {00 ep 02 0F} (mice/keyboards, value at [3:5]) and
// {00 02 ep 02 0F} (newer headset receivers, value at [4:6]).
var pCorsairBragi = ssProto{kind: KindMouse,
	query: func(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
		read := func(ep, prop byte, alt bool) ([]byte, int, bool) {
			pkt := []byte{0x00, ep, 0x02, prop}
			off := 3
			if alt {
				pkt = []byte{0x00, 0x02, ep, 0x02, prop}
				off = 4
			}
			data, ok := d.xfer(c, pkt, func(r []byte) bool { return len(r) > off+1 && r[off-2] == 0x02 }, 600*time.Millisecond)
			return data, off, ok
		}
		for _, alt := range []bool{false, true} {
			for _, ep := range []byte{0x09, 0x08, 0x0A} {
				data, off, ok := read(ep, 0x0F, alt)
				if !ok || data[off-1] != 0x00 { // status byte must be 0 (success)
					continue
				}
				v := int(binary.LittleEndian.Uint16(data[off : off+2]))
				if v <= 0 || v > 1000 {
					continue
				}
				res := ssResult{pct: (v + 5) / 10}
				if cd, coff, ok := read(ep, 0x10, alt); ok && cd[coff-1] == 0x00 {
					res.charging = cd[coff] == 1
				}
				return res, data[:off+2], true
			}
		}
		return ssResult{}, nil, false
	}}

// ---------------- HyperX ----------------

var hyperxModels = map[uint32]ssModel{
	uint32(vidHP)<<16 | 0x0696:       {name: "Cloud II Wireless", p: &pHyperXCloud2},
	uint32(vidHP)<<16 | 0x018B:       {name: "Cloud II Wireless", p: &pHyperXCloud2},
	uint32(vidKingston)<<16 | 0x1718: {name: "Cloud II Wireless", p: &pHyperXCloud2K},
	uint32(vidHP)<<16 | 0x098D:       {name: "Cloud Alpha Wireless", p: &pHyperXAlpha},
	uint32(vidHP)<<16 | 0x08BE:       {name: "Cloud Alpha 2 Wireless", p: &pHyperXAlpha2},
	uint32(vidKingston)<<16 | 0x16C4: {name: "Cloud Flight", p: &pHyperXFlight},
	uint32(vidKingston)<<16 | 0x1723: {name: "Cloud Flight", p: &pHyperXFlight},
	uint32(vidHP)<<16 | 0x06BE:       {name: "Cloud III S Wireless", p: &pHyperXCloud3S},
	uint32(vidHP)<<16 | 0x02CC:       {name: "Cloud III S Wireless", p: &pHyperXCloud3S},
	uint32(vidHP)<<16 | 0x05B7:       {name: "Cloud III Wireless", p: &pHyperXCloud3},
	uint32(vidHP)<<16 | 0x0C9D:       {name: "Cloud III Wireless", p: &pHyperXCloud3},
	uint32(vidHP)<<16 | 0x0FCC:       {name: "Cloud III Wireless", p: &pHyperXCloud3},
}

// Cloud II Wireless (HP): {06 FF BB cmd} -> echo, battery cmd 02 -> [7]; charging cmd 03 -> [4]==1
var pHyperXCloud2 = ssProto{kind: KindHeadset,
	query: func(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
		ask := func(cmd byte) ([]byte, bool) {
			return d.xfer(c, []byte{0x06, 0xFF, 0xBB, cmd}, func(r []byte) bool {
				return len(r) > 7 && r[0] == 0x06 && r[1] == 0xFF && r[2] == 0xBB && r[3] == cmd
			}, time.Second)
		}
		lv, ok := ask(0x02)
		if !ok || lv[7] > 100 {
			return ssResult{}, nil, false
		}
		res := ssResult{pct: int(lv[7])}
		if ch, ok := ask(0x03); ok {
			res.charging = ch[4] == 1
		}
		return res, lv[:8], true
	}}

// Cloud II Wireless (Kingston): fixed 62-byte frame, reply [0]=0B [2]=BB [3]=cmd, battery [7], charging [4]
var pHyperXCloud2K = ssProto{kind: KindHeadset,
	query: func(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
		ask := func(cmd byte) ([]byte, bool) {
			req := []byte{0x06, 0x00, 0x02, 0x00, 0x9A, 0x00, 0x00, 0x68, 0x4A, 0x8E, 0x0A, 0x00, 0x00, 0x00, 0xBB, cmd, 0x00}
			return d.xfer(c, req, func(r []byte) bool { return len(r) > 7 && r[0] == 0x0B && r[2] == 0xBB && r[3] == cmd }, time.Second)
		}
		lv, ok := ask(0x02)
		if !ok || lv[7] > 100 {
			return ssResult{}, nil, false
		}
		res := ssResult{pct: int(lv[7])}
		if ch, ok := ask(0x03); ok {
			res.charging = ch[4] == 1
		}
		return res, lv[:8], true
	}}

// Cloud Alpha Wireless: {21 BB 03} connection ([3]==1 offline), {21 BB 0C} charging ([3]==1), {21 BB 0B} battery [3]
var pHyperXAlpha = ssProto{kind: KindHeadset,
	query: func(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
		ask := func(sub byte) ([]byte, bool) {
			return d.xfer(c, []byte{0x21, 0xBB, sub}, func(r []byte) bool { return len(r) > 3 && r[0] == 0x21 && r[1] == 0xBB && r[2] == sub }, 1500*time.Millisecond)
		}
		if cn, ok := ask(0x03); ok && cn[3] == 0x01 {
			return ssResult{offline: true}, cn[:4], true
		}
		res := ssResult{}
		if ch, ok := ask(0x0C); ok {
			res.charging = ch[3] == 0x01
		}
		lv, ok := ask(0x0B)
		if !ok || lv[3] > 100 {
			return ssResult{}, nil, false
		}
		res.pct = int(lv[3])
		return res, lv[:4], true
	}}

// Cloud Alpha 2 Wireless base station: {52 01} -> reply 53: [2]==0 offline; {50 02} -> reply 51: [2]=battery, [3]==1 charging
var pHyperXAlpha2 = ssProto{kind: KindHeadset,
	query: func(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
		if cn, ok := d.xfer(c, []byte{0x52, 0x01}, func(r []byte) bool { return len(r) > 2 && r[0] == 0x53 && r[1] == 0x01 }, time.Second); ok && cn[2] == 0 {
			return ssResult{offline: true}, cn[:3], true
		}
		lv, ok := d.xfer(c, []byte{0x50, 0x02}, func(r []byte) bool { return len(r) > 3 && r[0] == 0x51 && r[1] == 0x02 }, time.Second)
		if !ok || lv[2] > 100 {
			return ssResult{}, nil, false
		}
		return ssResult{pct: int(lv[2]), charging: lv[3] == 0x01}, lv[:4], true
	}}

// Cloud Flight: {21 FF 05} -> voltage [3:5] (big endian); above 0x100B = charging
var pHyperXFlight = ssProto{kind: KindHeadset,
	query: func(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
		r, ok := d.xfer(c, []byte{0x21, 0xFF, 0x05}, func(r []byte) bool { return len(r) > 4 && r[0] == 0x21 && r[1] == 0xFF && r[2] == 0x05 }, 2*time.Second)
		if !ok {
			return ssResult{}, nil, false
		}
		v := float64(int(r[3])<<8 | int(r[4]))
		if v > 0x100B {
			return ssResult{pct: 100, charging: true}, r[:5], true
		}
		var pct float64
		switch {
		case v <= 3648:
			pct = 0.00125 * v
		case v > 3975:
			pct = 100
		default:
			pct = 0.00000002547505*math.Pow(v, 4) - 0.0003900299*math.Pow(v, 3) + 2.238321*v*v - 5706.256*v + 5452299.0
		}
		return ssResult{pct: clampPct(int(math.Round(pct)))}, r[:5], true
	}}

// ---------------- Sony INZONE ----------------

var sonyModels = map[uint16]string{0x0FA8: "INZONE H9 II", 0x0EBF: "INZONE H5"}

var sonyTID uint16 = 1

// Sony INZONE dongles speak an HCI-style protocol on report 0x02: battery = event 0x04 (GET),
// reply payload [0]=charger, [1]=percent (0xFF = headset offline).
var pSonyInzone = ssProto{kind: KindHeadset,
	query: func(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
		sonyTID++
		if sonyTID <= 1 {
			sonyTID = 2
		}
		tid := sonyTID
		buf := make([]byte, 64)
		buf[0], buf[1] = 0x02, 12 // report ID, HCI length
		buf[2], buf[3], buf[4], buf[5] = 0x01, 0x00, 0xFC, 8
		buf[6], buf[7] = 0x96, 0xC3
		buf[8], buf[9], buf[10] = 0x41, 0x04, 0x01 // PC->receiver, BATTERY_INFO, GET
		buf[11], buf[12] = byte(tid), byte(tid>>8)
		var sum byte
		for i := 6; i <= 12; i++ {
			sum += buf[i]
		}
		buf[13] = sum
		r, ok := d.xfer(c, buf, func(r []byte) bool {
			if len(r) < 16 || r[0] != 0x02 || r[2] != 0x04 || r[3] != 0xFF || r[6] != 0x96 || r[7] != 0xC3 {
				return false
			}
			n := int(r[1])
			if n < 14 || n+1 >= len(r) || r[9] != 0x04 || uint16(r[11])|uint16(r[12])<<8 != tid {
				return false
			}
			var s byte
			for i := 5; i <= n; i++ {
				s += r[i]
			}
			return s == r[n+1]
		}, 1500*time.Millisecond)
		if !ok {
			return ssResult{}, nil, false
		}
		charger, pct := r[13], r[14]
		if pct == 0xFF {
			return ssResult{offline: true}, r[:15], true
		}
		if pct > 100 {
			return ssResult{}, nil, false
		}
		return ssResult{pct: int(pct), charging: charger != 0}, r[:15], true
	}}

// hyperxCollectionOK: most HyperX dongles talk on a vendor page (>= 0xFF00); the Cloud III S uses
// the 64-byte collection on page 0x01C0, the Cloud III Wireless the 62-byte one (any page).
func hyperxCollectionOK(p *ssProto, h hidInfo) bool {
	switch p {
	case &pHyperXCloud3S:
		return h.UsagePage == 0x01C0 && h.OutLen >= 6
	case &pHyperXCloud3:
		return h.OutLen == 62
	}
	return h.UsagePage >= 0xFF00
}

// Cloud III S Wireless (03F0:06BE / 02CC): 64-byte output report 0x0C "0c 02 03 01 00 <cmd>"
// on the 0x01C0 collection, answered by input report 0x0C with the value in byte 6:
// cmd 0x06 = battery % (over 100 = headset off), cmd 0x48 = charging. Source: LennardKittner/
// HyperHeadset (cloud_iii_s_wireless) and HeadsetControl issue #486 (NGENUITY captures).
var pHyperXCloud3S = ssProto{kind: KindHeadset,
	query: func(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
		ask := func(cmd byte) ([]byte, bool) {
			return d.xfer(c, []byte{0x0C, 0x02, 0x03, 0x01, 0x00, cmd}, func(r []byte) bool {
				return len(r) > 6 && r[0] == 0x0C && r[5] == cmd
			}, time.Second)
		}
		lv, ok := ask(0x06)
		if !ok {
			return ssResult{}, nil, false
		}
		if lv[6] > 100 {
			return ssResult{offline: true}, lv[:7], true
		}
		res := ssResult{pct: int(lv[6])}
		if ch, ok := ask(0x48); ok {
			res.charging = ch[6] != 0
		}
		return res, lv[:7], true
	}}

// Cloud III Wireless: 62-byte output report "66 <cmd>": 0x89 -> battery in byte 4,
// 0x8A -> charging (byte 2 non-zero). From NGENUITY USB captures (HaloBattery / BatteryHub).
var pHyperXCloud3 = ssProto{kind: KindHeadset,
	query: func(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
		ask := func(cmd byte) ([]byte, bool) {
			return d.xfer(c, []byte{0x66, cmd}, func(r []byte) bool {
				return len(r) > 4 && r[0] == 0x66 && r[1] == cmd
			}, time.Second)
		}
		lv, ok := ask(0x89)
		if !ok || lv[4] > 100 {
			return ssResult{}, nil, false
		}
		res := ssResult{pct: int(lv[4])}
		if ch, ok := ask(0x8A); ok {
			res.charging = ch[2] != 0
		}
		return res, lv[:5], true
	}}
