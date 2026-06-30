package input

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"log"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsController struct {
	touchReady   bool
	touchDevice  uintptr // HSYNTHETICPOINTERDEVICE
	touchActive  map[uint32]contactState
	touchFrameID uint32
}

type contactState struct {
	x int32
	y int32
}

func newWindowsController() (Controller, error) {
	// Declare the process as DPI-aware so that InjectSyntheticPointerInput,
	// GetDeviceCaps, and all coordinate APIs use physical screen pixels rather
	// than DPI-virtualized coordinates. Without this, touch injection on high-
	// DPI displays (125%+, 150%+) maps to the wrong screen position because
	// the client sends physical-pixel coordinates (from gdigrab) but Windows
	// interprets them as DPI-scaled coordinates.
	user32 := windows.NewLazyDLL("user32.dll")
	// SetProcessDPIAware is supported since Windows Vista; it's the simplest
	// per-process opt-out of DPI virtualization.  For per-monitor awareness
	// (Windows 8.1+) we'd need SetProcessDpiAwareness, but system-wide DPI
	// awareness is sufficient for a single-desktop screen server.
	if proc := user32.NewProc("SetProcessDPIAware"); proc.Find() == nil {
		proc.Call()
	}

	wc := &windowsController{}
	if err := wc.initTouchInjection(); err != nil {
		log.Printf("touch injection not available: %v", err)
		// Non-fatal: mouse/keyboard still work.
	}
	return wc, nil
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
		downFlags = _MOUSEEVENTF_LEFTDOWN
		upFlags = _MOUSEEVENTF_LEFTUP
	case MouseButtonRight:
		downFlags = _MOUSEEVENTF_RIGHTDOWN
		upFlags = _MOUSEEVENTF_RIGHTUP
	case MouseButtonMiddle:
		downFlags = _MOUSEEVENTF_MIDDLEDOWN
		upFlags = _MOUSEEVENTF_MIDDLEUP
	}
	if pressed {
		return sendMouseInput(downFlags, 0, 0, 0)
	}
	return sendMouseInput(upFlags, 0, 0, 0)
}

func (c *windowsController) Scroll(dx, dy int) error {
	if dy != 0 {
		if err := sendMouseInput(_MOUSEEVENTF_WHEEL, 0, 0, uint32(dy)*_WHEEL_DELTA); err != nil {
			return err
		}
	}
	if dx != 0 {
		if err := sendMouseInput(_MOUSEEVENTF_HWHEEL, 0, 0, uint32(dx)*_WHEEL_DELTA); err != nil {
			return err
		}
	}
	return nil
}

func (c *windowsController) PressKey(keyCode int) error {
	return sendKeybdInput(uint16(keyCode), 0)
}

func (c *windowsController) ReleaseKey(keyCode int) error {
	return sendKeybdInput(uint16(keyCode), _KEYEVENTF_KEYUP)
}

func (c *windowsController) GetKeyState() ([]int, error) {
	return getKeyState()
}

func (c *windowsController) ReleaseAllKeys() error {
	pressed, err := getKeyState()
	if err != nil {
		return err
	}
	for _, keyCode := range pressed {
		if kerr := sendKeybdInput(uint16(keyCode), _KEYEVENTF_KEYUP); kerr != nil {
			if err == nil {
				err = kerr
			}
		}
	}
	return err
}

func (c *windowsController) Close() error {
	if c.touchDevice != 0 {
		user32 := windows.NewLazyDLL("user32.dll")
		user32.NewProc("DestroySyntheticPointerDevice").Call(uintptr(c.touchDevice))
		c.touchDevice = 0
	}
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
	getSystemMetricsProc := user32.NewProc("GetSystemMetrics")
	originX, _, _ := getSystemMetricsProc.Call(76)
	originY, _, _ := getSystemMetricsProc.Call(77)
	return int(pt.X) - int(int32(originX)), int(pt.Y) - int(int32(originY)), nil
}

