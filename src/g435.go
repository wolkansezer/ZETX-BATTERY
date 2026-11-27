//go:build windows

package main

import (
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Logitech G435. Unlike other Logitech G headsets it does not speak HID++: its LIGHTSPEED dongle
// only bridges "PA" commands to the Airoha/Audiowise radio chip, and the battery comes back as a
// voltage. There are two dongle revisions with different transports:
//
//   - 046D:0ADF (Holtek): 64-byte reports on usage page 0xFF00, framed "cmd len crc16 data".
//     Protocol from DsGrilo/G435-Battery-app (MIT, tested on hardware) and
//     portyporty/g435-battery-percentage.
//   - 046D:0ACB (Realtek, the common one): report 6 on usage page 0xFFC0. The bridge must be put
//     into its "bootloader" mode for the exchange, and the dongle's firmware stops audio and
//     microphone for about half a second while it is there. Protocol from
//     deddroid/logitech-g435-battery (MIT, docs/PROTOCOL.md, tested on hardware) and endes' research.
//     Because of the audio gap this dongle is read only every 30 minutes, and reading can be
//     switched off from the menu.
//
// PA command: 50 41 <category> <seq> <len> <command...>; reply frames start with 50 49:
//   'P' 'I' <type> <seq> <timestamp u32> <len u16> <payload>
//   wireless (0x0E) get reply payload: <cmd> <value>;  battery (OTA 0x06, cmd 00 31) payload:
//   06 <seq|80> 00 <mV lo> <mV hi>

const (
	pidG435ACB = 0x0ACB
	pidG435ADF = 0x0ADF
)

var (
	g435ACBSeen   atomic.Bool // a 0ACB dongle is present (the menu then shows the switch)
	g435ACBPaused atomic.Bool // user switched off reading on the 0ACB dongle
	g435Mu        sync.Mutex
	g435Cache     = map[string]g435Entry{}
)

type g435Entry struct {
	res  ssResult
	raw  []byte
	ok   bool
	next time.Time
}

// g435Model is called from lookupModel for Logitech collections.
func g435Model(h hidInfo) (ssModel, bool) {
	switch {
	case h.PID == pidG435ADF && h.UsagePage == 0xFF00 && h.OutLen >= 64:
		return ssModel{name: "G435", brand: "Logitech", p: &pG435ADF}, true
	case h.PID == pidG435ACB && h.UsagePage == 0xFFC0 && h.FeatLen >= 32 && h.OutLen >= 64:
		g435ACBSeen.Store(true)
		return ssModel{name: "G435", brand: "Logitech", p: &pG435ACB}, true
	}
	return ssModel{}, false
}

// g435Throttled wraps a reader so the dongle is asked rarely; in between the last answer is shown.
func g435Throttled(read func(c hidInfo) (ssResult, []byte, bool), okEvery, offEvery, failEvery time.Duration, paused *atomic.Bool) func(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
	return func(d *ssDev, c hidInfo) (ssResult, []byte, bool) {
		g435Mu.Lock()
		e, have := g435Cache[d.key]
		g435Mu.Unlock()
		if have && (time.Now().Before(e.next) || (paused != nil && paused.Load())) {
			return e.res, e.raw, e.ok
		}
		if !have && paused != nil && paused.Load() {
			return ssResult{}, nil, false
		}
		res, raw, ok := read(c)
		wait := failEvery
		switch {
		case ok && res.offline:
			wait = offEvery
		case ok:
			wait = okEvery
		case have && e.ok && !e.res.offline:
			// one failed read does not hide a level we already know
			res, raw, ok = e.res, e.raw, true
		}
		g435Mu.Lock()
		g435Cache[d.key] = g435Entry{res: res, raw: raw, ok: ok, next: time.Now().Add(wait)}
		g435Mu.Unlock()
		return res, raw, ok
	}
}

var (
	pG435ADF = ssProto{kind: KindHeadset, iface: -1, page: 0xFF00, noTransport: true,
		query: g435Throttled(g435ReadADF, 5*time.Minute, time.Minute, 2*time.Minute, nil)}
	pG435ACB = ssProto{kind: KindHeadset, iface: -1, page: 0xFFC0, noTransport: true,
		query: g435Throttled(g435ReadACB, 30*time.Minute, 3*time.Minute, 10*time.Minute, &g435ACBPaused)}
)

func g435PA(category, seq, length byte, payload ...byte) []byte {
	return append([]byte{0x50, 0x41, category, seq, length}, payload...)
}

func g435BatteryMV(buf []byte) (int, bool) {
	// <len 07 00> 06 <seq|80> 00 <mV lo> <mV hi>
	for i := 0; i+7 <= len(buf); i++ {
		w := buf[i:]
		if w[0] == 0x07 && w[1] == 0x00 && w[2] == 0x06 && w[3]&0x80 != 0 && w[4] == 0x00 {
			return int(w[5]) | int(w[6])<<8, true
		}
	}
	// fallback: walk the "PI" frame (10-byte header, then the payload)
	for i := 0; i+15 <= len(buf); i++ {
		if buf[i] == 0x50 && buf[i+1] == 0x49 {
			p := buf[i+10:]
			if p[0] == 0x06 && p[1]&0x80 != 0 && p[2] == 0x00 {
				return int(p[3]) | int(p[4])<<8, true
			}
		}
	}
	return 0, false
}

func g435RemoteValue(buf []byte, cmd byte) (byte, bool) {
	// <len 02 00> <cmd> <value>
	for i := 0; i+4 <= len(buf); i++ {
		if buf[i] == 0x02 && buf[i+1] == 0x00 && buf[i+2] == cmd {
			return buf[i+3], true
		}
	}
	for i := 0; i+12 <= len(buf); i++ {
		if buf[i] == 0x50 && buf[i+1] == 0x49 && buf[i+2] == 0x0E && buf[i+10] == cmd {
			return buf[i+11], true
		}
	}
	return 0, false
}

// g435Resume makes the next poll read the dongle again (after the user re-enabled reading).
func g435Resume() {
	g435Mu.Lock()
	for k, e := range g435Cache {
		e.next = time.Time{}
		g435Cache[k] = e
	}
	g435Mu.Unlock()
}

func g435PlausibleMV(mv int) bool { return mv >= 2500 && mv <= 4600 }

// g435Percent estimates the charge from the resting Li-Po voltage (curve of deddroid's project).
func g435Percent(mv int) int {
	curve := [][2]int{{4200, 100}, {4150, 95}, {4100, 90}, {4050, 85}, {4000, 80}, {3950, 75}, {3900, 70}, {3850, 62},
		{3800, 55}, {3750, 45}, {3700, 35}, {3650, 25}, {3600, 15}, {3550, 10}, {3500, 5}, {3400, 0}}
	if mv >= curve[0][0] {
		return 100
	}
	for i := 0; i+1 < len(curve); i++ {
		hi, lo := curve[i], curve[i+1]
		if mv >= lo[0] {
			return lo[1] + (mv-lo[0])*(hi[1]-lo[1])/(hi[0]-lo[0])
		}
	}
	return 0
}

func crc16XModem(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// ---------------- 046D:0ADF (Holtek bridge) ----------------

func g435ReadADF(c hidInfo) (ssResult, []byte, bool) {
	t, err := newTransport("", 0, 0, c.Path, c.InLen, c.OutLen)
	if err != nil {
		return ssResult{}, nil, false
	}
	defer t.Close()
	send := func(hidCmd byte, data []byte) bool {
		out := make([]byte, c.OutLen) // report ID 0 | cmd | len | crc lo | crc hi | data
		out[1], out[2] = hidCmd, byte(len(data))
		copy(out[5:], data)
		crc := crc16XModem(out[:65])
		out[3], out[4] = byte(crc), byte(crc>>8)
		return t.write(t.long, out) == nil
	}
	seq := byte(0)
	pa := func(category, length byte, payload ...byte) bool {
		seq = seq%127 + 1
		t.drain()
		return send(0x80, g435PA(category, seq, length, payload...))
	}
	wait := func(d time.Duration, match func([]byte) bool) bool {
		_, ok := t.recv(d, match)
		return ok
	}
	getMode := func() (byte, bool) {
		var mode byte
		if !pa(0x0E, 1, 0xE0) {
			return 0, false
		}
		ok := wait(1200*time.Millisecond, func(r []byte) bool {
			v, ok := g435RemoteValue(r, 0xE0)
			mode = v
			return ok
		})
		return mode, ok
	}
	if c.OutLen < 65 {
		return ssResult{}, nil, false
	}
	send(0x85, []byte{1}) // bridge power saving off, as Logitech's own tool does on connect
	time.Sleep(50 * time.Millisecond)
	mode, ok := getMode()
	if !ok {
		return ssResult{}, nil, false // dongle does not answer
	}
	if mode != 1 {
		// remote mode 1 routes the battery question to the headset; restored afterwards
		pa(0x0E, 2, 0xE1, 0x01)
		time.Sleep(120 * time.Millisecond)
		defer func() {
			pa(0x0E, 2, 0xE1, 0x00)
			time.Sleep(60 * time.Millisecond)
		}()
	}
	mv := 0
	if !pa(0x06, 1, 0x00, 0x31) {
		return ssResult{}, nil, false
	}
	got := wait(1500*time.Millisecond, func(r []byte) bool {
		v, ok := g435BatteryMV(r)
		mv = v
		return ok
	})
	raw := []byte{byte(mv), byte(mv >> 8)}
	if !got || !g435PlausibleMV(mv) {
		return ssResult{offline: true}, raw, true // dongle is there, headset is off or not linked
	}
	return ssResult{pct: g435Percent(mv)}, raw, true
}

// ---------------- 046D:0ACB (Realtek bridge) ----------------

func g435ReadACB(c hidInfo) (ssResult, []byte, bool) {
	if c.FeatLen < 16 || c.OutLen < 16 {
		return ssResult{}, nil, false
	}
	t, err := newTransport("", 0, 0, c.Path, c.InLen, c.OutLen)
	if err != nil {
		return ssResult{}, nil, false
	}
	defer t.Close()
	fh, err := openHID(c.Path, windows.GENERIC_READ|windows.GENERIC_WRITE, false)
	if err != nil {
		return ssResult{}, nil, false
	}
	defer windows.CloseHandle(fh)

	// bridge sends one bridge message as the output report and waits for its ack on the input
	// endpoint: 06 5a = ok, 06 fe = already in bootloader.
	bridge := func(op0, op1 byte, body []byte) (byte, bool) {
		out := make([]byte, c.OutLen)
		out[0], out[1], out[2] = 0x06, op0, op1
		copy(out[3:], body)
		t.drain()
		if t.write(t.long, out) != nil {
			return 0, false
		}
		r, ok := t.recv(800*time.Millisecond, func(r []byte) bool { return len(r) >= 2 && r[0] == 0x06 })
		if !ok {
			return 0, false
		}
		return r[1], true
	}
	getFeature := func() ([]byte, bool) {
		buf := make([]byte, c.FeatLen)
		buf[0] = 0x06
		if ok, _, _ := pHidD_GetFeature.Call(uintptr(fh), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); ok == 0 {
			return nil, false
		}
		return buf, true
	}

	if _, ok := bridge(0x01, 0x03, nil); !ok {
		bridge(0x08, 0x03, nil) // make sure the bridge is not left in the bootloader
		return ssResult{}, nil, false
	}
	// Always leave the bootloader: audio stays off for as long as the bridge is in it.
	defer bridge(0x08, 0x03, nil)

	for i := 0; i < 16; i++ { // drop stale replies
		if b, ok := getFeature(); !ok || len(b) < 2 || b[1] == 0xFF {
			break
		}
	}
	seq := byte(0)
	query := func(category, length byte, payload []byte, parse func([]byte) bool) bool {
		seq = seq%127 + 1
		cmd := g435PA(category, seq, length, payload...)
		msg := append([]byte{byte(len(cmd)), byte(len(cmd) >> 8)}, cmd...)
		if _, ok := bridge(0x04, 0x03, msg); !ok {
			return false
		}
		deadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline) {
			b, ok := getFeature()
			if !ok {
				return false
			}
			if len(b) > 1 && b[1] != 0xFF {
				if parse(b) {
					return true
				}
				continue
			}
			time.Sleep(30 * time.Millisecond)
		}
		return false
	}
	// 1) Is the headset connected? (The battery question is never sent first: sent first it has
	//    been seen to leave the dongle silent until it is replugged.)
	var connected byte = 0xFF
	if !query(0x0E, 1, []byte{0xE3}, func(b []byte) bool {
		v, ok := g435RemoteValue(b, 0xE3)
		connected = v
		return ok
	}) {
		return ssResult{}, nil, false
	}
	if connected == 0 {
		return ssResult{offline: true}, []byte{0}, true
	}
	mv := 0
	battery := func() bool {
		return query(0x06, 1, []byte{0x00, 0x31}, func(b []byte) bool {
			v, ok := g435BatteryMV(b)
			mv = v
			return ok
		})
	}
	// 2) Battery. In remote mode 0 the dongle answers itself with a meaningless small value:
	//    then switch to remote mode 1 and ask once more.
	if !(battery() && g435PlausibleMV(mv)) {
		query(0x0E, 2, []byte{0xE1, 0x01}, func([]byte) bool { return true })
		if !(battery() && g435PlausibleMV(mv)) {
			return ssResult{}, nil, false
		}
	}
	return ssResult{pct: g435Percent(mv)}, []byte{byte(mv), byte(mv >> 8)}, true
}
