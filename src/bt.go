//go:build windows

package main

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Bluetooth Low Energy GATT Battery Service (0x180F / 0x2A19) – works for any brand.

var (
	pBtGattGetServices            = btapis.NewProc("BluetoothGATTGetServices")
	pBtGattGetCharacteristics     = btapis.NewProc("BluetoothGATTGetCharacteristics")
	pBtGattGetCharacteristicValue = btapis.NewProc("BluetoothGATTGetCharacteristicValue")
	batteryServiceGUID            = mustGUID("{0000180F-0000-1000-8000-00805F9B34FB}")
)

const (
	gattFlagNone        = 0x00
	gattFlagForceDevice = 0x04
)

// BTH_LE_UUID: BOOLEAN IsShortUuid; union { USHORT; GUID } (GUID aligned to 4)
type bthUUID struct {
	IsShort byte
	_       [3]byte
	Long    windows.GUID
}

func (u bthUUID) short() uint16 {
	if u.IsShort != 0 {
		return *(*uint16)(unsafe.Pointer(&u.Long))
	}
	return uint16(u.Long.Data1 & 0xFFFF)
}

type bthService struct {
	UUID   bthUUID
	Handle uint16
	_      [2]byte
}

type bthChar struct {
	ServiceHandle uint16
	_             [2]byte
	UUID          bthUUID
	AttrHandle    uint16
	ValueHandle   uint16
	Flags         [8]byte
}

// gattForced remembers when a Bluetooth LE device was last asked over the air.
var gattForced = map[string]time.Time{}

// readGattBattery reads the battery characteristic from Windows' GATT cache, which Windows keeps
// up to date by itself. Only when the cache is empty is the device asked over the air, and then
// at most once per btEvery, so a Bluetooth mouse keeps its full polling rate.
func readGattBattery(path string) (int, bool) {
	h, err := windows.CreateFile(u16(path), windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		h, err = windows.CreateFile(u16(path), 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			return 0, false
		}
	}
	defer windows.CloseHandle(h)
	if pBtGattGetServices.Find() != nil {
		return 0, false
	}
	var n uint16
	pBtGattGetServices.Call(uintptr(h), 0, 0, uintptr(unsafe.Pointer(&n)), gattFlagNone)
	if n == 0 || n > 64 {
		return 0, false
	}
	svcs := make([]bthService, n)
	if r, _, _ := pBtGattGetServices.Call(uintptr(h), uintptr(n), uintptr(unsafe.Pointer(&svcs[0])), uintptr(unsafe.Pointer(&n)), gattFlagNone); r != 0 {
		return 0, false
	}
	for i := 0; i < int(n); i++ {
		svc := &svcs[i]
		var cn uint16
		pBtGattGetCharacteristics.Call(uintptr(h), uintptr(unsafe.Pointer(svc)), 0, 0, uintptr(unsafe.Pointer(&cn)), gattFlagNone)
		if cn == 0 || cn > 64 {
			continue
		}
		chars := make([]bthChar, cn)
		if r, _, _ := pBtGattGetCharacteristics.Call(uintptr(h), uintptr(unsafe.Pointer(svc)), uintptr(cn), uintptr(unsafe.Pointer(&chars[0])), uintptr(unsafe.Pointer(&cn)), gattFlagNone); r != 0 {
			continue
		}
		for j := 0; j < int(cn); j++ {
			c := &chars[j]
			if c.UUID.short() != 0x2A19 {
				continue
			}
			flags := []uintptr{gattFlagNone}
			if last, ok := gattForced[path]; !ok || time.Since(last) >= btEvery {
				flags = append(flags, gattFlagForceDevice)
			}
			for _, flag := range flags {
				if flag == gattFlagForceDevice {
					gattForced[path] = time.Now()
				}
				buf := make([]byte, 64)
				var req uint16
				r, _, _ := pBtGattGetCharacteristicValue.Call(uintptr(h), uintptr(unsafe.Pointer(c)), uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&req)), flag)
				if r == 0 {
					size := *(*uint32)(unsafe.Pointer(&buf[0]))
					if size >= 1 && buf[4] <= 100 {
						return int(buf[4]), true
					}
				}
			}
		}
	}
	return 0, false
}
