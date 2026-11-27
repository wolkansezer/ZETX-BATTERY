//go:build windows

package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SteelSeries wireless receivers (2.4 GHz dongles / base stations) answer battery queries with
// vendor-specific HID commands. Command bytes and response offsets come from the open-source
// projects HeadsetControl (github.com/Sapd/HeadsetControl) and rivalcfg (github.com/flozz/rivalcfg).
//
// On Windows a HID read always starts with the report ID byte. Like hidapi, report ID 0 is
// stripped, so offsets below match the ones in those projects.

const ssVID = 0x1038

// steelSeriesModel returns the protocol for a SteelSeries collection.
func steelSeriesModel(h hidInfo) (ssModel, bool) {
	m, ok := ssModels[h.PID]
	if !ok {
		return m, false
	}
	m.brand = "SteelSeries"
	vendorOK := h.UsagePage >= 0xFF00 || (m.p.page == 0 && ifaceNum(h.Path) == m.p.iface)
	return m, vendorOK && h.OutLen >= len(m.p.cmd) && h.InLen >= 2
}

type ssResult struct {
	pct      int
	charging bool
	offline  bool
}

type ssProto struct {
	// query replaces the default single command/response exchange (multi-step protocols).
	query       func(d *ssDev, c hidInfo) (ssResult, []byte, bool)
	noTransport bool // protocol uses feature reports on its own handle (Razer)
	name        string
	kind        Kind
	iface       int    // USB interface number (mi_XX); -1 = any
	page        uint16 // 0 = any vendor page (>= 0xFF00)
	cmd         []byte // first byte = report ID
	echo        []byte // expected first bytes of the reply (after stripping report ID 0)
	parse       func(d []byte) (ssResult, bool)
}

func scale(v, lo, hi int) int {
	if hi <= lo {
		return v
	}
	return clampPct((v - lo) * 100 / (hi - lo))
}

func at(d []byte, i int) int {
	if i < len(d) {
		return int(d[i])
	}
	return -1
}

