package input

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsController struct{}

func newWindowsController() (Controller, error) {
	return &windowsController{}, nil
}

func (c *windowsController) MoveMouse(x, y int) error {
	return mouseMove(int32(x), int32(y))
}

func (c *windowsController) PressMouse(btn MouseButton, x, y int) error {
	return mouseButtonPress(btn, true)
}

func (c *windowsController) ReleaseMouse(btn MouseButton, x, y int) error {
	return mouseButtonPress(btn, false)
}

func mouseButtonPress(btn MouseButton, pressed bool) error {
	var downFlags, upFlags uint32
	switch btn {
	case MouseButtonLeft:
		downFlags = 0x0002
		upFlags = 0x0004
	case MouseButtonRight:
		downFlags = 0x0008
		upFlags = 0x0010
	case MouseButtonMiddle:
		downFlags = 0x0020
		upFlags = 0x0040
	}
	if pressed {
		return mouseEvent(downFlags, 0, 0)
	}
	return mouseEvent(upFlags, 0, 0)
}

func (c *windowsController) Scroll(dx, dy int) error {
	return mouseEvent(0x0800, uint32(dy)*120, 0)
}

func (c *windowsController) PressKey(keyCode int) error {
	return keybdEvent(uint16(keyCode), 0, 0)
}

func (c *windowsController) ReleaseKey(keyCode int) error {
	return keybdEvent(uint16(keyCode), 0, 2)
}

func (c *windowsController) Close() error {
	return nil
}

func (c *windowsController) GetCursorPos() (x, y int, err error) {
	user32 := windows.NewLazyDLL("user32.dll")
	getCursorPosProc := user32.NewProc("GetCursorPos")
	var pt struct {
		X int32
		Y int32
	}
	r1, _, _ := getCursorPosProc.Call(uintptr(unsafe.Pointer(&pt)))
	if r1 == 0 {
		return 0, 0, windows.GetLastError()
	}
	return int(pt.X), int(pt.Y), nil
}

func (c *windowsController) GetScreenSize() (width, height int, err error) {
	user32 := windows.NewLazyDLL("user32.dll")
	getSystemMetricsProc := user32.NewProc("GetSystemMetrics")
	w, _, _ := getSystemMetricsProc.Call(0)
	h, _, _ := getSystemMetricsProc.Call(1)
	width = int(int32(w))
	height = int(int32(h))
	if width == 0 || height == 0 {
		return 0, 0, windows.GetLastError()
	}
	return width, height, nil
}

func mouseMove(x, y int32) error {
	user32 := windows.NewLazyDLL("user32.dll")
	mouseEventProc := user32.NewProc("mouse_event")
	r1, _, _ := mouseEventProc.Call(0x0001, uintptr(x), uintptr(y), 0, 0)
	if r1 == 0 {
		return windows.GetLastError()
	}
	return nil
}

func mouseEvent(flags, xData, yData uint32) error {
	user32 := windows.NewLazyDLL("user32.dll")
	mouseEventProc := user32.NewProc("mouse_event")
	r1, _, _ := mouseEventProc.Call(uintptr(flags), uintptr(xData), uintptr(yData), 0, 0)
	if r1 == 0 {
		return windows.GetLastError()
	}
	return nil
}

func keybdEvent(key uint16, scan uint16, flags uint32) error {
	user32 := windows.NewLazyDLL("user32.dll")
	keybdEventProc := user32.NewProc("keybd_event")
	r1, _, _ := keybdEventProc.Call(uintptr(key), uintptr(scan), uintptr(flags), 0)
	if r1 == 0 {
		return windows.GetLastError()
	}
	return nil
}