func (c *windowsController) GetScreenSize() (width, height int, err error) {
	user32 := windows.NewLazyDLL("user32.dll")
	gdi32 := windows.NewLazyDLL("gdi32.dll")

	// Get the desktop DC to query real device dimensions.
	// GetSystemMetrics(SM_CXVIRTUALSCREEN) can return incorrect values
	// under DPI virtualization or on some Windows IoT / LTSC editions.
	hdc, _, _ := user32.NewProc("GetDC").Call(0)
	if hdc == 0 {
		// Fall back to GetSystemMetrics.
		getSM := user32.NewProc("GetSystemMetrics")
		w, _, _ := getSM.Call(78) // SM_CXVIRTUALSCREEN
		h, _, _ := getSM.Call(79) // SM_CYVIRTUALSCREEN
		width = int(int32(w))
		height = int(int32(h))
	} else {
		defer user32.NewProc("ReleaseDC").Call(0, hdc)
		getDeviceCaps := gdi32.NewProc("GetDeviceCaps")
		// DESKTOPHORZRES=118, DESKTOPVERTRES=117 — physical pixel dimensions
		// of the full virtual desktop. These indices (available Win 8.1+)
		// are NOT subject to DPI virtualization, unlike HORZRES=8/VERTRES=10
		// which return DPI-scaled values for non-DPI-aware processes.
		// Using them as a safety net even after SetProcessDPIAware().
		w, _, _ := getDeviceCaps.Call(hdc, 118)
		h, _, _ := getDeviceCaps.Call(hdc, 117)
		if int32(w) == 0 || int32(h) == 0 {
			// Fallback for pre-Win8.1 (unlikely on IoT LTSC 2021).
			w, _, _ = getDeviceCaps.Call(hdc, 8)  // HORZRES
			h, _, _ = getDeviceCaps.Call(hdc, 10) // VERTRES
		}
		width = int(int32(w))
		height = int(int32(h))
	}
	if width == 0 || height == 0 {
		return 0, 0, windows.GetLastError()
	}
	return width, height, nil
}

func (c *windowsController) SetCursorPos(x, y int) error {
	w, h, err := c.GetScreenSize()
	if err != nil {
		return err
	}
	dx := normalizedAbsolute(x, w)
	dy := normalizedAbsolute(y, h)
	flags := uint32(_MOUSEEVENTF_MOVE | _MOUSEEVENTF_ABSOLUTE | _MOUSEEVENTF_VIRTUALDESK)
	return sendMouseInput(flags, dx, dy, 0)
}

func (c *windowsController) GetCursorInfo() (*CursorInfo, error) {
	return getCursorInfo()
}