var (
	// Arctis 1 / 7X / 7P (first gen): {06 12} -> [3]=battery %, [2]=status (01 offline)
	pArctis1 = ssProto{kind: KindHeadset, iface: 3, page: 0xFF43, cmd: []byte{0x06, 0x12}, echo: []byte{0x06, 0x12},
		parse: func(d []byte) (ssResult, bool) {
			if at(d, 3) < 0 {
				return ssResult{}, false
			}
			return ssResult{pct: clampPct(at(d, 3)), offline: at(d, 2) == 0x01}, true
		}}
	// Arctis 7 / Pro 2019: {06 18} -> [2]=battery %
	pArctis7 = ssProto{kind: KindHeadset, iface: 5, page: 0xFF43, cmd: []byte{0x06, 0x18}, echo: []byte{0x06, 0x18},
		parse: func(d []byte) (ssResult, bool) {
			if at(d, 2) < 0 {
				return ssResult{}, false
			}
			return ssResult{pct: clampPct(at(d, 2))}, true
		}}
	// Arctis 9: {00 20} -> [3]=battery 0x64..0x9A, [4]=01 charging
	pArctis9 = ssProto{kind: KindHeadset, iface: 0, cmd: []byte{0x00, 0x20},
		parse: func(d []byte) (ssResult, bool) {
			if at(d, 4) < 0 {
				return ssResult{}, false
			}
			return ssResult{pct: scale(at(d, 3), 0x64, 0x9A), charging: at(d, 4) == 0x01}, true
		}}
	// Arctis 7+ family: {00 b0} -> [1]=01 offline, [2]=battery 0..4, [3]=01 charging
	pArctis7Plus = ssProto{kind: KindHeadset, iface: 3, page: 0xFFC0, cmd: []byte{0x00, 0xB0}, echo: []byte{0xB0},
		parse: func(d []byte) (ssResult, bool) {
			if at(d, 3) < 0 {
				return ssResult{}, false
			}
			return ssResult{pct: scale(at(d, 2), 0, 4), charging: at(d, 3) == 0x01, offline: at(d, 1) == 0x01}, true
		}}
	// Arctis Nova 7 / 7X / 7P, older firmware: {00 b0} -> [2]=battery 0..4, [3]=status (00 offline, 01/02 charging)
	pNova7Steps = ssProto{kind: KindHeadset, iface: 3, page: 0xFFC0, cmd: []byte{0x00, 0xB0}, echo: []byte{0xB0},
		parse: func(d []byte) (ssResult, bool) {
			if at(d, 3) < 0 {
				return ssResult{}, false
			}
			st := at(d, 3)
			return ssResult{pct: scale(at(d, 2), 0, 4), charging: st == 0x01 || st == 0x02, offline: st == 0x00}, true
		}}
	// Arctis Nova 7 Gen 2 / newer firmware: same reply, [2]=battery 0..100
	pNova7Pct = ssProto{kind: KindHeadset, iface: 3, page: 0xFFC0, cmd: []byte{0x00, 0xB0}, echo: []byte{0xB0},
		parse: func(d []byte) (ssResult, bool) {
			if at(d, 3) < 0 {
				return ssResult{}, false
			}
			st := at(d, 3)
			return ssResult{pct: clampPct(at(d, 2)), charging: st == 0x01 || st == 0x02, offline: st == 0x00}, true
		}}
	// Arctis Nova 5 / 5X / 3P / 3X: {00 b0} -> [1]=02 offline, [3]=battery %, [4]=01 charging
	pNova5 = ssProto{kind: KindHeadset, iface: 3, page: 0xFFC0, cmd: []byte{0x00, 0xB0}, echo: []byte{0xB0},
		parse: func(d []byte) (ssResult, bool) {
			if at(d, 4) < 0 {
				return ssResult{}, false
			}
			return ssResult{pct: clampPct(at(d, 3)), charging: at(d, 4) == 0x01, offline: at(d, 1) == 0x02}, true
		}}
	// Arctis Nova Pro Wireless base station: {06 b0} -> [6]=battery 0..8, [15]=status (01 offline, 02 charging)
	pNovaPro = ssProto{kind: KindHeadset, iface: 4, cmd: []byte{0x06, 0xB0}, echo: []byte{0x06, 0xB0},
		parse: func(d []byte) (ssResult, bool) {
			if at(d, 15) < 0 {
				return ssResult{}, false
			}
			return ssResult{pct: scale(at(d, 6), 0, 8), charging: at(d, 15) == 0x02, offline: at(d, 15) == 0x01}, true
		}}
	// Arctis Nova Pro Omni: {01 b0} -> [6]=battery %, [15]=status (01 offline, 02 charging)
	pNovaOmni = ssProto{kind: KindHeadset, iface: 3, page: 0xFFC0, cmd: []byte{0x01, 0xB0}, echo: []byte{0x01, 0xB0},
		parse: func(d []byte) (ssResult, bool) {
			if at(d, 15) < 0 || at(d, 6) > 100 {
				return ssResult{}, false
			}
			return ssResult{pct: at(d, 6), charging: at(d, 15) == 0x02, offline: at(d, 15) == 0x01}, true
		}}
	// Aerox / Prime wireless mice: cmd 0x92 (cable) or 0xD2 (2.4 GHz) -> [1]: bit7 charging, ((v&0x7F)-1)*5 = %
	mouseLevel = func(d []byte) (ssResult, bool) {
		v := at(d, 1)
		if v <= 0 {
			return ssResult{}, false
		}
		pct := ((v & 0x7F) - 1) * 5
		if pct < 0 || pct > 100 {
			return ssResult{}, false
		}
		return ssResult{pct: pct, charging: v&0x80 != 0}, true
	}
	pMouseWired    = ssProto{kind: KindMouse, iface: 3, cmd: []byte{0x00, 0x92}, parse: mouseLevel}
	pMouseWireless = ssProto{kind: KindMouse, iface: 3, cmd: []byte{0x00, 0xD2}, parse: mouseLevel}
	// Rival 3 Wireless / Rival 650: {00 AA 01} -> [0]=battery %, [2]=charging
	pRival = ssProto{kind: KindMouse, iface: 3, cmd: []byte{0x00, 0xAA, 0x01},
		parse: func(d []byte) (ssResult, bool) {
			if at(d, 2) < 0 || at(d, 0) > 100 {
				return ssResult{}, false
			}
			return ssResult{pct: at(d, 0), charging: at(d, 2) != 0}, true
		}}
)

