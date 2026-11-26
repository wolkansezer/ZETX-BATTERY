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

type ssResult struct {
	pct      int
	charging bool
	offline  bool
}

type ssProto struct {
	name  string
	kind  Kind
	iface int    // USB interface number (mi_XX); -1 = any
	page  uint16 // 0 = any vendor page (>= 0xFF00)
	cmd   []byte // first byte = report ID
	echo  []byte // expected first bytes of the reply (after stripping report ID 0)
	parse func(d []byte) (ssResult, bool)
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
	name string
	p    *ssProto
}

var ssModels = map[uint16]ssModel{
	0x12B3: {"Arctis 1 Wireless", &pArctis1}, 0x12B6: {"Arctis 1 Xbox", &pArctis1},
	0x12D7: {"Arctis 7X", &pArctis1}, 0x12D5: {"Arctis 7P", &pArctis1},
	0x1260: {"Arctis 7", &pArctis7}, 0x12AD: {"Arctis 7 (2019)", &pArctis7},
	0x1252: {"Arctis Pro (2019)", &pArctis7}, 0x1280: {"Arctis Pro GameDAC", &pArctis7},
	0x12C2: {"Arctis 9", &pArctis9}, 0x12C4: {"Arctis 9", &pArctis9},
	0x220E: {"Arctis 7+", &pArctis7Plus}, 0x2212: {"Arctis 7+ PS5", &pArctis7Plus},
	0x2216: {"Arctis 7+ Xbox", &pArctis7Plus}, 0x2236: {"Arctis 7+ Destiny", &pArctis7Plus},
	0x2202: {"Arctis Nova 7", &pNova7Steps}, 0x2206: {"Arctis Nova 7X", &pNova7Steps},
	0x223A: {"Arctis Nova 7 Diablo IV", &pNova7Steps}, 0x227A: {"Arctis Nova 7 WoW", &pNova7Steps},
	0x22A4: {"Arctis Nova 7X", &pNova7Steps}, 0x220A: {"Arctis Nova 7P", &pNova7Steps},
	0x22A1: {"Arctis Nova 7", &pNova7Pct}, 0x227E: {"Arctis Nova 7 Gen 2", &pNova7Pct},
	0x2258: {"Arctis Nova 7X v2", &pNova7Pct}, 0x229E: {"Arctis Nova 7X v2", &pNova7Pct},
	0x22AD: {"Arctis Nova 7X v2", &pNova7Pct}, 0x22A9: {"Arctis Nova 7 Diablo IV", &pNova7Pct},
	0x22A5: {"Arctis Nova 7X", &pNova7Pct}, 0x22A7: {"Arctis Nova 7P v2", &pNova7Pct},
	0x2298: {"Arctis Nova 7P v2", &pNova7Pct},
	0x2232: {"Arctis Nova 5", &pNova5}, 0x2253: {"Arctis Nova 5X", &pNova5},
	0x2269: {"Arctis Nova 3P Wireless", &pNova5}, 0x226D: {"Arctis Nova 3X Wireless", &pNova5},
	0x12E0: {"Arctis Nova Pro Wireless", &pNovaPro}, 0x12E5: {"Arctis Nova Pro Wireless X", &pNovaPro},
	0x2290: {"Arctis Nova Pro Omni", &pNovaOmni},
	0x183A: {"Aerox 3 Wireless", &pMouseWired}, 0x187A: {"Aerox 3 Wireless", &pMouseWired},
	0x1838: {"Aerox 3 Wireless", &pMouseWireless}, 0x1878: {"Aerox 3 Wireless", &pMouseWireless},
	0x1854: {"Aerox 5 Wireless", &pMouseWired}, 0x185E: {"Aerox 5 Wireless", &pMouseWired}, 0x1862: {"Aerox 5 Wireless", &pMouseWired},
	0x1852: {"Aerox 5 Wireless", &pMouseWireless}, 0x185C: {"Aerox 5 Wireless", &pMouseWireless}, 0x1860: {"Aerox 5 Wireless", &pMouseWireless},
	0x185A: {"Aerox 9 Wireless", &pMouseWired}, 0x1876: {"Aerox 9 Wireless", &pMouseWired},
	0x1858: {"Aerox 9 Wireless", &pMouseWireless}, 0x1874: {"Aerox 9 Wireless", &pMouseWireless},
	0x1842: {"Prime Wireless", &pMouseWired}, 0x184A: {"Prime Mini Wireless", &pMouseWired},
	0x1840: {"Prime Wireless", &pMouseWireless}, 0x1848: {"Prime Mini Wireless", &pMouseWireless},
	0x1830: {"Rival 3 Wireless", &pRival}, 0x1872: {"Rival 3 Wireless Gen 2", &pRival},
	0x172B: {"Rival 650 Wireless", &pRival}, 0x1726: {"Rival 650 Wireless", &pRival},
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
}

