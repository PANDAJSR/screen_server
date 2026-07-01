//go:build windows

package sysinfo

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modWtsapi32 = windows.NewLazySystemDLL("wtsapi32.dll")
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modDwmapi   = windows.NewLazySystemDLL("dwmapi.dll")

	procWTSEnumerateSessionsW    = modWtsapi32.NewProc("WTSEnumerateSessionsW")
	procWTSQuerySessionInfoW     = modWtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory            = modWtsapi32.NewProc("WTSFreeMemory")
	procEnumDisplayMonitors      = modUser32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW          = modUser32.NewProc("GetMonitorInfoW")
	procEnumWindows              = modUser32.NewProc("EnumWindows")
	procIsWindowVisible          = modUser32.NewProc("IsWindowVisible")
	procGetWindowTextW           = modUser32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW     = modUser32.NewProc("GetWindowTextLengthW")
	procGetWindowRect            = modUser32.NewProc("GetWindowRect")
	procGetClientRect            = modUser32.NewProc("GetClientRect")
	procClientToScreen           = modUser32.NewProc("ClientToScreen")
	procGetClassNameW            = modUser32.NewProc("GetClassNameW")
	procGetSystemMetrics         = modUser32.NewProc("GetSystemMetrics")
	procDwmGetWindowAttribute    = modDwmapi.NewProc("DwmGetWindowAttribute")
)

// DWMWA_EXTENDED_FRAME_BOUNDS returns the window rect without the DWM shadow.
const dwmwaExtendedFrameBounds = 9

const (
	wtsCurrentServerHandle = 0
	wtsUserName            = 5 // WTS_INFO_CLASS
)

// WTS_CONNECTSTATE_CLASS
const (
	wtsActive       = 0
	wtsConnected    = 1
	wtsConnectQuery = 2
	wtsShadow       = 3
	wtsDisconnected = 4
	wtsIdle         = 5
	wtsListen       = 6
	wtsReset        = 7
	wtsDown         = 8
	wtsInit         = 9
)

// System metrics constants
const (
	smXVirtualScreen   = 76
	smYVirtualScreen   = 77
	smCXVirtualScreen  = 78
	smCYVirtualScreen  = 79
)

// Monitor info constants
const (
	monitorInfoFPrimary = 1
)

type wtsSessionInfo struct {
	SessionID      uint32
	WinStationName *uint16
	State          uint32
}

// RECT for monitor enumeration
type winRect struct {
	Left, Top, Right, Bottom int32
}

// MONITORINFOEXW for GetMonitorInfoW
type monitorInfoEx struct {
	CbSize    uint32
	RcMonitor winRect
	RcWork    winRect
	Flags     uint32
	DeviceName [32]uint16
}

type SessionInfo struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	State    string `json:"state"`
	UserName string `json:"userName"`
}

