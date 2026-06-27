package input

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
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

func (c *windowsController) GetKeyState() ([]int, error) {
	return getKeyState()
}

func (c *windowsController) ReleaseAllKeys() error {
	pressed, err := getKeyState()
	if err != nil {
		return err
	}
	for _, keyCode := range pressed {
		// KEYEVENTF_KEYUP = 0x0002
		if kerr := keybdEvent(uint16(keyCode), 0, 0x0002); kerr != nil {
			// Collect first error but keep trying to release all keys
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
	return int(pt.X), int(pt.Y), nil
}

func (c *windowsController) SetCursorPos(x, y int) error {
	user32 := windows.NewLazyDLL("user32.dll")
	setCursorPosProc := user32.NewProc("SetCursorPos")
	r1, _, _ := setCursorPosProc.Call(uintptr(x), uintptr(y))
	if r1 == 0 {
		return windows.GetLastError()
	}
	return nil
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

func (c *windowsController) GetCursorInfo() (*CursorInfo, error) {
	return getCursorInfo()
}

// ---- Win32 structs and low-level helpers ------------------------------------

type _point struct {
	x int32
	y int32
}

type _cursorInfoEx struct {
	cbSize      uint32
	flags       uint32
	hCursor     windows.Handle
	ptScreenPos _point
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
		return nil, windows.GetLastError()
	}

	if ci.hCursor == 0 {
		return nil, windows.ERROR_NOT_FOUND
	}

	return captureCursorImage(ci.hCursor)
}

func captureCursorImage(hCursor windows.Handle) (*CursorInfo, error) {
	user32 := windows.NewLazyDLL("user32.dll")
	getIconInfoProc := user32.NewProc("GetIconInfo")

	var info _iconInfoEx
	r1, _, _ := getIconInfoProc.Call(uintptr(hCursor), uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		return nil, windows.GetLastError()
	}
	// hbmMask and hbmColor must be deleted after use
	defer func() {
		gdi32 := windows.NewLazyDLL("gdi32.dll")
		if info.hbmMask != 0 {
			gdi32.NewProc("DeleteObject").Call(uintptr(info.hbmMask))
		}
		if info.hbmColor != 0 {
			gdi32.NewProc("DeleteObject").Call(uintptr(info.hbmColor))
		}
	}()

	// Prefer the color bitmap for 32-bit cursors
	if info.hbmColor != 0 {
		img, w, h, err := dibToNRGBA(info.hbmColor)
		if err != nil {
			return nil, err
		}
		return encodeCursorPNG(img, w, h, int(info.xHotspot), int(info.yHotspot))
	}

	// Fallback: use the mask bitmap for monochrome cursors
	if info.hbmMask != 0 {
		img, w, h, err := maskToNRGBA(info.hbmMask)
		if err != nil {
			return nil, err
		}
		return encodeCursorPNG(img, w, h, int(info.xHotspot), int(info.yHotspot))
	}

	return nil, windows.ERROR_NOT_FOUND
}

func dibToNRGBA(hbm windows.Handle) (*image.NRGBA, int, int, error) {
	gdi32 := windows.NewLazyDLL("gdi32.dll")
	getObjectProc := gdi32.NewProc("GetObjectW")

	var bm _bitmap
	r1, _, _ := getObjectProc.Call(uintptr(hbm), uintptr(unsafe.Sizeof(bm)), uintptr(unsafe.Pointer(&bm)))
	if r1 == 0 {
		return nil, 0, 0, windows.GetLastError()
	}

	w := int(bm.bmWidth)
	h := int(bm.bmHeight)

	// For 32-bit color cursors, bmHeight is typically 2× the image height
	// (color bitmap stacked on top of mask). Detect and correct.
	if bm.bmBitsPixel == 32 && h > 0 && h%2 == 0 {
		// Check if the bottom half looks like a mask (only check when we have the bitmap)
		// For safety we always divide by 2 for 32-bit cursor bitmaps
		h = h / 2
	}

	// Get screen DC and create compatible DC
	user32 := windows.NewLazyDLL("user32.dll")
	getDCProc := user32.NewProc("GetDC")
	hdc, _, _ := getDCProc.Call(0)
	if hdc == 0 {
		return nil, 0, 0, windows.ERROR_INVALID_HANDLE
	}
	defer user32.NewProc("ReleaseDC").Call(0, hdc)

	hdcMem, _, _ := gdi32.NewProc("CreateCompatibleDC").Call(hdc)
	if hdcMem == 0 {
		return nil, 0, 0, windows.ERROR_INVALID_HANDLE
	}
	defer gdi32.NewProc("DeleteDC").Call(hdcMem)

	oldBmp, _, _ := gdi32.NewProc("SelectObject").Call(hdcMem, uintptr(hbm))
	defer gdi32.NewProc("SelectObject").Call(hdcMem, oldBmp)

	var bi _bitmapHeader
	bi.biSize = uint32(unsafe.Sizeof(bi))
	bi.biWidth = int32(w)
	bi.biHeight = -int32(h) // negative = top-down DIB
	bi.biPlanes = 1
	bi.biBitCount = 32
	bi.biCompression = 0 // BI_RGB

	buf := make([]byte, w*h*4)
	r1, _, _ = gdi32.NewProc("GetDIBits").Call(
		hdcMem, uintptr(hbm),
		0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bi)),
		0, // DIB_RGB_COLORS
	)
	if r1 == 0 {
		return nil, 0, 0, windows.GetLastError()
	}

	// Convert BGRA → NRGBA
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		offset := i * 4
		img.Pix[offset+0] = buf[offset+2] // R ← B
		img.Pix[offset+1] = buf[offset+1] // G ← G
		img.Pix[offset+2] = buf[offset+0] // B ← R
		img.Pix[offset+3] = buf[offset+3] // A ← A
	}

	// Clean up: delete the temp buf to help GC
	buf = nil

	return img, w, h, nil
}