type ssModel struct {
	name    string
	p       *ssProto
	brand   string
	kind    Kind
	kindSet bool
}

var ssModels = map[uint16]ssModel{
	0x12B3: {name: "Arctis 1 Wireless", p: &pArctis1}, 0x12B6: {name: "Arctis 1 Xbox", p: &pArctis1},
	0x12D7: {name: "Arctis 7X", p: &pArctis1}, 0x12D5: {name: "Arctis 7P", p: &pArctis1},
	0x1260: {name: "Arctis 7", p: &pArctis7}, 0x12AD: {name: "Arctis 7 (2019)", p: &pArctis7},
	0x1252: {name: "Arctis Pro (2019)", p: &pArctis7}, 0x1280: {name: "Arctis Pro GameDAC", p: &pArctis7},
	0x12C2: {name: "Arctis 9", p: &pArctis9}, 0x12C4: {name: "Arctis 9", p: &pArctis9},
	0x220E: {name: "Arctis 7+", p: &pArctis7Plus}, 0x2212: {name: "Arctis 7+ PS5", p: &pArctis7Plus},
	0x2216: {name: "Arctis 7+ Xbox", p: &pArctis7Plus}, 0x2236: {name: "Arctis 7+ Destiny", p: &pArctis7Plus},
	0x2202: {name: "Arctis Nova 7", p: &pNova7Steps}, 0x2206: {name: "Arctis Nova 7X", p: &pNova7Steps},
	0x223A: {name: "Arctis Nova 7 Diablo IV", p: &pNova7Steps}, 0x227A: {name: "Arctis Nova 7 WoW", p: &pNova7Steps},
	0x22A4: {name: "Arctis Nova 7X", p: &pNova7Steps}, 0x220A: {name: "Arctis Nova 7P", p: &pNova7Steps},
	0x22A1: {name: "Arctis Nova 7", p: &pNova7Pct}, 0x227E: {name: "Arctis Nova 7 Gen 2", p: &pNova7Pct},
	0x2258: {name: "Arctis Nova 7X v2", p: &pNova7Pct}, 0x229E: {name: "Arctis Nova 7X v2", p: &pNova7Pct},
	0x22AD: {name: "Arctis Nova 7X v2", p: &pNova7Pct}, 0x22A9: {name: "Arctis Nova 7 Diablo IV", p: &pNova7Pct},
	0x22A5: {name: "Arctis Nova 7X", p: &pNova7Pct}, 0x22A7: {name: "Arctis Nova 7P v2", p: &pNova7Pct},
	0x2298: {name: "Arctis Nova 7P v2", p: &pNova7Pct},
	0x2232: {name: "Arctis Nova 5", p: &pNova5}, 0x2253: {name: "Arctis Nova 5X", p: &pNova5},
	0x2269: {name: "Arctis Nova 3P Wireless", p: &pNova5}, 0x226D: {name: "Arctis Nova 3X Wireless", p: &pNova5},
	0x12E0: {name: "Arctis Nova Pro Wireless", p: &pNovaPro}, 0x12E5: {name: "Arctis Nova Pro Wireless X", p: &pNovaPro},
	0x2290: {name: "Arctis Nova Pro Omni", p: &pNovaOmni},
	0x183A: {name: "Aerox 3 Wireless", p: &pMouseWired}, 0x187A: {name: "Aerox 3 Wireless", p: &pMouseWired},
	0x1838: {name: "Aerox 3 Wireless", p: &pMouseWireless}, 0x1878: {name: "Aerox 3 Wireless", p: &pMouseWireless},
	0x1854: {name: "Aerox 5 Wireless", p: &pMouseWired}, 0x185E: {name: "Aerox 5 Wireless", p: &pMouseWired}, 0x1862: {name: "Aerox 5 Wireless", p: &pMouseWired},
	0x1852: {name: "Aerox 5 Wireless", p: &pMouseWireless}, 0x185C: {name: "Aerox 5 Wireless", p: &pMouseWireless}, 0x1860: {name: "Aerox 5 Wireless", p: &pMouseWireless},
	0x185A: {name: "Aerox 9 Wireless", p: &pMouseWired}, 0x1876: {name: "Aerox 9 Wireless", p: &pMouseWired},
	0x1858: {name: "Aerox 9 Wireless", p: &pMouseWireless}, 0x1874: {name: "Aerox 9 Wireless", p: &pMouseWireless},
	0x1842: {name: "Prime Wireless", p: &pMouseWired}, 0x184A: {name: "Prime Mini Wireless", p: &pMouseWired},
	0x1840: {name: "Prime Wireless", p: &pMouseWireless}, 0x1848: {name: "Prime Mini Wireless", p: &pMouseWireless},
	0x1830: {name: "Rival 3 Wireless", p: &pRival}, 0x1872: {name: "Rival 3 Wireless Gen 2", p: &pRival},
	0x172B: {name: "Rival 650 Wireless", p: &pRival}, 0x1726: {name: "Rival 650 Wireless", p: &pRival},
}

