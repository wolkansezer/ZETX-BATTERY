//go:build windows

package main

import (
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	pSetupDiGetClassDevsW             = setupapi.NewProc("SetupDiGetClassDevsW")
	pSetupDiEnumDeviceInfo            = setupapi.NewProc("SetupDiEnumDeviceInfo")
	pSetupDiEnumDeviceInterfaces      = setupapi.NewProc("SetupDiEnumDeviceInterfaces")
	pSetupDiGetDeviceInterfaceDetailW = setupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	pSetupDiGetDevicePropertyW        = setupapi.NewProc("SetupDiGetDevicePropertyW")
	pSetupDiGetDeviceInstanceIdW      = setupapi.NewProc("SetupDiGetDeviceInstanceIdW")
	pSetupDiDestroyDeviceInfoList     = setupapi.NewProc("SetupDiDestroyDeviceInfoList")
)

const (
	DIGCF_PRESENT         = 0x02
	DIGCF_ALLCLASSES      = 0x04
	DIGCF_DEVICEINTERFACE = 0x10
	invalidHandle         = ^uintptr(0)
)

type SP_DEVINFO_DATA struct {
	CbSize    uint32
	ClassGuid windows.GUID
	DevInst   uint32
	Reserved  uintptr
}

type SP_DEVICE_INTERFACE_DATA struct {
	CbSize             uint32
	InterfaceClassGuid windows.GUID
	Flags              uint32
	Reserved           uintptr
}

type DEVPROPKEY struct {
	Fmtid windows.GUID
	Pid   uint32
}

func mustGUID(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(err)
	}
	return g
}

var (
	keyContainerID  = DEVPROPKEY{mustGUID("{8c7ed206-3f8a-4827-b3ab-ae9e1faefc6c}"), 2}
	keyFriendlyName = DEVPROPKEY{mustGUID("{a45c254e-df1c-4efd-8020-67d146a850e0}"), 14}
	keyDeviceDesc   = DEVPROPKEY{mustGUID("{a45c254e-df1c-4efd-8020-67d146a850e0}"), 2}
	keyBtBattery    = DEVPROPKEY{mustGUID("{104EA319-6EE2-4701-BD47-8DDBF425BBE5}"), 2}
	keyBtCoD        = DEVPROPKEY{mustGUID("{2BD67D8B-8BEB-48D5-87E0-6CDA3428040A}"), 10}
	keyBtConnected  = DEVPROPKEY{mustGUID("{83DA6326-97A6-4088-9453-A1923F573B29}"), 15}

	classKeyboard = mustGUID("{4D36E96B-E325-11CE-BFC1-08002BE10318}")
	classMouse    = mustGUID("{4D36E96F-E325-11CE-BFC1-08002BE10318}")
	classMedia    = mustGUID("{4D36E96C-E325-11CE-BFC1-08002BE10318}")
	classAudioEP  = mustGUID("{C166523C-FE0C-4A94-A586-F1A80CFBBF3E}")
	nullContainer = mustGUID("{00000000-0000-0000-FFFF-FFFFFFFFFFFF}")
)

func getPropRaw(h uintptr, did *SP_DEVINFO_DATA, key *DEVPROPKEY) ([]byte, bool) {
	var ptype, req uint32
	pSetupDiGetDevicePropertyW.Call(h, uintptr(unsafe.Pointer(did)), uintptr(unsafe.Pointer(key)),
		uintptr(unsafe.Pointer(&ptype)), 0, 0, uintptr(unsafe.Pointer(&req)), 0)
	if req == 0 || req > 1<<16 {
		return nil, false
	}
	buf := make([]byte, req)
	r, _, _ := pSetupDiGetDevicePropertyW.Call(h, uintptr(unsafe.Pointer(did)), uintptr(unsafe.Pointer(key)),
		uintptr(unsafe.Pointer(&ptype)), uintptr(unsafe.Pointer(&buf[0])), uintptr(req), uintptr(unsafe.Pointer(&req)), 0)
	if r == 0 {
		return nil, false
	}
	return buf[:req], true
}

func getPropString(h uintptr, did *SP_DEVINFO_DATA, key *DEVPROPKEY) string {
	b, ok := getPropRaw(h, did, key)
	if !ok || len(b) < 2 {
		return ""
	}
	u := unsafe.Slice((*uint16)(unsafe.Pointer(&b[0])), len(b)/2)
	return syscall.UTF16ToString(u)
}

