//go:build windows

package main

import (
	"fmt"
	"regexp"

	"golang.org/x/sys/windows"
	"strings"
	"time"
)

// Logitech HID++ 2.0 (Unifying, Lightspeed, Bolt receivers, direct USB / Bluetooth devices and G headsets).

const hidppSWID = 0x09

type hidppErr struct {
	code byte
	v1   bool
}

func (e hidppErr) Error() string { return fmt.Sprintf("hid++ error v1=%v code=%d", e.v1, e.code) }

type timeoutErr struct{}

func (timeoutErr) Error() string { return "timeout" }

type hidppDev struct {
	t        *hidTransport
	featIdx  map[[2]uint16]byte // (devIdx, featureID) -> index
	product  string
	vendorPg uint16
	timeout  time.Duration
}

func (d *hidppDev) to() time.Duration {
	if d.timeout > 0 {
		return d.timeout
	}
	return 1500 * time.Millisecond
}

func (d *hidppDev) request(idx, feat, fn byte, params []byte, timeout time.Duration) ([]byte, error) {
	fs := fn<<4 | hidppSWID
	t := d.t
	var pkt []byte
	var ch *hidChan
	switch {
	case t.short != nil && t.short.outLen == 7 && len(params) <= 3:
		pkt, ch = make([]byte, 7), t.short
		pkt[0] = 0x10
	case t.long != nil && t.long.outLen == 20:
		pkt, ch = make([]byte, 20), t.long
		pkt[0] = 0x11
	case t.short != nil && t.short.outLen == 7:
		pkt, ch = make([]byte, 7), t.short
		pkt[0] = 0x10
	default:
		return nil, fmt.Errorf("no channel")
	}
	pkt[1], pkt[2], pkt[3] = idx, feat, fs
	copy(pkt[4:], params)
	t.drain()
	if err := t.write(ch, pkt); err != nil {
		return nil, err
	}
	p, ok := t.recv(timeout, func(p []byte) bool {
		if len(p) < 7 || (p[0] != 0x10 && p[0] != 0x11) || p[1] != idx {
			return false
		}
		if p[2] == feat && p[3] == fs {
			return true
		}
		return (p[2] == 0x8F || p[2] == 0xFF) && p[3] == feat && p[4] == fs
	})
	if !ok {
		return nil, timeoutErr{}
	}
	if p[2] == 0x8F && !(feat == 0x8F) {
		return nil, hidppErr{code: p[5], v1: true}
	}
	if p[2] == 0xFF && feat != 0xFF {
		return nil, hidppErr{code: p[5]}
	}
	return p[4:], nil
}

// ping returns the HID++ protocol major version of the device at idx.
func (d *hidppDev) ping(idx byte) (int, error) {
	r, err := d.request(idx, 0x00, 0x01, []byte{0, 0, 0x5A}, d.to())
	if err != nil {
		if e, ok := err.(hidppErr); ok && e.v1 && e.code == 0x01 {
			return 1, nil
		}
		return 0, err
	}
	return int(r[0]), nil
}

func (d *hidppDev) feature(idx byte, id uint16) byte {
	k := [2]uint16{uint16(idx), id}
	if v, ok := d.featIdx[k]; ok {
		return v
	}
	r, err := d.request(idx, 0x00, 0x00, []byte{byte(id >> 8), byte(id)}, 1500*time.Millisecond)
	v := byte(0)
	if err == nil {
		v = r[0]
	}
	d.featIdx[k] = v
	return v
}

var voltTable = [][2]int{{4186, 100}, {4067, 90}, {3989, 80}, {3922, 70}, {3859, 60}, {3811, 50}, {3778, 40}, {3751, 30}, {3717, 20}, {3671, 10}, {3646, 5}, {3579, 2}, {3500, 0}}

