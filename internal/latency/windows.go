package latency

import (
	"log"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsController shows a topmost, frameless popup window centered on the
// primary monitor and fills it with a solid color. A single worker goroutine
// owns the window and its Win32 message loop (Win32 requires the creating
// thread to pump messages). The hub calls ShowBlue/ShowRed/Close serialized on
// its own goroutine, so hwnd field access needs no lock. The fill color is a
// package-level atomic read by the worker's WndProc; only one detection window
// is ever active at a time.
type windowsController struct {
	hwnd    uintptr
	started bool
	ready   chan struct{}
	done    chan struct{}
}

// currentColor is the COLORREF the window paints. Set atomically by ShowBlue/
// ShowRed (hub goroutine), read atomically by WndProc (worker goroutine).
var currentColor uint32 = colorBlue

func newWindowsController() (Controller, error) {
	return &windowsController{}, nil
}

func NewController() (Controller, error) {
	return newWindowsController()
}

func (c *windowsController) ShowBlue() error {
	atomic.StoreUint32(&currentColor, colorBlue)
	if !c.started {
		if err := ensureClass(); err != nil {
			return err
		}
		c.started = true
		c.ready = make(chan struct{})
		c.done = make(chan struct{})
		go c.run()
		<-c.ready
	} else if c.hwnd != 0 {
		invalidate(c.hwnd)
	}
	return nil
}

func (c *windowsController) ShowRed() error {
	atomic.StoreUint32(&currentColor, colorRed)
	if c.hwnd != 0 {
		invalidate(c.hwnd)
	}
	return nil
}

func (c *windowsController) Close() error {
	if !c.started {
		return nil
	}
	hwnd := c.hwnd
	done := c.done
	if hwnd != 0 {
		postMessage(hwnd, _WM_CLOSE, 0, 0)
	}
	if done != nil {
		<-done
	}
	c.started = false
	c.hwnd = 0
	return nil
}

// run owns the window for one detection cycle: register (once), create, pump,
// then signal done when the loop exits (WM_QUIT after Close).
func (c *windowsController) run() {
	defer func() {
		if c.done != nil {
			close(c.done)
		}
	}()
	user32 := windows.NewLazyDLL("user32.dll")

	hwnd := createWindow(user32)
	c.hwnd = hwnd
	close(c.ready)
	if hwnd == 0 {
		log.Printf("[latency] CreateWindowEx failed")
		return
	}

	showWindow(hwnd, _SW_SHOW)
	updateWindow(hwnd)

	var m _MSG
	for getMessage(user32, &m) > 0 {
		translateMessage(user32, &m)
		dispatchMessage(user32, &m)
	}
	c.hwnd = 0
}

// ---- Class registration (once per process) ----

const latencyClassName = "ScreenServerLatencyWindow"

var (
	classOnce sync.Once
	classErr  error
)

func ensureClass() error {
	classOnce.Do(func() {
		user32 := windows.NewLazyDLL("user32.dll")
		className := windows.StringToUTF16Ptr(latencyClassName)
		wc := _WNDCLASSEX{
			Style:         _CS_HREDRAW | _CS_VREDRAW,
			LpfnWndProc:   windows.NewCallback(windowProc),
			LpszClassName: className,
		}
		wc.CbSize = uint32(unsafe.Sizeof(wc))
		// RegisterClassExW is called exactly once per process (sync.Once), so a
		// zero return is a genuine failure, not a duplicate-class condition.
		r, _, err := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&wc)))
		if r == 0 {
			classErr = err
		}
	})
	return classErr
}

func createWindow(user32 *windows.LazyDLL) uintptr {
	screenW, _, _ := user32.NewProc("GetSystemMetrics").Call(_SM_CXSCREEN)
	screenH, _, _ := user32.NewProc("GetSystemMetrics").Call(_SM_CYSCREEN)
	w := int(int32(screenW))
	h := int(int32(screenH))
	if w <= 0 || h <= 0 {
		w, h = 1920, 1080
	}
	// Square ~1/4 of the smaller screen dimension, clamped.
	size := w / 4
	if h/4 < size {
		size = h / 4
	}
	if size < 200 {
		size = 200
	}
	if size > 600 {
		size = 600
	}
	x := (w - size) / 2
	y := (h - size) / 2

	className := windows.StringToUTF16Ptr(latencyClassName)
	hwnd, _, _ := user32.NewProc("CreateWindowExW").Call(
		uintptr(_WS_EX_TOPMOST|_WS_EX_TOOLWINDOW|_WS_EX_NOACTIVATE),
		uintptr(unsafe.Pointer(className)),
		0,
		uintptr(_WS_POPUP|_WS_VISIBLE),
		uintptr(x), uintptr(y), uintptr(size), uintptr(size),
		0, 0, 0,
		0,
	)
	return hwnd
}