var miNumRe = regexp.MustCompile(`mi_([0-9a-f]{2})`)

func ifaceNum(path string) int {
	m := miNumRe.FindStringSubmatch(strings.ToLower(path))
	if m == nil {
		return -1
	}
	var n int
	fmt.Sscanf(m[1], "%x", &n)
	return n
}

type ssDev struct {
	key       string
	model     ssModel
	pid       uint16
	container string
	cands     []hidInfo // collections to try, best first
	working   int       // index into cands that answered, -1 = unknown
	t         *hidTransport
	tpath     string
	res       ssResult
	ok        bool
	fails     int
	misses    int // full rounds without any answer (wired-only or unsupported device)
}

type ssMonitor struct {
	devs map[string]*ssDev
}

func newSsMonitor() *ssMonitor { return &ssMonitor{devs: map[string]*ssDev{}} }

func (m *ssMonitor) refresh(hids []hidInfo) {
	found := map[string]*ssDev{}
	for _, h := range hids {
		model, ok := lookupModel(h)
		if !ok {
			if h.UsagePage >= 0xFF00 && knownVendor(h.VID) {
				logf("  %04X:%04X (desteklenmeyen model) sayfa=%04X mi=%d %s", h.VID, h.PID, h.UsagePage, ifaceNum(h.Path), h.Product)
			}
			continue
		}
		key := fmt.Sprintf("v:%04x:%04x:%s", h.VID, h.PID, h.Container.String())
		d := found[key]
		if d == nil {
			d = &ssDev{key: key, model: model, pid: h.PID, container: h.Container.String(), working: -1}
			found[key] = d
		}
		d.cands = append(d.cands, h)
	}
	for k, d := range found {
		p := d.model.p
		sort.SliceStable(d.cands, func(i, j int) bool { return candScore(d.cands[i], p) > candScore(d.cands[j], p) })
		if old, ok := m.devs[k]; ok && len(old.cands) == len(d.cands) {
			continue
		}
		logf("  %s %s (%04X) bulundu, %d koleksiyon", d.model.brand, d.model.name, d.pid, len(d.cands))
		if old, ok := m.devs[k]; ok && old.t != nil {
			old.t.Close()
		}
		m.devs[k] = d
	}
	for k, d := range m.devs {
		if _, ok := found[k]; !ok {
			if d.t != nil {
				d.t.Close()
			}
			delete(m.devs, k)
		}
	}
}

func candScore(h hidInfo, p *ssProto) int {
	s := 0
	if p.iface >= 0 && ifaceNum(h.Path) == p.iface {
		s += 4
	}
	if p.page != 0 && h.UsagePage == p.page {
		s += 2
	}
	return s
}

