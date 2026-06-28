package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Simple touch visualiser: opens a window that draws a coloured circle
// for each active touch contact and logs pointer events to stderr.

var (
	user32   = windows.NewLazyDLL("user32.dll")
	kernel32 = windows.NewLazyDLL("kernel32.dll")
	gdi32    = windows.NewLazyDLL("gdi32.dll")

	registerClass    = user32.NewProc("RegisterClassExW")
	createWindow     = user32.NewProc("CreateWindowExW")
	defWindowProc    = user32.NewProc("DefWindowProcW")
	getMessage       = user32.NewProc("GetMessageW")
	translateMessage = user32.NewProc("TranslateMessage")
	dispatchMessage  = user32.NewProc("DispatchMessageW")
	postQuitMessage  = user32.NewProc("PostQuitMessage")
	beginPaint       = user32.NewProc("BeginPaint")
	endPaint         = user32.NewProc("EndPaint")
	getClientRect    = user32.NewProc("GetClientRect")
	fillRect         = user32.NewProc("FillRect")
	createSolidBrush = gdi32.NewProc("CreateSolidBrush")
	deleteObject     = gdi32.NewProc("DeleteObject")
	createPen        = gdi32.NewProc("CreatePen")
	selectObject     = gdi32.NewProc("SelectObject")
	ellipse          = gdi32.NewProc("Ellipse")
	setBkMode        = gdi32.NewProc("SetBkMode")
	setTextColor     = gdi32.NewProc("SetTextColor")
	drawText         = user32.NewProc("DrawTextW")
	getDC            = user32.NewProc("GetDC")
	releaseDC        = user32.NewProc("ReleaseDC")
	setPixel         = gdi32.NewProc("SetPixel")

	// Pointer/touch
	getPointerType      *windows.LazyProc
	getPointerInfo      *windows.LazyProc
	getPointerTouchInfo *windows.LazyProc
	getPointerPenInfo   *windows.LazyProc
	getSystemMetrics    = user32.NewProc("GetSystemMetrics")
)

const (
	_PT_TOUCH = 2
	_PT_PEN   = 3
)

const (
	_POINTER_FLAG_NONE      = 0x00000000
	_POINTER_FLAG_INRANGE   = 0x00000002
	_POINTER_FLAG_INCONTACT = 0x00000004
	_POINTER_FLAG_DOWN      = 0x00010000
	_POINTER_FLAG_UPDATE    = 0x00020000
	_POINTER_FLAG_UP        = 0x00040000
)

const (
	WM_POINTERDOWN    = 0x0246
	WM_POINTERUP      = 0x0247
	WM_POINTERUPDATE  = 0x0245
	WM_POINTERLEAVE   = 0x024A
	WM_PAINT          = 0x000F
	WM_DESTROY        = 0x0002
	WM_LBUTTONDOWN    = 0x0201
	WM_KEYDOWN        = 0x0100
	VK_ESCAPE         = 0x1B
	SM_CXSCREEN       = 78
	SM_CYSCREEN       = 79
	COLOR_WINDOW      = 5
	COLOR_BTNFACE     = 15
	TRANSPARENT       = 1
	DT_CENTER         = 0x00000001
	DT_VCENTER        = 0x00000004
	DT_SINGLELINE     = 0x00000020
	WS_OVERLAPPEDWINDOW = 0x00CF0000
	WS_VISIBLE        = 0x10000000
	CW_USEDEFAULT     = 0x80000000
	PS_SOLID          = 0
)

type POINT struct{ X, Y int32 }
type RECT struct{ Left, Top, Right, Bottom int32 }
type MSG struct {
	Hwnd    windows.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      POINT
}
type WNDCLASSEX struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}
type PAINTSTRUCT struct {
	Hdc         windows.Handle
	FErase      int32
	RcPaint     RECT
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}
type POINTER_INFO struct {
	PointerType          int32
	PointerId            uint32
	FrameId              uint32
	PointerFlags         uint32
	SourceDevice         uintptr
	HwndTarget           uintptr
	PtPixelLocation      POINT
	PtHimetricLocation   POINT
	PtPixelLocationRaw   POINT
	PtHimetricLocationRaw POINT
	DwTime               uint32
	HistoryCount         uint32
	InputData            int32
	DwKeyStates          uint32
	PerformanceCount     uint64
	ButtonChangeType     int32
}
type POINTER_TOUCH_INFO struct {
	PointerInfo POINTER_INFO
	TouchFlags  uint32
	TouchMask   uint32
	RcContact   RECT
	Orientation uint32
	Pressure    uint32
}

// Active touch contacts for painting.
type contact struct {
	id uint32
	x  int32
	y  int32
	// pressure etc. could be added
}

