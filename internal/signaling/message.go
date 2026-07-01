package signaling

import (
	"context"
	"encoding/json"
)

const (
	MessageTypeHello       = "hello"
	MessageTypeWelcome     = "welcome"
	MessageTypePeerJoined  = "peer-joined"
	MessageTypePeerLeft    = "peer-left"
	MessageTypePing        = "ping"
	MessageTypePong        = "pong"
	MessageTypeOffer       = "offer"
	MessageTypeAnswer      = "answer"
	MessageTypeCandidate   = "candidate"
	MessageTypeError       = "error"

	// Quality preset message — client selects a quality/latency trade-off.
	MessageTypeQualityPreset = "quality-preset"

	// Capture settings — client selects capture mode (desktop/display/window).
	MessageTypeCaptureSettings = "capture-settings"

	// Capture region — server sends the active capture area for input mapping.
	MessageTypeCaptureRegion = "capture-region"

	// Input control message types
	MessageTypeInputMode          = "input-mode"           // enable/disable control mode
	MessageTypeInputKeyDown       = "input-keydown"        // key press
	MessageTypeInputKeyUp         = "input-keyup"          // key release
	MessageTypeInputMouseMove     = "input-mousemove"      // relative mouse movement
	MessageTypeInputMouseMoveAbs  = "input-mousemove-abs"  // absolute mouse position
	MessageTypeInputMouseBtn      = "input-mousebtn"       // mouse button press/release
	MessageTypeInputScroll        = "input-scroll"         // mouse scroll
	MessageTypeCursorPos          = "cursor-pos"           // cursor position update from server
	MessageTypeCursorImage        = "cursor-image"         // cursor image data from server
	MessageTypeScreenSize         = "screen-size"          // screen dimensions from server
	MessageTypeInputKeyState      = "input-key-state"      // server → client: currently pressed keys
	MessageTypeInputReleaseAll    = "input-release-all"    // client → server: release all keys
	MessageTypeInputTouch         = "input-touch"          // client → server: multi-touch batch

	// Latency detection message types (client → server). The client drives the
	// timer; the server only toggles a topmost colored window on screen.
	MessageTypeLatencyStart = "latency-start" // begin test: server shows blue window
	MessageTypeLatencyBlue  = "latency-blue"  // client saw blue: server flips to red
	MessageTypeLatencyRed   = "latency-red"   // client saw red: server closes window

	// Client→server log forwarding. The frontend sends structured log entries so
	// all timing data appears in the server log stream tagged [frontend].
	MessageTypeLog = "log"
)

// Message is the only JSON envelope used by the signaling channel.
// SDP and ICE payloads stay as RawMessage so Step 3 can pass browser-native
// RTCSessionDescriptionInit / RTCIceCandidateInit objects without lossy mapping.
type Message struct {
	Type    string          `json:"type"`
	Room    string          `json:"room,omitempty"`
	From    string          `json:"from,omitempty"`
	To      string          `json:"to,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type ServerSignal struct {
	ClientID string
	Room     string
	Message  Message
	Send     func(Message)
}

type ServerHandler interface {
	OnSignal(context.Context, ServerSignal)
	OnDisconnect(clientID string)
}
