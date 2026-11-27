//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	pHidD_GetHidGuid        = hiddll.NewProc("HidD_GetHidGuid")
	pHidD_GetAttributes     = hiddll.NewProc("HidD_GetAttributes")
	pHidD_GetPreparsedData  = hiddll.NewProc("HidD_GetPreparsedData")
	pHidD_FreePreparsedData = hiddll.NewProc("HidD_FreePreparsedData")
	pHidD_GetProductString  = hiddll.NewProc("HidD_GetProductString")
	pHidD_GetInputReport    = hiddll.NewProc("HidD_GetInputReport")
	pHidP_GetCaps           = hiddll.NewProc("HidP_GetCaps")
	pHidP_GetValueCaps      = hiddll.NewProc("HidP_GetValueCaps")
	pHidP_GetUsageValue     = hiddll.NewProc("HidP_GetUsageValue")
)

const hidpStatusSuccess = 0x00110000

type HIDD_ATTRIBUTES struct {
	Size          uint32
	VendorID      uint16
	ProductID     uint16
	VersionNumber uint16
}

type HIDP_CAPS struct {
	Usage                     uint16
	UsagePage                 uint16
	InputReportByteLength     uint16
	OutputReportByteLength    uint16
	FeatureReportByteLength   uint16
	Reserved                  [17]uint16
	NumberLinkCollectionNodes uint16
	NumberInputButtonCaps     uint16
	NumberInputValueCaps      uint16
	NumberInputDataIndices    uint16
	NumberOutputButtonCaps    uint16
	NumberOutputValueCaps     uint16
	NumberOutputDataIndices   uint16
	NumberFeatureButtonCaps   uint16
	NumberFeatureValueCaps    uint16
	NumberFeatureDataIndices  uint16
}

type batteryCap struct {
	ReportID   byte
	Page       uint16
	Usage      uint16
	LinkColl   uint16
	LogicalMin int32
	LogicalMax int32
}

type hidInfo struct {
	Path      string
	Container windows.GUID
	VID, PID  uint16
	UsagePage uint16
	Usage     uint16
	InLen     int
	OutLen    int
	FeatLen   int
	Product   string
	Battery   *batteryCap // standard HID battery value in an input report
	FeatBatt  *batteryCap // the same value in a feature report
	BT        bool        // collection of a Bluetooth / Bluetooth LE device
}

// isBTPath reports whether a HID path belongs to a Bluetooth (classic or LE) device. Every request
// to such a device goes over the air and shares the radio with its input reports, so asking too
// often lowers the polling rate of a Bluetooth mouse. Bluetooth devices are therefore read rarely.
func isBTPath(path string) bool {
	p := strings.ToLower(path)
	return strings.Contains(p, "{00001124-0000-1000-8000-00805f9b34fb}") || // classic BT HID
		strings.Contains(p, "{00001812-0000-1000-8000-00805f9b34fb}") || // BLE HID (HOGP)
		strings.Contains(p, "bthenum") || strings.Contains(p, "bthledevice")
}

// btEvery is the shortest interval between two over-the-air reads of one Bluetooth device.
const btEvery = 5 * time.Minute

// productCache keeps HID product strings so they are asked from each device only once.
var productCache = map[string]string{}

// batteryUsage reports whether a value cap is a battery level: HID Battery Strength (0x06/0x20)
// or Battery System Remaining Capacity / Absolute State Of Charge (0x85/0x66, 0x85/0x65).
func batteryUsage(page, usage uint16) bool {
	return (page == 0x06 && usage == 0x20) || (page == 0x85 && (usage == 0x66 || usage == 0x65))
}

func openHID(path string, access uint32, overlapped bool) (windows.Handle, error) {
	flags := uint32(0)
	if overlapped {
		flags = windows.FILE_FLAG_OVERLAPPED
	}
	return windows.CreateFile(u16(path), access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, flags, 0)
}