func (c *windowsController) InjectTouch(contacts []TouchContact) error {
	if !c.touchReady {
		return nil
	}
	if len(contacts) == 0 {
		return nil
	}

	elemSize := int(unsafe.Sizeof(_POINTER_TYPE_INFO{}))
	c.touchFrameID++
	frameID := c.touchFrameID

	buf := make([]byte, elemSize*len(contacts))
	idx := 0

	for _, ct := range contacts {
		flags := touchPhaseToFlags(ct.Phase)
		if flags == 0 {
			continue
		}

		id := uint32(ct.ID)
		x := int32(ct.X)
		y := int32(ct.Y)

		// END events may arrive with (0,0) — use last tracked position.
		if ct.Phase == TouchPhaseEnd && x == 0 && y == 0 {
			if tracked, ok := c.touchActive[id]; ok {
				x = tracked.x
				y = tracked.y
			}
		}

		// Track state.
		if ct.Phase == TouchPhaseStart || ct.Phase == TouchPhaseMove {
			c.touchActive[id] = contactState{x: x, y: y}
		} else if ct.Phase == TouchPhaseEnd {
			delete(c.touchActive, id)
		}

		// Cast buffer element to the Go struct and populate it directly.
		ptr := (*_POINTER_TYPE_INFO)(unsafe.Pointer(&buf[idx*elemSize]))
		ptr.Type = _PT_TOUCH
		ptr.TouchInfo.PointerInfo.PointerType = int32(_PT_TOUCH)
		ptr.TouchInfo.PointerInfo.PointerId = id
		ptr.TouchInfo.PointerInfo.FrameId = frameID
		ptr.TouchInfo.PointerInfo.PointerFlags = flags
		ptr.TouchInfo.PointerInfo.PtPixelLocation.X = x
		ptr.TouchInfo.PointerInfo.PtPixelLocation.Y = y
		ptr.TouchInfo.PointerInfo.PtPixelLocationRaw.X = x
		ptr.TouchInfo.PointerInfo.PtPixelLocationRaw.Y = y
		ptr.TouchInfo.TouchFlags = 0
		ptr.TouchInfo.TouchMask = _TOUCH_MASK_CONTACTAREA | _TOUCH_MASK_ORIENTATION | _TOUCH_MASK_PRESSURE
		ptr.TouchInfo.RcContact.Left = x - 4
		ptr.TouchInfo.RcContact.Top = y - 4
		ptr.TouchInfo.RcContact.Right = x + 4
		ptr.TouchInfo.RcContact.Bottom = y + 4
		ptr.TouchInfo.RcContactRaw.Left = x - 4
		ptr.TouchInfo.RcContactRaw.Top = y - 4
		ptr.TouchInfo.RcContactRaw.Right = x + 4
		ptr.TouchInfo.RcContactRaw.Bottom = y + 4
		ptr.TouchInfo.Orientation = 0
		ptr.TouchInfo.Pressure = 1024

		log.Printf("[touch] contact id=%d phase=%s flags=0x%x pos=(%d,%d) frame=%d", ct.ID, ct.Phase, flags, x, y, frameID)
		idx++
	}

	if idx == 0 {
		return nil
	}

	log.Printf("[touch] batch n=%d frame=%d → InjectSyntheticPointerInput", idx, frameID)

	user32 := windows.NewLazyDLL("user32.dll")
	proc := user32.NewProc("InjectSyntheticPointerInput")
	r1, _, err := proc.Call(
		uintptr(c.touchDevice),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(idx),
	)
	if r1 == 0 {
		log.Printf("[touch] InjectSyntheticPointerInput failed: %v", err)
		return err
	}
	return nil
}

func touchPhaseToFlags(phase TouchPhase) uint32 {
	switch phase {
	case TouchPhaseStart:
		return _POINTER_FLAG_DOWN | _POINTER_FLAG_INRANGE | _POINTER_FLAG_INCONTACT
	case TouchPhaseMove:
		return _POINTER_FLAG_UPDATE | _POINTER_FLAG_INRANGE | _POINTER_FLAG_INCONTACT
	case TouchPhaseEnd:
		// UP alone = end the contact completely (no hovering state).
		return _POINTER_FLAG_UP
	default:
		return 0
	}
}

// ---- Win32 structs and low-level helpers ------------------------------------

type _cursorInfoEx struct {
	cbSize      uint32
	flags       uint32
	hCursor     windows.Handle
	ptScreenPos struct {
		x int32
		y int32
	}
}

type _iconInfoEx struct {
	fIcon    int32
	xHotspot int32
	yHotspot int32
	hbmMask  windows.Handle
	hbmColor windows.Handle
}

type _bitmapHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type _bitmap struct {
	bmType       int32
	bmWidth      int32
	bmHeight     int32
	bmWidthBytes int32
	bmPlanes     uint16
	bmBitsPixel  uint16
	bmBits       uintptr
}

func getCursorInfo() (*CursorInfo, error) {
	user32 := windows.NewLazyDLL("user32.dll")
	getCursorInfoProc := user32.NewProc("GetCursorInfo")

	var ci _cursorInfoEx
	ci.cbSize = uint32(unsafe.Sizeof(ci))
	r1, _, _ := getCursorInfoProc.Call(uintptr(unsafe.Pointer(&ci)))
	if r1 == 0 {
		log.Printf("[cursor] GetCursorInfo failed: %v", windows.GetLastError())
		return nil, windows.GetLastError()
	}

	if ci.hCursor == 0 {
		log.Printf("[cursor] GetCursorInfo returned hCursor=0 (cursor hidden or unavailable)")
		return nil, windows.ERROR_NOT_FOUND
	}

	log.Printf("[cursor] GetCursorInfo OK hCursor=0x%x flags=0x%x", ci.hCursor, ci.flags)
	return captureCursorImage(ci.hCursor)
}