// ---- Window procedure (runs on worker goroutine) ----

func windowProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case _WM_ERASEBKGND:
		return 1 // we paint everything in WM_PAINT; suppress background erase
	case _WM_PAINT:
		paintWindow(hwnd)
		return 0
	case _WM_APP_INVALIDATE:
		invalidateRect(hwnd, nil)
		return 0
	case _WM_DESTROY:
		postQuitMessage()
		return 0
	}
	return defWindowProc(hwnd, msg, wParam, lParam)
}

func paintWindow(hwnd uintptr) {
	user32 := windows.NewLazyDLL("user32.dll")
	gdi32 := windows.NewLazyDLL("gdi32.dll")
	var ps _PAINTSTRUCT
	hdc, _, _ := user32.NewProc("BeginPaint").Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer user32.NewProc("EndPaint").Call(hwnd, uintptr(unsafe.Pointer(&ps)))

	var rc _RECT
	user32.NewProc("GetClientRect").Call(hwnd, uintptr(unsafe.Pointer(&rc)))

	color := atomic.LoadUint32(&currentColor)
	// CreateSolidBrush + DeleteObject are GDI (gdi32.dll); FillRect is user32.
	brush, _, _ := gdi32.NewProc("CreateSolidBrush").Call(uintptr(color))
	if brush != 0 {
		user32.NewProc("FillRect").Call(hdc, uintptr(unsafe.Pointer(&rc)), brush)
		gdi32.NewProc("DeleteObject").Call(brush)
	}
}

// ---- Thin user32 wrappers ----

func invalidate(hwnd uintptr) {
	invalidateRect(hwnd, nil)
}

func invalidateRect(hwnd uintptr, rc *_RECT) {
	user32 := windows.NewLazyDLL("user32.dll")
	var rcPtr uintptr
	if rc != nil {
		rcPtr = uintptr(unsafe.Pointer(rc))
	}
	user32.NewProc("InvalidateRect").Call(hwnd, rcPtr, 1) // erase = TRUE
}

func postMessage(hwnd uintptr, msg uint32, wParam, lParam uintptr) {
	user32 := windows.NewLazyDLL("user32.dll")
	user32.NewProc("PostMessageW").Call(hwnd, uintptr(msg), wParam, lParam)
}

func postQuitMessage() {
	user32 := windows.NewLazyDLL("user32.dll")
	user32.NewProc("PostQuitMessage").Call(0)
}

func showWindow(hwnd uintptr, cmd int32) {
	user32 := windows.NewLazyDLL("user32.dll")
	user32.NewProc("ShowWindow").Call(hwnd, uintptr(cmd))
}

func updateWindow(hwnd uintptr) {
	user32 := windows.NewLazyDLL("user32.dll")
	user32.NewProc("UpdateWindow").Call(hwnd)
}

func getMessage(user32 *windows.LazyDLL, m *_MSG) int32 {
	r, _, _ := user32.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(m)), 0, 0, 0)
	return int32(r)
}

func translateMessage(user32 *windows.LazyDLL, m *_MSG) {
	user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(m)))
}

func dispatchMessage(user32 *windows.LazyDLL, m *_MSG) {
	user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(m)))
}

func defWindowProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	user32 := windows.NewLazyDLL("user32.dll")
	r, _, _ := user32.NewProc("DefWindowProcW").Call(hwnd, msg, wParam, lParam)
	return r
}

// ---- Win32 types ----

type _POINT struct{ X, Y int32 }
type _RECT struct{ Left, Top, Right, Bottom int32 }

type _MSG struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      _POINT
}

type _PAINTSTRUCT struct {
	Hdc         uintptr
	FErase      int32 // BOOL
	RcPaint     _RECT
	FRestore    int32 // BOOL
	FIncUpdate  int32 // BOOL
	RGBReserved [32]byte
}

type _WNDCLASSEX struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

// ---- Win32 constants ----

const (
	_CS_HREDRAW = 0x0002
	_CS_VREDRAW = 0x0001

	_WS_POPUP   = 0x80000000
	_WS_VISIBLE = 0x10000000

	_WS_EX_TOPMOST     = 0x00000008
	_WS_EX_TOOLWINDOW  = 0x00000080
	_WS_EX_NOACTIVATE  = 0x08000000

	_SW_SHOW = 5

	_SM_CXSCREEN = 0
	_SM_CYSCREEN = 1

	_WM_DESTROY      = 2
	_WM_PAINT        = 0x000F
	_WM_CLOSE        = 0x0010
	_WM_ERASEBKGND   = 0x0014
	_WM_APP_INVALIDATE = 0x8001 // WM_APP + 1
)