type DisplayInfo struct {
	Index   int    `json:"index"`
	Name    string `json:"name"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Primary bool   `json:"primary"`
}

type WindowInfo struct {
	Title string `json:"title"`
	Class string `json:"class"`
}

func connStateToString(state uint32) string {
	switch state {
	case wtsActive:
		return "Active"
	case wtsConnected:
		return "Connected"
	case wtsConnectQuery:
		return "ConnectQuery"
	case wtsShadow:
		return "Shadow"
	case wtsDisconnected:
		return "Disc"
	case wtsIdle:
		return "Idle"
	case wtsListen:
		return "Listen"
	case wtsReset:
		return "Reset"
	case wtsDown:
		return "Down"
	case wtsInit:
		return "Init"
	default:
		return "Unknown"
	}
}

// EnumSessions enumerates all Windows terminal services sessions.
func EnumSessions() ([]SessionInfo, error) {
	var ppInfo uintptr
	var count uint32

	ret, _, _ := procWTSEnumerateSessionsW.Call(
		wtsCurrentServerHandle,
		0,
		1,
		uintptr(unsafe.Pointer(&ppInfo)),
		uintptr(unsafe.Pointer(&count)),
	)
	if ret == 0 {
		return nil, syscall.GetLastError()
	}
	defer procWTSFreeMemory.Call(ppInfo)

	sessions := make([]SessionInfo, 0, count)
	infos := unsafe.Slice((*wtsSessionInfo)(unsafe.Pointer(ppInfo)), count)
	for _, info := range infos {
		name := ""
		if info.WinStationName != nil {
			name = windows.UTF16PtrToString(info.WinStationName)
		}
		userName := querySessionUserName(info.SessionID)
		sessions = append(sessions, SessionInfo{
			ID:       int(info.SessionID),
			Name:     name,
			State:    connStateToString(info.State),
			UserName: userName,
		})
	}
	return sessions, nil
}

func querySessionUserName(sessionID uint32) string {
	var bufPtr *uint16
	var bufSize uint32
	ret, _, _ := procWTSQuerySessionInfoW.Call(
		wtsCurrentServerHandle,
		uintptr(sessionID),
		wtsUserName,
		uintptr(unsafe.Pointer(&bufPtr)),
		uintptr(unsafe.Pointer(&bufSize)),
	)
	if ret == 0 || bufPtr == nil {
		return ""
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(bufPtr)))
	return windows.UTF16PtrToString(bufPtr)
}

// EnumDisplays enumerates all monitors and their virtual-screen positions.
func EnumDisplays() ([]DisplayInfo, error) {
	var displays []DisplayInfo
	var idx int

	type monitorEnumCtx struct {
		displays *[]DisplayInfo
		index    *int
	}
	ctx := monitorEnumCtx{displays: &displays, index: &idx}

	callback := syscall.NewCallback(func(hMonitor, hdc, lprcMonitor, dwData uintptr) uintptr {
		data := (*monitorEnumCtx)(unsafe.Pointer(dwData))
		*data.index++

		rect := (*winRect)(unsafe.Pointer(lprcMonitor))

		var mi monitorInfoEx
		mi.CbSize = uint32(unsafe.Sizeof(mi))
		procGetMonitorInfoW.Call(hMonitor, uintptr(unsafe.Pointer(&mi)))

		name := windows.UTF16PtrToString(&mi.DeviceName[0])
		primary := mi.Flags&monitorInfoFPrimary != 0

		*data.displays = append(*data.displays, DisplayInfo{
			Index:   *data.index - 1,
			Name:    name,
			X:       int(rect.Left),
			Y:       int(rect.Top),
			Width:   int(rect.Right - rect.Left),
			Height:  int(rect.Bottom - rect.Top),
			Primary: primary,
		})
		return 1
	})

	procEnumDisplayMonitors.Call(0, 0, callback, uintptr(unsafe.Pointer(&ctx)))
	return displays, nil
}

// EnumWindows_ enumerates all visible top-level windows with non-empty titles.
func EnumWindows_() ([]WindowInfo, error) {
	var wins []WindowInfo

	callback := syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		if vis == 0 {
			return 1
		}

		textLen, _, _ := procGetWindowTextLengthW.Call(hwnd)
		if textLen == 0 {
			return 1
		}

		buf := make([]uint16, textLen+1)
		procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(textLen+1))
		title := syscall.UTF16ToString(buf)
		if title == "" {
			return 1
		}

		classBuf := make([]uint16, 256)
		procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&classBuf[0])), 256)
		class := syscall.UTF16ToString(classBuf)

		wins = append(wins, WindowInfo{Title: title, Class: class})
		return 1
	})

	procEnumWindows.Call(callback, 0)
	return wins, nil
}

// GetWindowRectByTitle returns the screen rectangle of the client area for a
// window identified by title. Uses GetClientRect + ClientToScreen to match what
// FFmpeg's gdigrab actually captures — gdigrab uses GetClientRect (client area
// only, excluding title bar and borders) for window capture, NOT GetWindowRect.
func GetWindowRectByTitle(title string) (x, y, width, height int, found bool) {
	var result struct {
		left, top, right, bottom int32
		found                    bool
	}
	target := title

	callback := syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		if vis == 0 {
			return 1
		}
		textLen, _, _ := procGetWindowTextLengthW.Call(hwnd)
		if textLen == 0 {
			return 1
		}
		buf := make([]uint16, textLen+1)
		procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(textLen+1))
		if syscall.UTF16ToString(buf) != target {
			return 1
		}

		// gdigrab uses GetClientRect for the capture area, then ClientToScreen
		// to convert (0,0) of the client area to screen coordinates.
		var clientRect winRect
		r1, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&clientRect)))
		if r1 == 0 {
			return 1 // continue enumeration
		}
		var pt struct {
			X int32
			Y int32
		}
		r2, _, _ := procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&pt)))
		if r2 == 0 {
			return 1
		}
		result.left = pt.X
		result.top = pt.Y
		result.right = pt.X + (clientRect.Right - clientRect.Left)
		result.bottom = pt.Y + (clientRect.Bottom - clientRect.Top)
		result.found = true
		return 0
	})

	procEnumWindows.Call(callback, 0)
	if !result.found {
		return 0, 0, 0, 0, false
	}
	return int(result.left), int(result.top), int(result.right - result.left), int(result.bottom - result.top), true
}

// GetVirtualScreenSize returns the total virtual desktop size across all monitors.
func GetVirtualScreenSize() (width, height int) {
	w, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	h, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	return int(w), int(h)
}

// GetVirtualScreenOrigin returns the top-left origin of the virtual desktop.
func GetVirtualScreenOrigin() (x, y int) {
	ox, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	oy, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	return int(ox), int(oy)
}
