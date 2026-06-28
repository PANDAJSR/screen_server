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

// TouchPhase describes the lifecycle stage of a touch contact.
type TouchPhase string

const (
	TouchPhaseStart TouchPhase = "start" // finger down
	TouchPhaseMove  TouchPhase = "move"  // finger moved
	TouchPhaseEnd   TouchPhase = "end"   // finger up
)

// TouchContact represents a single finger/contact point.
type TouchContact struct {
	ID    int        `json:"id"`
	X     int        `json:"x"`
	Y     int        `json:"y"`
	Phase TouchPhase `json:"phase"`
}

// TouchEvent carries a batch of touch contacts from the client.
// All contacts in a batch are injected atomically to preserve
// multi-touch gesture integrity (e.g. pinch-zoom, rotation).
type TouchEvent struct {
	Touches []TouchContact `json:"touches"`
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
	GetKeyState() ([]int, error)
	ReleaseAllKeys() error
	InjectTouch(contacts []TouchContact) error
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