// Headset table (G733 and similar), matched to G HUB's reading: ~3.81 V under load = 78 %.
var headsetVoltTable = [][2]int{{4150, 100}, {4050, 96}, {3950, 90}, {3880, 84}, {3810, 78}, {3760, 70}, {3720, 60}, {3680, 48}, {3640, 35}, {3600, 22}, {3560, 12}, {3520, 5}, {3450, 0}}

func headsetVoltToPercent(mv int) int { return curve(headsetVoltTable, mv) }

func voltToPercent(mv int) int { return curve(voltTable, mv) }

func curve(voltTable [][2]int, mv int) int {
	if mv >= voltTable[0][0] {
		return 100
	}
	for i := 1; i < len(voltTable); i++ {
		hi, lo := voltTable[i-1], voltTable[i]
		if mv >= lo[0] {
			return lo[1] + (mv-lo[0])*(hi[1]-lo[1])/(hi[0]-lo[0])
		}
	}
	return 0
}

type battRead struct {
	pct      int
	charging bool
	src      string // which HID++ feature produced it
	raw      []byte
	voltage  bool // percentage estimated from voltage (smoothed by the caller)
	coarse   bool // device only reports steps (100/50/20/5)
}

// battery reads every battery feature the device has and picks the most precise one:
// 1) 0x1004 real state-of-charge  2) voltage (0x1001 / 0x1F20)  3) 0x1000 level  4) 0x1004 level steps.
func (d *hidppDev) battery(idx byte) (pct int, charging bool, ok bool) {
	b, ok := d.batteryRead(idx)
	return b.pct, b.charging, ok
}

func (d *hidppDev) batteryRead(idx byte) (battRead, bool) {
	to := d.to()
	var steps *battRead
	if fi := d.feature(idx, 0x1004); fi != 0 { // UNIFIED_BATTERY
		socSupported := true
		if c, err := d.request(idx, fi, 0, nil, to); err == nil {
			socSupported = c[1]&0x02 != 0
		}
		if r, err := d.request(idx, fi, 1, nil, to); err == nil {
			// r[2]: 0 discharging, 1 charging, 2 slow charging, 3 complete, 4 error; r[3] = external power
			chg := r[2] == 1 || r[2] == 2 || (r[2] == 3 && r[3] != 0)
			if socSupported && r[0] > 0 && r[0] <= 100 {
				return battRead{pct: int(r[0]), charging: chg, src: "1004-soc", raw: r[:4]}, true
			}
			lv := 0
			switch {
			case r[1]&0x08 != 0:
				lv = 100
			case r[1]&0x04 != 0:
				lv = 60
			case r[1]&0x02 != 0:
				lv = 20
			case r[1]&0x01 != 0:
				lv = 5
			}
			steps = &battRead{pct: lv, charging: chg, src: "1004-seviye", raw: r[:4], coarse: true}
		}
	}
	if fi := d.feature(idx, 0x1001); fi != 0 { // BATTERY_VOLTAGE
		if r, err := d.request(idx, fi, 0, nil, to); err == nil {
			mv := int(r[0])<<8 | int(r[1])
			if mv >= 3000 && mv <= 4500 {
				// bit7 = on external power; low bits: 0 charging, 1 end of charge, 2 stopped, 3 error
				chg := r[2]&0x80 != 0 && r[2]&0x03 <= 1
				return battRead{pct: voltToPercent(mv), charging: chg, src: fmt.Sprintf("1001-%dmV", mv), raw: r[:3], voltage: true}, true
			}
		}
	}
	if fi := d.feature(idx, 0x1F20); fi != 0 { // ADC_MEASUREMENT (G headsets)
		if r, err := d.request(idx, fi, 0, nil, to); err == nil {
			mv := int(r[0])<<8 | int(r[1])
			if mv >= 3000 && mv <= 4500 {
				// flags: bit0 = reading valid / connected, bit1 = charging
				return battRead{pct: headsetVoltToPercent(mv), charging: r[2]&0x02 != 0, src: fmt.Sprintf("1F20-%dmV", mv), raw: r[:3], voltage: true}, true
			}
		}
	}
	if fi := d.feature(idx, 0x1000); fi != 0 { // BATTERY_STATUS
		if r, err := d.request(idx, fi, 0, nil, to); err == nil {
			// r[2]: 0 discharging, 1 recharging, 2 almost full, 3 full, 4 slow recharge, 5/6 error
			pct := int(r[0])
			chg := r[2] == 1 || r[2] == 2 || r[2] == 4
			if r[2] == 3 {
				pct = 100
			}
			if pct > 0 && pct <= 100 {
				return battRead{pct: pct, charging: chg, src: "1000", raw: r[:3], coarse: pct%5 == 0}, true
			}
		}
	}
	if steps != nil && steps.pct > 0 {
		return *steps, true
	}
	return battRead{}, false
}