// captureCursorImage uses different strategies depending on cursor type:
//   - Monochrome (hbmColor==0): decode AND/XOR masks directly (produces correct alpha).
//   - Color (hbmColor!=0): render via DrawIconEx into a BGRA DIB at the cursor's own size.
func captureCursorImage(hCursor windows.Handle) (*CursorInfo, error) {
	user32 := windows.NewLazyDLL("user32.dll")
	gdi32 := windows.NewLazyDLL("gdi32.dll")

	getIconInfoProc := user32.NewProc("GetIconInfo")
	var ii _iconInfoEx
	r1, _, _ := getIconInfoProc.Call(uintptr(hCursor), uintptr(unsafe.Pointer(&ii)))
	if r1 == 0 {
		log.Printf("[cursor] GetIconInfo failed for hCursor=0x%x: %v", hCursor, windows.GetLastError())
		return nil, windows.GetLastError()
	}
	hotspotX := int(ii.xHotspot)
	hotspotY := int(ii.yHotspot)
	log.Printf("[cursor] GetIconInfo OK: hbmColor=0x%x hbmMask=0x%x hotspot=(%d,%d) fIcon=%d",
		ii.hbmColor, ii.hbmMask, hotspotX, hotspotY, ii.fIcon)

	defer func() {
		if ii.hbmMask != 0 {
			gdi32.NewProc("DeleteObject").Call(uintptr(ii.hbmMask))
		}
		if ii.hbmColor != 0 {
			gdi32.NewProc("DeleteObject").Call(uintptr(ii.hbmColor))
		}
	}()

	// ---- Monochrome cursor: decode AND/XOR masks directly ----
	if ii.hbmColor == 0 && ii.hbmMask != 0 {
		log.Printf("[cursor] monochrome cursor 鈥?using mask decode path")
		img, imgW, imgH, err := decodeMaskCursor(ii.hbmMask)
		if err != nil {
			log.Printf("[cursor] decodeMaskCursor failed: %v", err)
			return nil, err
		}
		return encodeCursorPNG(img, imgW, imgH, hotspotX, hotspotY)
	}

	// ---- Color cursor: read directly from hbmColor via GetDIBits ----
	// The colour bitmap (hbmColor) is 32-bit BGRA.  For doubled bitmaps the
	// top half contains the real colour data with proper alpha; we use the
	// system cursor height as the authoritative row count.
	bmpW, bmpH := getBitmapSize(ii.hbmColor)
	log.Printf("[cursor] colour cursor: hbmColor raw=%dx%d", bmpW, bmpH)

	getSystemMetricsProc := user32.NewProc("GetSystemMetrics")
	sysW, _, _ := getSystemMetricsProc.Call(13)
	sysH, _, _ := getSystemMetricsProc.Call(14)
	cursorW := int(int32(sysW))
	cursorH := int(int32(sysH))

	readRows := bmpH
	if cursorH > 0 && bmpH == cursorH*2 {
		readRows = cursorH
		log.Printf("[cursor] colour cursor: doubled detected, reading %d rows", readRows)
	}
	imgW := bmpW
	if cursorW > 0 && cursorW < imgW {
		imgW = cursorW
	}

	hdc, _, _ := user32.NewProc("GetDC").Call(0)
	if hdc == 0 {
		return nil, windows.ERROR_INVALID_HANDLE
	}
	defer user32.NewProc("ReleaseDC").Call(0, hdc)

	hdcMem, _, _ := gdi32.NewProc("CreateCompatibleDC").Call(hdc)
	if hdcMem == 0 {
		return nil, windows.ERROR_INVALID_HANDLE
	}
	defer gdi32.NewProc("DeleteDC").Call(hdcMem)

	oldBmp, _, _ := gdi32.NewProc("SelectObject").Call(hdcMem, uintptr(ii.hbmColor))
	defer gdi32.NewProc("SelectObject").Call(hdcMem, oldBmp)

	var bi _bitmapHeader
	bi.biSize = uint32(unsafe.Sizeof(bi))
	bi.biWidth = int32(bmpW)
	bi.biHeight = -int32(readRows)
	bi.biPlanes = 1
	bi.biBitCount = 32
	bi.biCompression = 0

	buf := make([]byte, bmpW*readRows*4)
	r1, _, _ = gdi32.NewProc("GetDIBits").Call(
		hdcMem, uintptr(ii.hbmColor),
		0, uintptr(readRows),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bi)),
		0,
	)
	if r1 == 0 {
		log.Printf("[cursor] colour GetDIBits failed: %v", windows.GetLastError())
		return nil, windows.GetLastError()
	}

	srcStride := bmpW * 4
	nonZero := 0
	for _, b := range buf {
		if b != 0 {
			nonZero++
		}
	}
	log.Printf("[cursor] colour buffer nonZero=%d/%d (%.1f%%)", nonZero, len(buf),
		float64(nonZero)/float64(len(buf))*100)

	img := image.NewNRGBA(image.Rect(0, 0, imgW, readRows))
	for y := 0; y < readRows; y++ {
		srcRow := buf[y*srcStride : (y+1)*srcStride]
		dstRow := img.Pix[y*img.Stride : (y+1)*img.Stride]
		for x := 0; x < imgW; x++ {
			off := x * 4
			dstRow[off+0] = srcRow[off+2]
			dstRow[off+1] = srcRow[off+1]
			dstRow[off+2] = srcRow[off+0]
			dstRow[off+3] = srcRow[off+3]
		}
	}

	alphaZero, alphaFull := 0, 0
	for i := 3; i < len(img.Pix); i += 4 {
		switch img.Pix[i] {
		case 0:
			alphaZero++
		case 255:
			alphaFull++
		}
	}
	log.Printf("[cursor] colour output %dx%d alpha_zero=%d alpha_255=%d total=%d",
		imgW, readRows, alphaZero, alphaFull, imgW*readRows)

	return encodeCursorPNG(img, imgW, readRows, hotspotX, hotspotY)
}