func maskToNRGBA(hbm windows.Handle) (*image.NRGBA, int, int, error) {
	gdi32 := windows.NewLazyDLL("gdi32.dll")
	getObjectProc := gdi32.NewProc("GetObjectW")

	var bm _bitmap
	r1, _, _ := getObjectProc.Call(uintptr(hbm), uintptr(unsafe.Sizeof(bm)), uintptr(unsafe.Pointer(&bm)))
	if r1 == 0 {
		return nil, 0, 0, windows.GetLastError()
	}

	w := int(bm.bmWidth)
	fullH := int(bm.bmHeight)
	if fullH <= 0 {
		return nil, 0, 0, windows.ERROR_INVALID_DATA
	}
	h := fullH / 2 // mask is AND mask (top) + XOR mask (bottom)

	user32 := windows.NewLazyDLL("user32.dll")
	getDCProc := user32.NewProc("GetDC")
	hdc, _, _ := getDCProc.Call(0)
	if hdc == 0 {
		return nil, 0, 0, windows.ERROR_INVALID_HANDLE
	}
	defer user32.NewProc("ReleaseDC").Call(0, hdc)

	hdcMem, _, _ := gdi32.NewProc("CreateCompatibleDC").Call(hdc)
	if hdcMem == 0 {
		return nil, 0, 0, windows.ERROR_INVALID_HANDLE
	}
	defer gdi32.NewProc("DeleteDC").Call(hdcMem)

	oldBmp, _, _ := gdi32.NewProc("SelectObject").Call(hdcMem, uintptr(hbm))
	defer gdi32.NewProc("SelectObject").Call(hdcMem, oldBmp)

	// Get the full mask as a 1-bpp DIB (each row is DWORD-aligned)
	var bi _bitmapHeader
	bi.biSize = uint32(unsafe.Sizeof(bi))
	bi.biWidth = int32(w)
	bi.biHeight = -int32(fullH)
	bi.biPlanes = 1
	bi.biBitCount = 1
	bi.biCompression = 0

	rowBytes := ((w + 31) / 32) * 4
	buf := make([]byte, rowBytes*fullH)
	r1, _, _ = gdi32.NewProc("GetDIBits").Call(
		hdcMem, uintptr(hbm),
		0, uintptr(fullH),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bi)),
		0,
	)
	if r1 == 0 {
		return nil, 0, 0, windows.GetLastError()
	}

	// AND mask: first h rows; XOR mask: last h rows
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		andRow := buf[y*rowBytes : (y+1)*rowBytes]
		xorRow := buf[(y+h)*rowBytes : (y+h+1)*rowBytes]
		for x := 0; x < w; x++ {
			byteIdx := x / 8
			bitIdx := 7 - (x % 8)
			andBit := (andRow[byteIdx] >> bitIdx) & 1
			xorBit := (xorRow[byteIdx] >> bitIdx) & 1

			offset := (y*w + x) * 4
			if andBit == 0 && xorBit == 0 {
				// opaque black
				img.Pix[offset+0] = 0
				img.Pix[offset+1] = 0
				img.Pix[offset+2] = 0
				img.Pix[offset+3] = 255
			} else if andBit == 1 && xorBit == 0 {
				// transparent
				img.Pix[offset+0] = 0
				img.Pix[offset+1] = 0
				img.Pix[offset+2] = 0
				img.Pix[offset+3] = 0
			} else if andBit == 0 && xorBit == 1 {
				// opaque white
				img.Pix[offset+0] = 255
				img.Pix[offset+1] = 255
				img.Pix[offset+2] = 255
				img.Pix[offset+3] = 255
			} else {
				// invert (andBit=1, xorBit=1) – treat as black
				img.Pix[offset+0] = 0
				img.Pix[offset+1] = 0
				img.Pix[offset+2] = 0
				img.Pix[offset+3] = 255
			}
		}
	}

	return img, w, h, nil
}

func encodeCursorPNG(img image.Image, w, h, hotspotX, hotspotY int) (*CursorInfo, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return &CursorInfo{
		ImageData: base64.StdEncoding.EncodeToString(buf.Bytes()),
		Width:     w,
		Height:    h,
		HotspotX:  hotspotX,
		HotspotY:  hotspotY,
	}, nil
}

// ---- low-level input helpers (unchanged signatures) --------------------------

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

func getKeyState() ([]int, error) {
	user32 := windows.NewLazyDLL("user32.dll")
	getAsyncKeyStateProc := user32.NewProc("GetAsyncKeyState")

	var pressed []int
	for vk := 1; vk <= 254; vk++ {
		ret, _, _ := getAsyncKeyStateProc.Call(uintptr(vk))
		// MSB (bit 15) set = key is currently down
		if ret&0x8000 != 0 {
			pressed = append(pressed, vk)
		}
	}
	return pressed, nil
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