func clampPct(p int) int {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

func (d *hidppDev) nameAndKind(idx byte) (string, Kind) {
	name := ""
	kind := KindOther
	if fi := d.feature(idx, 0x0005); fi != 0 {
		if r, err := d.request(idx, fi, 0, nil, 1500*time.Millisecond); err == nil {
			n := int(r[0])
			var sb []byte
			for len(sb) < n && n < 64 {
				c, err := d.request(idx, fi, 1, []byte{byte(len(sb))}, 1500*time.Millisecond)
				if err != nil {
					break
				}
				chunk := c
				if len(chunk) > n-len(sb) {
					chunk = chunk[:n-len(sb)]
				}
				if len(chunk) == 0 {
					break
				}
				sb = append(sb, chunk...)
			}
			name = strings.TrimSpace(strings.TrimRight(string(sb), "\x00"))
		}
		if r, err := d.request(idx, fi, 2, nil, 1500*time.Millisecond); err == nil {
			switch r[0] {
			case 0, 2:
				kind = KindKeyboard
			case 3, 4, 5:
				kind = KindMouse
			case 8:
				kind = KindHeadset
			}
		}
	}
	if name == "" {
		name = d.product
	}
	if kind == KindOther {
		kind = kindFromName(name + " " + d.product)
		if kind == KindOther && d.vendorPg == 0xFF43 {
			kind = KindHeadset
		}
	}
	if !strings.HasPrefix(strings.ToLower(name), "logi") && !strings.HasPrefix(strings.ToLower(name), "g") && !strings.HasPrefix(strings.ToLower(name), "mx") && name != "" {
		name = "Logitech " + name
	}
	return name, kind
}

type lgGroup struct {
	key                  string
	shortPath, longPath  string
	sIn, sOut, lIn, lOut int
	product              string
	page                 uint16
	containerKey         string
}

// readLongReg reads a HID++ 1.0 receiver register (e.g. 0xB5 pairing info).
func (d *hidppDev) readLongReg(addr, p0 byte) ([]byte, bool) {
	t := d.t
	var pkt []byte
	var ch *hidChan
	if t.short != nil && t.short.outLen == 7 {
		pkt, ch = make([]byte, 7), t.short
		pkt[0] = 0x10
	} else if t.long != nil {
		pkt, ch = make([]byte, 20), t.long
		pkt[0] = 0x11
	} else {
		return nil, false
	}
	pkt[1], pkt[2], pkt[3], pkt[4] = 0xFF, 0x83, addr, p0
	t.drain()
	if t.write(ch, pkt) != nil {
		return nil, false
	}
	p, ok := t.recv(1200*time.Millisecond, func(p []byte) bool {
		if len(p) < 7 || p[1] != 0xFF {
			return false
		}
		return (p[2] == 0x83 && p[3] == addr && p[4] == p0) || (p[2] == 0x8F && p[3] == 0x83 && p[4] == addr)
	})
	if !ok || p[2] == 0x8F {
		return nil, false
	}
	return p[4:], true
}

var miRe = regexp.MustCompile(`mi_[0-9a-f]{2}`)

type windowsGUID = windows.GUID