func enumHID() []hidInfo {
	var guid windows.GUID
	pHidD_GetHidGuid.Call(uintptr(unsafe.Pointer(&guid)))
	var out []hidInfo
	for _, it := range enumInterfaces(guid) {
		h, err := openHID(it.Path, 0, false)
		if err != nil {
			continue
		}
		info := hidInfo{Path: it.Path, Container: it.Container, BT: isBTPath(it.Path)}
		attr := HIDD_ATTRIBUTES{Size: uint32(unsafe.Sizeof(HIDD_ATTRIBUTES{}))}
		if r, _, _ := pHidD_GetAttributes.Call(uintptr(h), uintptr(unsafe.Pointer(&attr))); r != 0 {
			info.VID, info.PID = attr.VendorID, attr.ProductID
		}
		if p, ok := productCache[it.Path]; ok {
			info.Product = p
		} else {
			buf := make([]uint16, 128)
			if r, _, _ := pHidD_GetProductString.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), 256); r != 0 {
				info.Product = strings.TrimSpace(syscall.UTF16ToString(buf))
			}
			productCache[it.Path] = info.Product
		}
		var pp uintptr
		if r, _, _ := pHidD_GetPreparsedData.Call(uintptr(h), uintptr(unsafe.Pointer(&pp))); r != 0 {
			var caps HIDP_CAPS
			if st, _, _ := pHidP_GetCaps.Call(pp, uintptr(unsafe.Pointer(&caps))); st == hidpStatusSuccess {
				info.UsagePage, info.Usage = caps.UsagePage, caps.Usage
				info.InLen, info.OutLen = int(caps.InputReportByteLength), int(caps.OutputReportByteLength)
				info.FeatLen = int(caps.FeatureReportByteLength)
				info.Battery = findBatteryCap(pp, 0, caps.NumberInputValueCaps)
				info.FeatBatt = findBatteryCap(pp, 2, caps.NumberFeatureValueCaps)
			}
			pHidD_FreePreparsedData.Call(pp)
		}
		windows.CloseHandle(h)
		out = append(out, info)
	}
	return out
}

func findBatteryCap(pp uintptr, reportType uintptr, n uint16) *batteryCap {
	if n == 0 {
		return nil
	}
	vc := make([]byte, 72*int(n))
	if st, _, _ := pHidP_GetValueCaps.Call(reportType, uintptr(unsafe.Pointer(&vc[0])), uintptr(unsafe.Pointer(&n)), pp); st != hidpStatusSuccess {
		return nil
	}
	for i := 0; i < int(n); i++ {
		c := vc[i*72 : i*72+72]
		page := binary.LittleEndian.Uint16(c[0:])
		usage := binary.LittleEndian.Uint16(c[56:])
		if batteryUsage(page, usage) {
			return &batteryCap{
				ReportID:   c[2],
				Page:       page,
				Usage:      usage,
				LinkColl:   binary.LittleEndian.Uint16(c[6:]),
				LogicalMin: int32(binary.LittleEndian.Uint32(c[40:])),
				LogicalMax: int32(binary.LittleEndian.Uint32(c[44:])),
			}
		}
	}
	return nil
}

type btRead struct {
	pct int
	ok  bool
	at  time.Time
}

// btReads remembers the last over-the-air battery read of each Bluetooth collection.
var btReads = map[string]btRead{}

// readHIDBattery reads a standard HID battery value (any brand that reports it), from an input
// report or, failing that, a feature report. Bluetooth devices are asked at most once per btEvery.
func readHIDBattery(info hidInfo) (int, bool) {
	if info.Battery == nil && info.FeatBatt == nil {
		return 0, false
	}
	if info.BT {
		if r, ok := btReads[info.Path]; ok && time.Since(r.at) < btEvery {
			return r.pct, r.ok
		}
	}
	pct, ok := readHIDBatteryNow(info)
	if info.BT {
		btReads[info.Path] = btRead{pct: pct, ok: ok, at: time.Now()}
	}
	return pct, ok
}

func readHIDBatteryNow(info hidInfo) (int, bool) {
	h, err := openHID(info.Path, windows.GENERIC_READ|windows.GENERIC_WRITE, false)
	if err != nil {
		h, err = openHID(info.Path, 0, false)
		if err != nil {
			return 0, false
		}
	}
	defer windows.CloseHandle(h)
	var pp uintptr
	if r, _, _ := pHidD_GetPreparsedData.Call(uintptr(h), uintptr(unsafe.Pointer(&pp))); r == 0 {
		return 0, false
	}
	defer pHidD_FreePreparsedData.Call(pp)
	try := func(bc *batteryCap, reportType uintptr, n int, get *windows.LazyProc) (int, bool) {
		if bc == nil || n <= 0 {
			return 0, false
		}
		rep := make([]byte, n)
		rep[0] = bc.ReportID
		if r, _, _ := get.Call(uintptr(h), uintptr(unsafe.Pointer(&rep[0])), uintptr(len(rep))); r == 0 {
			return 0, false
		}
		var val uint32
		st, _, _ := pHidP_GetUsageValue.Call(reportType, uintptr(bc.Page), uintptr(bc.LinkColl), uintptr(bc.Usage), uintptr(unsafe.Pointer(&val)), pp, uintptr(unsafe.Pointer(&rep[0])), uintptr(len(rep)))
		if st != hidpStatusSuccess {
			return 0, false
		}
		lo, hi := bc.LogicalMin, bc.LogicalMax
		if hi <= lo {
			lo, hi = 0, 100
		}
		pct := int((int64(val) - int64(lo)) * 100 / (int64(hi) - int64(lo)))
		if pct < 0 || pct > 100 {
			return 0, false
		}
		return pct, true
	}
	if pct, ok := try(info.Battery, 0, info.InLen, pHidD_GetInputReport); ok {
		return pct, true
	}
	return try(info.FeatBatt, 2, info.FeatLen, pHidD_GetFeature)
}