type ssMonitor struct {
	devs map[string]*ssDev
}

func newSsMonitor() *ssMonitor { return &ssMonitor{devs: map[string]*ssDev{}} }

func (m *ssMonitor) refresh(hids []hidInfo) {
	found := map[string]*ssDev{}
	for _, h := range hids {
		if h.VID != ssVID {
			continue
		}
		model, ok := ssModels[h.PID]
		if !ok {
			if h.UsagePage >= 0xFF00 {
				logf("  SteelSeries 1038:%04X (desteklenmeyen model) sayfa=%04X mi=%d %s", h.PID, h.UsagePage, ifaceNum(h.Path), h.Product)
			}
			continue
		}
		vendorOK := h.UsagePage >= 0xFF00 || (model.p.page == 0 && ifaceNum(h.Path) == model.p.iface)
		if !vendorOK || h.OutLen < len(model.p.cmd) || h.InLen < 2 {
			continue
		}
		key := fmt.Sprintf("ss:%04x:%s", h.PID, h.Container.String())
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
		logf("  SteelSeries %s (1038:%04X) bulundu, %d koleksiyon", d.model.name, d.pid, len(d.cands))
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

func (m *ssMonitor) poll() {
	for _, d := range m.devs {
		d.read()
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
				logf("    SteelSeries %s [ham=% X] -> %%%d sarj=%v kapali=%v", d.model.name, raw, res.pct, res.charging, res.offline)
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
	p := d.model.p
	pkt := make([]byte, c.OutLen)
	copy(pkt, p.cmd)
	d.t.drain()
	if err := d.t.write(d.t.long, pkt); err != nil {
		return ssResult{}, nil, false
	}
	var first []byte
	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		raw, ok := d.t.recv(time.Until(deadline), func([]byte) bool { return true })
		if !ok {
			break
		}
		data := raw
		if len(data) > 0 && data[0] == 0 {
			data = data[1:]
		}
		if len(p.echo) > 0 {
			if len(data) < len(p.echo) || string(data[:len(p.echo)]) != string(p.echo) {
				if first == nil {
					first = data
				}
				continue
			}
		}
		if r, ok := p.parse(data); ok {
			return r, data, true
		}
	}
	// Some firmwares don't echo the command: fall back to the first reply.
	if first != nil {
		if r, ok := p.parse(first); ok {
			return r, first, true
		}
	}
	return ssResult{}, nil, false
}

func (m *ssMonitor) devices() (devs []Device, containers map[string]bool) {
	containers = map[string]bool{}
	for _, d := range m.devs {
		if !d.ok {
			continue
		}
		containers[d.container] = true
		dev := Device{Key: d.key, Name: "SteelSeries " + d.model.name, Kind: d.model.p.kind, Percent: d.res.pct, Charging: d.res.charging, Online: !d.res.offline, Source: "SteelSeries"}
		if d.res.offline {
			dev.Percent, dev.Charging, dev.Source = -1, false, "kapalı / bağlı değil"
		}
		devs = append(devs, dev)
	}
	return
}