// poll re-reads devices; ones that never answered are retried only on full scans.
func (m *ssMonitor) poll(full bool) {
	for _, d := range m.devs {
		if !full && !d.ok && d.misses >= 2 {
			continue
		}
		d.read()
		if d.ok {
			d.misses = 0
		} else {
			d.misses++
		}
	}
}

func (d *ssDev) read() {
	order := []int{}
	if d.working >= 0 {
		order = append(order, d.working)
	}
	for i := range d.cands {
		if i != d.working {
			order = append(order, i)
		}
	}
	for _, i := range order {
		res, raw, ok := d.query(i)
		if ok {
			if !d.ok || res != d.res {
				logf("    %s %s [ham=% X] -> %%%d sarj=%v kapali=%v", d.model.brand, d.model.name, raw, res.pct, res.charging, res.offline)
			}
			d.working, d.res, d.ok, d.fails = i, res, true, 0
			return
		}
		if d.working == i { // the known-good collection stopped answering
			d.fails++
			if d.fails < 3 {
				return
			}
			d.working = -1
		}
	}
	if d.ok {
		d.fails++
		if d.fails >= 3 {
			d.ok = false
		}
	}
}

func (d *ssDev) query(i int) (ssResult, []byte, bool) {
	c := d.cands[i]
	p := d.model.p
	if p.noTransport {
		return p.query(d, c)
	}
	if d.t == nil || d.t.long == nil || d.tpath != c.Path {
		if d.t != nil {
			d.t.Close()
		}
		t, err := newTransport("", 0, 0, c.Path, c.InLen, c.OutLen)
		if err != nil {
			d.t, d.tpath = nil, ""
			return ssResult{}, nil, false
		}
		d.t, d.tpath = t, c.Path
	}
	if p.query != nil {
		return p.query(d, c)
	}
	var first []byte
	data, ok := d.xfer(c, p.cmd, func(data []byte) bool {
		if len(p.echo) > 0 && (len(data) < len(p.echo) || string(data[:len(p.echo)]) != string(p.echo)) {
			if first == nil {
				first = data
			}
			return false
		}
		_, ok := p.parse(data)
		return ok
	}, 700*time.Millisecond)
	if ok {
		r, _ := p.parse(data)
		return r, data, true
	}
	// Some firmwares don't echo the command: fall back to the first reply.
	if first != nil {
		if r, ok := p.parse(first); ok {
			return r, first, true
		}
	}
	return ssResult{}, nil, false
}

// xfer writes one output report (padded to the collection's length; pkt[0] = report ID) and returns
// the first reply accepted by match. Report ID 0 is stripped from replies, as hidapi does.
func (d *ssDev) xfer(c hidInfo, pkt []byte, match func([]byte) bool, timeout time.Duration) ([]byte, bool) {
	out := make([]byte, c.OutLen)
	copy(out, pkt)
	d.t.drain()
	if err := d.t.write(d.t.long, out); err != nil {
		return nil, false
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		raw, ok := d.t.recv(time.Until(deadline), func([]byte) bool { return true })
		if !ok {
			break
		}
		data := raw
		if len(data) > 0 && data[0] == 0 {
			data = data[1:]
		}
		if match(data) {
			return data, true
		}
	}
	return nil, false
}

func (m *ssMonitor) devices() (devs []Device, containers map[string]bool) {
	containers = map[string]bool{}
	for _, d := range m.devs {
		if !d.ok {
			continue
		}
		containers[d.container] = true
		name := d.model.name
		if d.model.brand != "" && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(d.model.brand)) {
			name = d.model.brand + " " + name
		}
		kind := d.model.p.kind
		if d.model.kindSet {
			kind = d.model.kind
		}
		dev := Device{Key: d.key, Name: name, Kind: kind, Percent: d.res.pct, Charging: d.res.charging, Online: !d.res.offline, Source: d.model.brand}
		if d.res.offline {
			dev.Percent, dev.Charging, dev.Source = -1, false, "kapalı / bağlı değil"
		}
		devs = append(devs, dev)
	}
	return
}
