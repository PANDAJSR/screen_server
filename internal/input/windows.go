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

func (c *windowsController) GetScreenSize() (width, height int, err error) {
	user32 := windows.NewLazyDLL("user32.dll")
	getSystemMetricsProc := user32.NewProc("GetSystemMetrics")
	w, _, _ := getSystemMetricsProc.Call(78)
	h, _, _ := getSystemMetricsProc.Call(79)
	width = int(int32(w))
	height = int(int32(h))
	if width == 0 || height == 0 {
		return 0, 0, windows.GetLastError()
	}
	return width, height, nil
}

func (c *windowsController) GetCursorInfo() (*CursorInfo, error) {
	return getCursorInfo()
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