// getBitmapSize returns the width and height of a GDI bitmap.
func getBitmapSize(hbm windows.Handle) (w, h int) {
	if hbm == 0 {
		return 0, 0
	}
	gdi32 := windows.NewLazyDLL("gdi32.dll")
	var bm _bitmap
	r1, _, _ := gdi32.NewProc("GetObjectW").Call(uintptr(hbm), uintptr(unsafe.Sizeof(bm)), uintptr(unsafe.Pointer(&bm)))
	if r1 == 0 {
		return 0, 0
	}
	return int(bm.bmWidth), int(bm.bmHeight)
}

// decodeMaskCursor decodes a monochrome cursor from its AND/XOR mask bitmap.
// The mask bitmap stores AND mask (top half) and XOR mask (bottom half) as 1 bpp.
func decodeMaskCursor(hbmMask windows.Handle) (*image.NRGBA, int, int, error) {
	gdi32 := windows.NewLazyDLL("gdi32.dll")
	user32 := windows.NewLazyDLL("user32.dll")

	bmpW, fullH := getBitmapSize(hbmMask)
	if bmpW <= 0 || fullH <= 0 {
		return nil, 0, 0, windows.ERROR_INVALID_DATA
	}
	realH := fullH / 2 // AND (top) + XOR (bottom) stacked
	log.Printf("[cursor] decodeMaskCursor: mask=%dx%d real=%dx%d", bmpW, fullH, bmpW, realH)

	hdc, _, _ := user32.NewProc("GetDC").Call(0)
	if hdc == 0 {
		return nil, 0, 0, windows.ERROR_INVALID_HANDLE
	}
	defer user32.NewProc("ReleaseDC").Call(0, hdc)

	hdcMem, _, _ := gdi32.NewProc("CreateCompatibleDC").Call(hdc)
	if hdcMem == 0 {
		return nil, 0, 0, windows.ERROR_INVALID_HANDLE
	}
	defer gdi32.NewProc("DeleteDC").Call(hdcMem)

	oldBmp, _, _ := gdi32.NewProc("SelectObject").Call(hdcMem, uintptr(hbmMask))
	defer gdi32.NewProc("SelectObject").Call(hdcMem, oldBmp)

	var bi _bitmapHeader
	bi.biSize = uint32(unsafe.Sizeof(bi))
	bi.biWidth = int32(bmpW)
	bi.biHeight = -int32(fullH)
	bi.biPlanes = 1
	bi.biBitCount = 1
	bi.biCompression = 0

	rowBytes := ((bmpW + 31) / 32) * 4
	buf := make([]byte, rowBytes*fullH)
	r1, _, _ := gdi32.NewProc("GetDIBits").Call(
		hdcMem, uintptr(hbmMask),
		0, uintptr(fullH),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bi)),
		0,
	)
	if r1 == 0 {
		log.Printf("[cursor] decodeMaskCursor: GetDIBits failed: %v", windows.GetLastError())
		return nil, 0, 0, windows.GetLastError()
	}

	opaqueCount, transpCount := 0, 0
	img := image.NewNRGBA(image.Rect(0, 0, bmpW, realH))
	for y := 0; y < realH; y++ {
		andRow := buf[y*rowBytes : (y+1)*rowBytes]
		xorRow := buf[(y+realH)*rowBytes : (y+realH+1)*rowBytes]
		for x := 0; x < bmpW; x++ {
			byteIdx := x / 8
			bitIdx := 7 - (x % 8)
			andBit := (andRow[byteIdx] >> bitIdx) & 1
			xorBit := (xorRow[byteIdx] >> bitIdx) & 1

			offset := (y*bmpW + x) * 4
			if andBit == 0 && xorBit == 0 {
				// opaque black
				img.Pix[offset+0] = 0
				img.Pix[offset+1] = 0
				img.Pix[offset+2] = 0
				img.Pix[offset+3] = 255
				opaqueCount++
			} else if andBit == 1 && xorBit == 0 {
				// transparent
				img.Pix[offset+0] = 0
				img.Pix[offset+1] = 0
				img.Pix[offset+2] = 0
				img.Pix[offset+3] = 0
				transpCount++
			} else if andBit == 0 && xorBit == 1 {
				// opaque white
				img.Pix[offset+0] = 255
				img.Pix[offset+1] = 255
				img.Pix[offset+2] = 255
				img.Pix[offset+3] = 255
				opaqueCount++
			} else {
				// invert 鈥?treat as black
				img.Pix[offset+0] = 0
				img.Pix[offset+1] = 0
				img.Pix[offset+2] = 0
				img.Pix[offset+3] = 255
				opaqueCount++
			}
		}
	}
	log.Printf("[cursor] decodeMaskCursor: output %dx%d opaque=%d transp=%d total=%d",
		bmpW, realH, opaqueCount, transpCount, bmpW*realH)

	return img, bmpW, realH, nil
}

