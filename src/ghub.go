//go:build windows

package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
)

// G HUB exposes a local WebSocket API (ws://localhost:9010). When G HUB is running we read the
// battery percentage exactly as G HUB shows it (G HUB converts headset voltage with per-model tables).

type ghubDev struct {
	ID       string
	Name     string
	Type     string // HEADSET, MOUSE, KEYBOARD ...
	Pct      int
	Charging bool
	HasBatt  bool
	Updated  time.Time
}

type ghubClient struct {
	mu        sync.Mutex
	devs      map[string]*ghubDev
	connected bool
}

var ghub = &ghubClient{devs: map[string]*ghubDev{}}

func (g *ghubClient) snapshot() (out []ghubDev, connected bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, d := range g.devs {
		if d.HasBatt && d.Pct >= 0 && time.Since(d.Updated) < 10*time.Minute {
			out = append(out, *d)
		}
	}
	return out, g.connected
}

func (g *ghubClient) run() {
	for {
		err := g.session()
		g.mu.Lock()
		g.connected = false
		g.mu.Unlock()
		if err != nil {
			time.Sleep(10 * time.Second)
		}
	}
}

func (g *ghubClient) session() error {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:9010", 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	key := make([]byte, 16)
	rand.Read(key)
	req := "GET / HTTP/1.1\r\nHost: localhost:9010\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\nSec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Protocol: json\r\nOrigin: file://\r\n\r\n"
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(req)); err != nil {
		return err
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		return fmt.Errorf("g hub handshake: %q %v", status, err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return err
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}
	conn.SetDeadline(time.Time{})
	g.mu.Lock()
	g.connected = true
	g.mu.Unlock()
	logf("G HUB baglantisi kuruldu")

	var wmu sync.Mutex
	send := func(verb, path string) error {
		b, _ := json.Marshal(map[string]string{"msgId": "", "verb": verb, "path": path})
		wmu.Lock()
		defer wmu.Unlock()
		return wsWrite(conn, 0x1, b)
	}
	send("SUBSCRIBE", "/devices/state/changed")
	send("SUBSCRIBE", "/battery/state/changed")
	send("GET", "/devices/list")

	done := make(chan struct{})
	defer close(done)
	go func() { // refresh battery states regularly
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				g.mu.Lock()
				ids := make([]string, 0, len(g.devs))
				for id := range g.devs {
					ids = append(ids, id)
				}
				g.mu.Unlock()
				for _, id := range ids {
					if send("GET", "/battery/"+id+"/state") != nil {
						return
					}
				}
			}
		}
	}()

	for {
		op, msg, err := wsRead(br)
		if err != nil {
			return err
		}
		switch op {
		case 0x8:
			return errors.New("closed")
		case 0x9:
			wmu.Lock()
			wsWrite(conn, 0xA, msg)
			wmu.Unlock()
			continue
		case 0x1:
		default:
			continue
		}
		var m struct {
			Verb    string          `json:"verb"`
			Path    string          `json:"path"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(msg, &m) != nil {
			continue
		}
		switch {
		case m.Path == "/devices/list" || m.Path == "/devices/state/changed":
			for _, id := range g.parseDevices(m.Payload) {
				send("GET", "/battery/"+id+"/state")
			}
		case strings.HasPrefix(m.Path, "/battery/"):
			g.parseBattery(m.Path, m.Payload)
		}
	}
}

func (g *ghubClient) parseDevices(p json.RawMessage) (ids []string) {
	var v struct {
		DeviceInfos []struct {
			ID                  string `json:"id"`
			DeviceType          string `json:"deviceType"`
			DisplayName         string `json:"displayName"`
			ExtendedDisplayName string `json:"extendedDisplayName"`
			Capabilities        struct {
				HasBatteryStatus bool `json:"hasBatteryStatus"`
			} `json:"capabilities"`
		} `json:"deviceInfos"`
		// single-device events
		ID          string `json:"id"`
		DeviceType  string `json:"deviceType"`
		DisplayName string `json:"displayName"`
		State       string `json:"state"`
	}
	if json.Unmarshal(p, &v) != nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, d := range v.DeviceInfos {
		if d.ID == "" {
			continue
		}
		name := d.DisplayName
		if name == "" {
			name = d.ExtendedDisplayName
		}
		gd := g.devs[d.ID]
		if gd == nil {
			gd = &ghubDev{ID: d.ID, Pct: -1}
			g.devs[d.ID] = gd
		}
		gd.Name, gd.Type = name, strings.ToUpper(d.DeviceType)
		gd.HasBatt = gd.HasBatt || d.Capabilities.HasBatteryStatus
		logf("G HUB cihaz: %s (%s) pil=%v", name, d.DeviceType, d.Capabilities.HasBatteryStatus)
		ids = append(ids, d.ID)
	}
	if v.ID != "" {
		if gd := g.devs[v.ID]; gd != nil && strings.Contains(strings.ToLower(v.State), "not_connected") {
			gd.Pct = -1
		}
		ids = append(ids, v.ID)
	}
	return ids
}

func (g *ghubClient) parseBattery(path string, p json.RawMessage) {
	var v map[string]any
	if json.Unmarshal(p, &v) != nil {
		return
	}
	id, _ := v["deviceId"].(string)
	if id == "" {
		parts := strings.Split(strings.Trim(path, "/"), "/") // battery/<id>/state
		if len(parts) >= 2 {
			id = parts[1]
		}
	}
	pctF, ok := v["percentage"].(float64)
	if !ok || id == "" {
		return
	}
	chg := false
	for _, k := range []string{"charging", "isCharging"} {
		if b, ok := v[k].(bool); ok && b {
			chg = true
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	gd := g.devs[id]
	if gd == nil {
		gd = &ghubDev{ID: id}
		g.devs[id] = gd
	}
	if gd.Pct != int(pctF) || gd.Charging != chg {
		logf("G HUB pil: %s %%%d sarj=%v", gd.Name, int(pctF), chg)
	}
	gd.Pct, gd.Charging, gd.HasBatt, gd.Updated = int(pctF), chg, true, time.Now()
}

// ---- minimal WebSocket client framing (RFC 6455) ----

func wsWrite(w io.Writer, op byte, payload []byte) error {
	hdr := []byte{0x80 | op}
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n < 65536:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, uint64(n))
		hdr = append(append(hdr, 0x80|127), b...)
	}
	mask := make([]byte, 4)
	rand.Read(mask)
	hdr = append(hdr, mask...)
	body := make([]byte, n)
	for i := range payload {
		body[i] = payload[i] ^ mask[i%4]
	}
	_, err := w.Write(append(hdr, body...))
	return err
}

func wsRead(r *bufio.Reader) (byte, []byte, error) {
	var msg []byte
	var op byte
	for {
		h := make([]byte, 2)
		if _, err := io.ReadFull(r, h); err != nil {
			return 0, nil, err
		}
		fin := h[0]&0x80 != 0
		o := h[0] & 0x0F
		n := uint64(h[1] & 0x7F)
		if n == 126 {
			b := make([]byte, 2)
			if _, err := io.ReadFull(r, b); err != nil {
				return 0, nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b))
		} else if n == 127 {
			b := make([]byte, 8)
			if _, err := io.ReadFull(r, b); err != nil {
				return 0, nil, err
			}
			n = binary.BigEndian.Uint64(b)
		}
		var mask []byte
		if h[1]&0x80 != 0 {
			mask = make([]byte, 4)
			if _, err := io.ReadFull(r, mask); err != nil {
				return 0, nil, err
			}
		}
		if n > 16<<20 {
			return 0, nil, errors.New("frame too large")
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(r, data); err != nil {
			return 0, nil, err
		}
		if mask != nil {
			for i := range data {
				data[i] ^= mask[i%4]
			}
		}
		if o >= 0x8 { // control frame
			return o, data, nil
		}
		if o != 0 {
			op = o
		}
		msg = append(msg, data...)
		if fin {
			return op, msg, nil
		}
	}
}

// ---- matching G HUB devices with HID++ devices ----

var nonAlnum = regexp.MustCompile(`[^A-Z0-9]+`)

func compactName(s string) string {
	u := strings.ToUpper(s)
	for _, w := range []string{"LOGITECH", "GAMING", "HEADSET", "WIRELESS", "LIGHTSPEED", "MOUSE", "KEYBOARD", "RGB"} {
		u = strings.ReplaceAll(u, w, " ")
	}
	return nonAlnum.ReplaceAllString(u, "")
}

func ghubKind(t string) Kind {
	switch {
	case strings.Contains(t, "HEADSET"):
		return KindHeadset
	case strings.Contains(t, "MOUSE"):
		return KindMouse
	case strings.Contains(t, "KEYBOARD"):
		return KindKeyboard
	}
	return KindOther
}

// applyGHub overrides Logitech readings with G HUB's numbers so both apps always show the same value.
func applyGHub(devs []Device) []Device {
	list, ok := ghub.snapshot()
	if !ok || len(list) == 0 {
		return devs
	}
	for _, g := range list {
		gn := compactName(g.Name)
		gk := ghubKind(g.Type)
		best := -1
		for i, d := range devs {
			if !strings.HasPrefix(d.Key, "lg:") {
				continue
			}
			dn := compactName(d.Name)
			if len(gn) >= 3 && len(dn) >= 3 && (strings.Contains(dn, gn) || strings.Contains(gn, dn)) {
				best = i
				break
			}
		}
		if best < 0 && gk != KindOther { // only one Logitech device of that kind: match it
			n := 0
			for i, d := range devs {
				if strings.HasPrefix(d.Key, "lg:") && d.Kind == gk {
					best, n = i, n+1
				}
			}
			if n != 1 {
				best = -1
			}
		}
		if best >= 0 {
			devs[best].Percent, devs[best].Charging, devs[best].Online = g.Pct, g.Charging, true
			devs[best].Source = "G HUB"
			continue
		}
		k := gk
		if k == KindOther {
			k = kindFromName(g.Name)
		}
		devs = append(devs, Device{Key: "gh:" + g.ID, Name: "Logitech " + g.Name, Kind: k, Percent: g.Pct, Charging: g.Charging, Online: true, Source: "G HUB"})
	}
	return devs
}
