package input

import (
	"fmt"
	"runtime"
)

type MouseButton int

const (
	MouseButtonLeft   MouseButton = 1
	MouseButtonRight  MouseButton = 2
	MouseButtonMiddle MouseButton = 4
)

type InputEvent struct {
	Type string
	Data interface{}
}

type MouseMoveEvent struct {
	X, Y int
}

type MouseButtonEvent struct {
	Button MouseButton
	Pressed bool
	X, Y    int
}

type ScrollEvent struct {
	X, Y int
}

type KeyEvent struct {
	KeyCode int
	Pressed bool
}

// CursorInfo holds the current OS cursor image and hotspot.
type CursorInfo struct {
	ImageData string // base64-encoded PNG
	Width     int
	Height    int
	HotspotX  int
	HotspotY  int
}

type Controller interface {
	MoveMouse(x, y int) error
	PressMouse(btn MouseButton, x, y int) error
	ReleaseMouse(btn MouseButton, x, y int) error
	Scroll(dx, dy int) error
	PressKey(keyCode int) error
	ReleaseKey(keyCode int) error
	GetCursorPos() (x, y int, err error)
	GetCursorInfo() (*CursorInfo, error)
	GetScreenSize() (width, height int, err error)
	SetCursorPos(x, y int) error
	Close() error
}

func NewController() (Controller, error) {
	switch runtime.GOOS {
	case "windows":
		return newWindowsController()
	case "darwin":
		return newDarwinController()
	case "linux":
		return newLinuxController()
	default:
		return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}