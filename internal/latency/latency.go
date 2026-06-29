package latency

import (
	"fmt"
)

// Controller drives a topmost on-screen colored window used for end-to-end
// video latency detection. The client times the blue→red flip it observes in
// the decoded video; the server only toggles the window.
type Controller interface {
	// ShowBlue creates the window (if needed) filled with pure blue.
	ShowBlue() error
	// ShowRed flips the existing window to pure red.
	ShowRed() error
	// Close destroys the window.
	Close() error
}

// colorRef packs an RGB triple into a Windows COLORREF (0x00BBGGRR).
func colorRef(r, g, b uint8) uint32 {
	return uint32(b)<<16 | uint32(g)<<8 | uint32(r)
}

var (
	colorBlue = colorRef(0, 0, 255) // 0x00FF0000
	colorRed  = colorRef(255, 0, 0) // 0x000000FF
)

// errUnsupported is returned by the non-Windows stub.
var errUnsupported = fmt.Errorf("latency detection requires windows")