var colRe = regexp.MustCompile(`&col[0-9a-f]+`)
var instRe = regexp.MustCompile(`&[0-9a-f]{4}(#\{)`)

// groupKey makes all top-level collections of one HID interface share a key.
// Windows paths differ per collection both in "&colNN" and in the trailing "&000N" instance number,
// e.g. hid#vid_046d&pid_c547&mi_02&col01#8&2d6a1b42&0&0000#{...} and ...&col02#8&2d6a1b42&0&0001#{...}
func groupKey(path string) string {
	p := colRe.ReplaceAllString(strings.ToLower(path), "")
	return instRe.ReplaceAllString(p, "$1")
}

// ---------------- overlapped HID transport ----------------

type hidChan struct {
	h      windows.Handle
	outLen int
	inLen  int
}

type hidTransport struct {
	short, long *hidChan
	in          chan []byte
	wg          sync.WaitGroup
	closed      chan struct{}
}

func newTransport(shortPath string, sIn, sOut int, longPath string, lIn, lOut int) (*hidTransport, error) {
	t := &hidTransport{in: make(chan []byte, 256), closed: make(chan struct{})}
	open := func(p string, in, out int) *hidChan {
		if p == "" {
			return nil
		}
		h, err := openHID(p, windows.GENERIC_READ|windows.GENERIC_WRITE, true)
		if err != nil {
			return nil
		}
		return &hidChan{h: h, inLen: in, outLen: out}
	}
	t.short = open(shortPath, sIn, sOut)
	t.long = open(longPath, lIn, lOut)
	if t.short == nil && t.long == nil {
		return nil, errors.New("open failed")
	}
	for _, c := range []*hidChan{t.short, t.long} {
		if c != nil && c.inLen > 0 {
			t.wg.Add(1)
			go t.reader(c)
		}
	}
	return t, nil
}

func (t *hidTransport) reader(c *hidChan) {
	defer t.wg.Done()
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return
	}
	defer windows.CloseHandle(ev)
	buf := make([]byte, c.inLen)
	for {
		var ov windows.Overlapped
		ov.HEvent = ev
		windows.ResetEvent(ev)
		var n uint32
		err := windows.ReadFile(c.h, buf, &n, &ov)
		if err == windows.ERROR_IO_PENDING {
			err = windows.GetOverlappedResult(c.h, &ov, &n, true)
		}
		if err != nil {
			return
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		select {
		case t.in <- pkt:
		case <-t.closed:
			return
		default: // drop if nobody listens
		}
	}
}

func (t *hidTransport) write(c *hidChan, data []byte) error {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ev)
	var ov windows.Overlapped
	ov.HEvent = ev
	var n uint32
	err = windows.WriteFile(c.h, data, &n, &ov)
	if err == windows.ERROR_IO_PENDING {
		if ev, _ := windows.WaitForSingleObject(ev, 1000); ev != windows.WAIT_OBJECT_0 {
			windows.CancelIoEx(c.h, &ov)
			windows.GetOverlappedResult(c.h, &ov, &n, true)
			return errors.New("write timeout")
		}
		err = windows.GetOverlappedResult(c.h, &ov, &n, true)
	}
	return err
}

func (t *hidTransport) drain() {
	for {
		select {
		case <-t.in:
		default:
			return
		}
	}
}

func (t *hidTransport) recv(timeout time.Duration, match func([]byte) bool) ([]byte, bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case p := <-t.in:
			if match(p) {
				return p, true
			}
		case <-deadline.C:
			return nil, false
		}
	}
}

func (t *hidTransport) Close() {
	close(t.closed)
	for _, c := range []*hidChan{t.short, t.long} {
		if c != nil {
			windows.CancelIoEx(c.h, nil)
		}
	}
	done := make(chan struct{})
	go func() { t.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	for _, c := range []*hidChan{t.short, t.long} {
		if c != nil {
			windows.CloseHandle(c.h)
		}
	}
}