func getPropGUID(h uintptr, did *SP_DEVINFO_DATA, key *DEVPROPKEY) (windows.GUID, bool) {
	b, ok := getPropRaw(h, did, key)
	if !ok || len(b) < 16 {
		return windows.GUID{}, false
	}
	return *(*windows.GUID)(unsafe.Pointer(&b[0])), true
}

func instanceID(h uintptr, did *SP_DEVINFO_DATA) string {
	buf := make([]uint16, 512)
	var req uint32
	r, _, _ := pSetupDiGetDeviceInstanceIdW.Call(h, uintptr(unsafe.Pointer(did)), uintptr(unsafe.Pointer(&buf[0])), 512, uintptr(unsafe.Pointer(&req)))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

// container groups every device node that belongs to one physical device.
type container struct {
	ID        windows.GUID
	Names     []string
	BtName    string
	Kbd       bool
	Mouse     bool
	Audio     bool
	CoD       uint32
	HasCoD    bool
	BtBattery int // -1 = none
	HasBT     bool
	HasConn   bool
	Connected bool
}

func (c *container) bestName() string {
	if c.BtName != "" {
		return c.BtName
	}
	for _, n := range c.Names {
		l := strings.ToLower(n)
		if strings.Contains(l, "hid") || strings.Contains(l, "uyumlu") || strings.Contains(l, "compliant") || strings.Contains(l, "enumerator") {
			continue
		}
		return n
	}
	if len(c.Names) > 0 {
		return c.Names[0]
	}
	return ""
}

// kind classifies the container: Bluetooth class of device, name, then driver classes.
func (c *container) kind(extraName string) Kind {
	if c.HasCoD {
		major := (c.CoD >> 8) & 0x1F
		minor := (c.CoD >> 2) & 0x3F
		switch major {
		case 0x04:
			return KindHeadset
		case 0x05:
			if m := minor & 0x0F; m == 1 || m == 2 {
				return KindGamepad
			}
			switch (minor >> 4) & 0x3 {
			case 1:
				return KindKeyboard
			case 2:
				return KindMouse
			case 3:
				return KindKeyboard
			}
		}
	}
	if k := kindFromName(c.bestName() + " " + extraName); k != KindOther {
		return k
	}
	switch {
	case c.Audio:
		return KindHeadset
	case c.Mouse:
		return KindMouse
	case c.Kbd:
		return KindKeyboard
	}
	return KindOther
}

func kindFromName(n string) Kind {
	l := strings.ToLower(n)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(l, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("headset", "headphone", "kulakl", "earbud", "buds", "airpods", "arctis", "cloud ii", "cloud alpha", "cloud flight", "cloud stinger", "barracuda", "kraken", "blackshark", "nari", "g733", "g535", "g435", "g935", "g933", "g pro x 2", "pro x wireless", "astro a", "void", "hs80", "hs70", "virtuoso", "wh-", "wf-", "jbl tune", "freebuds", "galaxy buds", "barracuda", "blackshark", "virtuoso", "hs80", "hs65", "hs55", "inzone", "maxwell", "stealth", "elo", "quantum", "h3pro", "gsp"):
		return KindHeadset
	case has("keyboard", "klavye", "keys", "mx mechanical", "craft", "k380", "k400", "k780", "k270", "k800", "g915", "g913", "g715", "g815", "pop keys", "huntsman", "blackwidow", "deathstalker", "ornata", "apex", "k70", "k100", "k65", "k63", "k57", "k55", "alloy", "origins", "keychron", "akko", "royal kludge"):
		return KindKeyboard
	case has("mouse", "fare", "mx master", "mx anywhere", "mx ergo", "m720", "m590", "m650", "lift", "superlight", "g305", "g502", "g703", "g903", "g705", "g pro wireless", "pro x superlight", "viper", "deathadder", "basilisk", "orochi", "naga", "mamba", "atheris", "lancehead", "cobra", "pro click", "aerox", "rival", "prime", "dark core", "darkstar", "harpoon", "ironclaw", "sabre", "m75", "katar", "nightsabre", "scimitar", "trackball", "pulsefire", "model o", "model d", "xlite", "x2", "burst", "kone", "zowie", "g-wolves", "endgame", "lamzu", "vxe", "attack shark"):
		return KindMouse
	case has("gamepad", "joystick", "xbox", "dualsense", "dualshock", "wireless controller", "pro controller", "8bitdo", "joy-con", "gamesir", "gulikit", "flydigi", "f710", "stadia", "luna", "oyun kolu"):
		return KindGamepad
	}
	return KindOther
}

// enumContainers walks every present device node and groups it by container ID.
func enumContainers() map[windows.GUID]*container {
	out := map[windows.GUID]*container{}
	h, _, _ := pSetupDiGetClassDevsW.Call(0, 0, 0, DIGCF_ALLCLASSES|DIGCF_PRESENT)
	if h == invalidHandle || h == 0 {
		return out
	}
	defer pSetupDiDestroyDeviceInfoList.Call(h)
	for i := 0; i < 5000; i++ {
		did := SP_DEVINFO_DATA{CbSize: uint32(unsafe.Sizeof(SP_DEVINFO_DATA{}))}
		r, _, _ := pSetupDiEnumDeviceInfo.Call(h, uintptr(i), uintptr(unsafe.Pointer(&did)))
		if r == 0 {
			break
		}
		cid, ok := getPropGUID(h, &did, &keyContainerID)
		if !ok || cid == nullContainer {
			continue
		}
		c := out[cid]
		if c == nil {
			c = &container{ID: cid, BtBattery: -1}
			out[cid] = c
		}
		name := getPropString(h, &did, &keyFriendlyName)
		if name == "" {
			name = getPropString(h, &did, &keyDeviceDesc)
		}
		inst := strings.ToUpper(instanceID(h, &did))
		if name != "" {
			if (strings.HasPrefix(inst, "BTHENUM\\DEV_") || strings.HasPrefix(inst, "BTHLE\\DEV_")) && c.BtName == "" {
				c.BtName = name
			}
			c.Names = append(c.Names, name)
		}
		if strings.HasPrefix(inst, "BTHENUM\\") || strings.HasPrefix(inst, "BTHLE\\") || strings.HasPrefix(inst, "BTHLEDEVICE\\") {
			c.HasBT = true
			if b, ok := getPropRaw(h, &did, &keyBtConnected); ok && len(b) >= 1 {
				c.HasConn = true
				if b[0] != 0 {
					c.Connected = true
				}
			}
		}
		switch did.ClassGuid {
		case classKeyboard:
			c.Kbd = true
		case classMouse:
			c.Mouse = true
		case classMedia, classAudioEP:
			c.Audio = true
		}
		if b, ok := getPropRaw(h, &did, &keyBtBattery); ok && len(b) >= 1 && b[0] <= 100 {
			c.BtBattery = int(b[0])
		}
		if b, ok := getPropRaw(h, &did, &keyBtCoD); ok && len(b) >= 4 {
			c.CoD = *(*uint32)(unsafe.Pointer(&b[0]))
			c.HasCoD = c.CoD != 0
		}
	}
	return out
}

type ifaceInfo struct {
	Path      string
	Container windows.GUID
}

// enumInterfaces lists present device interfaces of the given class.
func enumInterfaces(guid windows.GUID) []ifaceInfo {
	var out []ifaceInfo
	h, _, _ := pSetupDiGetClassDevsW.Call(uintptr(unsafe.Pointer(&guid)), 0, 0, DIGCF_PRESENT|DIGCF_DEVICEINTERFACE)
	if h == invalidHandle || h == 0 {
		return out
	}
	defer pSetupDiDestroyDeviceInfoList.Call(h)
	for i := 0; i < 2000; i++ {
		ifd := SP_DEVICE_INTERFACE_DATA{CbSize: uint32(unsafe.Sizeof(SP_DEVICE_INTERFACE_DATA{}))}
		r, _, _ := pSetupDiEnumDeviceInterfaces.Call(h, 0, uintptr(unsafe.Pointer(&guid)), uintptr(i), uintptr(unsafe.Pointer(&ifd)))
		if r == 0 {
			break
		}
		var req uint32
		pSetupDiGetDeviceInterfaceDetailW.Call(h, uintptr(unsafe.Pointer(&ifd)), 0, 0, uintptr(unsafe.Pointer(&req)), 0)
		if req < 8 || req > 4096 {
			continue
		}
		buf := make([]byte, req+8)
		*(*uint32)(unsafe.Pointer(&buf[0])) = 8 // sizeof(SP_DEVICE_INTERFACE_DETAIL_DATA_W) on x64
		did := SP_DEVINFO_DATA{CbSize: uint32(unsafe.Sizeof(SP_DEVINFO_DATA{}))}
		r, _, _ = pSetupDiGetDeviceInterfaceDetailW.Call(h, uintptr(unsafe.Pointer(&ifd)), uintptr(unsafe.Pointer(&buf[0])), uintptr(req), 0, uintptr(unsafe.Pointer(&did)))
		if r == 0 {
			continue
		}
		path := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(&buf[4])))
		cid, _ := getPropGUID(h, &did, &keyContainerID)
		out = append(out, ifaceInfo{Path: path, Container: cid})
	}
	return out
}