func encodeCursorPNG(img image.Image, w, h, hotspotX, hotspotY int) (*CursorInfo, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Printf("[cursor] encodeCursorPNG: png.Encode failed: %v", err)
		return nil, err
	}
	b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
	log.Printf("[cursor] encodeCursorPNG: pngRaw=%d base64=%d chars", buf.Len(), len(b64))
	return &CursorInfo{
		ImageData: b64,
		Width:     w,
		Height:    h,
		HotspotX:  hotspotX,
		HotspotY:  hotspotY,
	}, nil
}

// ---- SendInput structs and helpers ------------------------------------------

const (
	_INPUT_MOUSE    = 0
	_INPUT_KEYBOARD = 1
)

const (
	_MOUSEEVENTF_MOVE        = 0x0001
	_MOUSEEVENTF_LEFTDOWN    = 0x0002
	_MOUSEEVENTF_LEFTUP      = 0x0004
	_MOUSEEVENTF_RIGHTDOWN   = 0x0008
	_MOUSEEVENTF_RIGHTUP     = 0x0010
	_MOUSEEVENTF_MIDDLEDOWN  = 0x0020
	_MOUSEEVENTF_MIDDLEUP    = 0x0040
	_MOUSEEVENTF_ABSOLUTE    = 0x8000
	_MOUSEEVENTF_WHEEL       = 0x0800
	_MOUSEEVENTF_HWHEEL      = 0x1000
	_MOUSEEVENTF_VIRTUALDESK = 0x4000
	_WHEEL_DELTA             = 120
)