var (
	contacts     = map[uint32]contact{}
	windowWidth  int32 = 800
	windowHeight int32 = 600
	screenW      int32
	screenH      int32
)

// Finger colours (10 fingers max).
var fingerColors = []uint32{
	0x0000FF, // red
	0x00FF00, // green
	0xFF0000, // blue
	0xFFFF00, // cyan
	0xFF00FF, // magenta
	0x00FFFF, // yellow
	0x808000, // olive
	0x008080, // teal
	0x800080, // purple
	0x408000, // lime
}

func init() {
	runtime.LockOSThread()
	getPointerType = user32.NewProc("GetPointerType")
	getPointerInfo = user32.NewProc("GetPointerInfo")
	getPointerTouchInfo = user32.NewProc("GetPointerTouchInfo")
	// Optional: GetPointerPenInfo
}

func main() {
	// Log to both stderr and a timestamped file.
	os.MkdirAll("logs", 0755)
	logName := filepath.Join("logs", time.Now().Format("touch-test_2006-01-02_150405")+".log")
	logFile, err := os.Create(logName)
	if err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, logFile))
		defer logFile.Close()
		fmt.Fprintf(os.Stderr, "logging to %s\n", logName)
	} else {
		log.SetOutput(os.Stderr)
	}
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	screenW32, _, _ := getSystemMetrics.Call(SM_CXSCREEN)
	screenH32, _, _ := getSystemMetrics.Call(SM_CYSCREEN)
	screenW = int32(screenW32)
	screenH = int32(screenH32)

	windowWidth = screenW * 3 / 4
	windowHeight = screenH * 3 / 4

	className, _ := windows.UTF16PtrFromString("TouchTestWindow")
	hInstance, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)

	wc := WNDCLASSEX{
		CbSize:        uint32(unsafe.Sizeof(WNDCLASSEX{})),
		Style:         0,
		LpfnWndProc:   windows.NewCallback(wndProc),
		HInstance:     windows.Handle(hInstance),
		HbrBackground: windows.Handle(uintptr(COLOR_WINDOW + 1)),
		LpszClassName: className,
	}
	registerClass.Call(uintptr(unsafe.Pointer(&wc)))

	title, _ := windows.UTF16PtrFromString("Touch Test — press ESC to close")
	hwnd, _, _ := createWindow.Call(
		0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		uintptr(WS_OVERLAPPEDWINDOW|WS_VISIBLE),
		uintptr((screenW-windowWidth)/2), uintptr((screenH-windowHeight)/2),
		uintptr(windowWidth), uintptr(windowHeight),
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		log.Fatalf("CreateWindowEx failed: %v", windows.GetLastError())
	}

	log.Printf("Touch Test window created: %dx%d at (%d,%d)", windowWidth, windowHeight, (screenW-windowWidth)/2, (screenH-windowHeight)/2)
	log.Println("Use your mobile client to touch this window. Press ESC to exit.")

	var msg MSG
	for {
		ret, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if ret == 0 {
			break
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func wndProc(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_POINTERDOWN, WM_POINTERUPDATE, WM_POINTERUP:
		handlePointer(hwnd, msg, wParam, lParam)

	case WM_POINTERLEAVE:
		// Pointer left detection range — treat as UP.
		handlePointerLeave(hwnd, wParam)

	case WM_PAINT:
		var ps PAINTSTRUCT
		hdc, _, _ := beginPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		if hdc != 0 {
			paint(hwnd, windows.Handle(hdc))
			endPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		}
		return 0

	case WM_KEYDOWN:
		if wParam == VK_ESCAPE {
			postQuitMessage.Call(0)
		}

	case WM_DESTROY:
		postQuitMessage.Call(0)
	}
	ret, _, _ := defWindowProc.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return ret
}

func handlePointer(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) {
	pointerID := uint32(wParam & 0xFFFF)

	// Get pointer type to filter touch only.
	var pType uint32
	r, _, _ := getPointerType.Call(uintptr(pointerID), uintptr(unsafe.Pointer(&pType)))
	if r == 0 {
		return
	}
	if pType != _PT_TOUCH {
		// Could also handle pen (PT_PEN=3)
		return
	}

	// Get basic pointer info (position + flags).
	var pi POINTER_INFO
	r, _, _ = getPointerInfo.Call(uintptr(pointerID), uintptr(unsafe.Pointer(&pi)))
	if r == 0 {
		return
	}

	// Get touch-specific info (pressure, contact area).
	var ti POINTER_TOUCH_INFO
	r, _, _ = getPointerTouchInfo.Call(uintptr(pointerID), uintptr(unsafe.Pointer(&ti)))
	_ = r // may fail, we still have pi

	flags := pi.PointerFlags
	phase := "?"
	if flags&_POINTER_FLAG_DOWN != 0 {
		phase = "DOWN"
	} else if flags&_POINTER_FLAG_UP != 0 {
		phase = "UP"
	} else if flags&_POINTER_FLAG_UPDATE != 0 {
		phase = "MOVE"
	}
	inContact := flags&_POINTER_FLAG_INCONTACT != 0
	inRange := flags&_POINTER_FLAG_INRANGE != 0

	x := pi.PtPixelLocation.X
	y := pi.PtPixelLocation.Y

	log.Printf("TOUCH id=%d phase=%s inContact=%v inRange=%v pos=(%d,%d) pressure=%d contact=(%d,%d,%d,%d)",
		pointerID, phase, inContact, inRange, x, y,
		ti.Pressure,
		ti.RcContact.Left, ti.RcContact.Top, ti.RcContact.Right, ti.RcContact.Bottom,
	)

	if msg == WM_POINTERUP || phase == "UP" {
		delete(contacts, pointerID)
	} else {
		contacts[pointerID] = contact{id: pointerID, x: x, y: y}
	}

	// Trigger repaint.
	user32.NewProc("InvalidateRect").Call(uintptr(hwnd), 0, 1)
}

func handlePointerLeave(hwnd windows.Handle, wParam uintptr) {
	pointerID := uint32(wParam & 0xFFFF)
	log.Printf("TOUCH id=%d LEAVE", pointerID)
	delete(contacts, pointerID)
	user32.NewProc("InvalidateRect").Call(uintptr(hwnd), 0, 1)
}

func paint(hwnd windows.Handle, hdc windows.Handle) {
	var rc RECT
	getClientRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rc)))

	// Background.
	bg, _, _ := createSolidBrush.Call(uintptr(COLOR_BTNFACE + 1))
	fillRect.Call(uintptr(hdc), uintptr(unsafe.Pointer(&rc)), bg)
	deleteObject.Call(bg)

	if len(contacts) == 0 {
		// Draw hint text.
		setBkMode.Call(uintptr(hdc), TRANSPARENT)
		setTextColor.Call(uintptr(hdc), 0x808080)
		text, _ := windows.UTF16PtrFromString("Touch this window with the remote client")
		drawText.Call(uintptr(hdc), uintptr(unsafe.Pointer(text)), 0xFFFFFFFF, uintptr(unsafe.Pointer(&rc)), DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		return
	}

	// Draw each touch contact.
	for _, c := range contacts {
		colorIdx := c.id % 10
		color := fingerColors[colorIdx]
		radius := int32(30)

		// Filled circle.
		brush, _, _ := createSolidBrush.Call(uintptr(color))
		pen, _, _ := createPen.Call(PS_SOLID, 2, uintptr(color))
		oldBrush, _, _ := selectObject.Call(uintptr(hdc), brush)
		oldPen, _, _ := selectObject.Call(uintptr(hdc), pen)

		ellipse.Call(uintptr(hdc),
			uintptr(c.x-radius), uintptr(c.y-radius),
			uintptr(c.x+radius), uintptr(c.y+radius),
		)

		selectObject.Call(uintptr(hdc), oldPen)
		selectObject.Call(uintptr(hdc), oldBrush)
		deleteObject.Call(pen)
		deleteObject.Call(brush)

		// Finger ID label.
		setBkMode.Call(uintptr(hdc), TRANSPARENT)
		setTextColor.Call(uintptr(hdc), 0xFFFFFF)
		label := fmt.Sprintf("%d", c.id)
		labelPtr, _ := windows.UTF16PtrFromString(label)
		labelRect := RECT{
			Left:   c.x - radius,
			Top:    c.y - radius,
			Right:  c.x + radius,
			Bottom: c.y + radius,
		}
		drawText.Call(uintptr(hdc), uintptr(unsafe.Pointer(labelPtr)), 0xFFFFFFFF, uintptr(unsafe.Pointer(&labelRect)), DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	}

	// Status text at bottom.
	status := fmt.Sprintf("Active touches: %d", len(contacts))
	statusPtr, _ := windows.UTF16PtrFromString(status)
	setTextColor.Call(uintptr(hdc), 0x000000)
	statusRect := RECT{Left: 0, Top: rc.Bottom - 30, Right: rc.Right, Bottom: rc.Bottom}
	drawText.Call(uintptr(hdc), uintptr(unsafe.Pointer(statusPtr)), 0xFFFFFFFF, uintptr(unsafe.Pointer(&statusRect)), DT_CENTER|DT_VCENTER|DT_SINGLELINE)
}