const (
	_KEYEVENTF_KEYUP = 0x0002
)

type _MOUSEINPUT struct {
	Dx          int32
	Dy          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

type _KEYBDINPUT struct {
	WVk         uint16
	WScan       uint16
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

func sendMouseInput(flags uint32, dx, dy int32, mouseData uint32) error {
	user32 := windows.NewLazyDLL("user32.dll")
	sendInputProc := user32.NewProc("SendInput")

	const inputSize = 40
	var buf [inputSize]byte

	*(*uint32)(unsafe.Pointer(&buf[0])) = _INPUT_MOUSE

	mi := (*_MOUSEINPUT)(unsafe.Pointer(&buf[8]))
	mi.Dx = dx
	mi.Dy = dy
	mi.MouseData = mouseData
	mi.DwFlags = flags
	mi.Time = 0
	mi.DwExtraInfo = 0

	r1, _, _ := sendInputProc.Call(1, uintptr(unsafe.Pointer(&buf[0])), uintptr(inputSize))
	if r1 == 0 {
		return windows.GetLastError()
	}
	return nil
}

func sendKeybdInput(vk uint16, flags uint32) error {
	user32 := windows.NewLazyDLL("user32.dll")
	sendInputProc := user32.NewProc("SendInput")

	const inputSize = 40
	var buf [inputSize]byte

	*(*uint32)(unsafe.Pointer(&buf[0])) = _INPUT_KEYBOARD

	ki := (*_KEYBDINPUT)(unsafe.Pointer(&buf[8]))
	ki.WVk = vk
	ki.WScan = 0
	ki.DwFlags = flags
	ki.Time = 0
	ki.DwExtraInfo = 0

	r1, _, _ := sendInputProc.Call(1, uintptr(unsafe.Pointer(&buf[0])), uintptr(inputSize))
	if r1 == 0 {
		return windows.GetLastError()
	}
	return nil
}

func normalizedAbsolute(pixel, screenDim int) int32 {
	return int32((pixel * 65536) / screenDim)
}

func mouseMove(x, y int32) error {
	return sendMouseInput(_MOUSEEVENTF_MOVE, x, y, 0)
}

func getKeyState() ([]int, error) {
	user32 := windows.NewLazyDLL("user32.dll")
	getAsyncKeyStateProc := user32.NewProc("GetAsyncKeyState")

	var pressed []int
	for vk := 1; vk <= 254; vk++ {
		ret, _, _ := getAsyncKeyStateProc.Call(uintptr(vk))
		if ret&0x8000 != 0 {
			pressed = append(pressed, vk)
		}
	}
	return pressed, nil
}

// ---- Touch injection (InjectSyntheticPointerInput) ----------------------------

const (
	// POINTER_INPUT_TYPE
	_PT_POINTER   = 1
	_PT_TOUCH     = 2
	_PT_PEN       = 3
	_PT_MOUSE     = 4
	_PT_TOUCHPAD  = 5

	// POINTER_FEEDBACK_MODE
	_POINTER_FEEDBACK_DEFAULT  = 1
	_POINTER_FEEDBACK_INDIRECT = 2
	_POINTER_FEEDBACK_NONE     = 3

	// POINTER_FLAGS
	_POINTER_FLAG_NONE           = 0x00000000
	_POINTER_FLAG_INRANGE        = 0x00000002
	_POINTER_FLAG_INCONTACT      = 0x00000004
	_POINTER_FLAG_FIRSTBUTTON    = 0x00000010
	_POINTER_FLAG_SECONDBUTTON   = 0x00000020
	_POINTER_FLAG_THIRDBUTTON    = 0x00000040
	_POINTER_FLAG_FOURTHBUTTON   = 0x00000080
	_POINTER_FLAG_FIFTHBUTTON    = 0x00000100
	_POINTER_FLAG_PRIMARY        = 0x00002000
	_POINTER_FLAG_CONFIDENCE     = 0x00004000
	_POINTER_FLAG_CANCELED       = 0x00008000
	_POINTER_FLAG_DOWN           = 0x00010000
	_POINTER_FLAG_UPDATE         = 0x00020000
	_POINTER_FLAG_UP             = 0x00040000
	_POINTER_FLAG_WHEEL          = 0x00080000
	_POINTER_FLAG_HWHEEL         = 0x00100000
	_POINTER_FLAG_CAPTURECHANGED = 0x00200000
	_POINTER_FLAG_HASTRANSFORM   = 0x00400000

	// TOUCH_FLAGS
	_TOUCH_FLAG_NONE = 0x00000000

	// TOUCH_MASK
	_TOUCH_MASK_NONE        = 0x00000000
	_TOUCH_MASK_CONTACTAREA = 0x00000001
	_TOUCH_MASK_ORIENTATION = 0x00000002
	_TOUCH_MASK_PRESSURE    = 0x00000004

	// POINTER_BUTTON_CHANGE_TYPE
	_POINTER_CHANGE_NONE       = 0
	_POINTER_CHANGE_FIRSTBUTTON_DOWN  = 1
	_POINTER_CHANGE_FIRSTBUTTON_UP    = 2
	_POINTER_CHANGE_SECONDBUTTON_DOWN = 3
	_POINTER_CHANGE_SECONDBUTTON_UP   = 4
	_POINTER_CHANGE_THIRDBUTTON_DOWN  = 5
	_POINTER_CHANGE_THIRDBUTTON_UP    = 6
	_POINTER_CHANGE_FOURTHBUTTON_DOWN = 7
	_POINTER_CHANGE_FOURTHBUTTON_UP   = 8
	_POINTER_CHANGE_FIFTHBUTTON_DOWN  = 9
	_POINTER_CHANGE_FIFTHBUTTON_UP    = 10

	_MAX_TOUCH_COUNT = 10
)

type _POINT struct {
	X int32
	Y int32
}

type _RECT struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type _POINTER_INFO struct {
	PointerType          int32
	PointerId            uint32
	FrameId              uint32
	PointerFlags         uint32
	SourceDevice         uintptr
	HwndTarget           uintptr
	PtPixelLocation      _POINT
	PtHimetricLocation   _POINT
	PtPixelLocationRaw   _POINT
	PtHimetricLocationRaw _POINT
	DwTime               uint32
	HistoryCount         uint32
	InputData            int32
	DwKeyStates          uint32
	PerformanceCount     uint64
	ButtonChangeType     int32
}

type _POINTER_TOUCH_INFO struct {
	PointerInfo   _POINTER_INFO
	TouchFlags    uint32
	TouchMask     uint32
	RcContact     _RECT
	RcContactRaw  _RECT
	Orientation   uint32
	Pressure      uint32
}

type _POINTER_TYPE_INFO struct {
	Type      int32
	_pad      uint32 // explicit padding to align the union
	TouchInfo _POINTER_TOUCH_INFO
}

// initTouchInjection creates a synthetic touch device.
func (c *windowsController) initTouchInjection() error {
	c.touchActive = make(map[uint32]contactState)

	user32 := windows.NewLazyDLL("user32.dll")
	proc := user32.NewProc("CreateSyntheticPointerDevice")
	if err := proc.Find(); err != nil {
		log.Printf("[touch] CreateSyntheticPointerDevice not found: %v", err)
		return err
	}

	r1, _, err := proc.Call(
		uintptr(_PT_TOUCH),
		uintptr(_MAX_TOUCH_COUNT),
		uintptr(_POINTER_FEEDBACK_DEFAULT),
	)
	if r1 == 0 {
		log.Printf("[touch] CreateSyntheticPointerDevice failed: %v", err)
		return err
	}
	c.touchDevice = r1
	c.touchReady = true
	log.Printf("[touch] touch device created (handle=0x%x)", r1)
	return nil
}
