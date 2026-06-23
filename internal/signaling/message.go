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

	// Input control message types
	MessageTypeInputMode      = "input-mode"       // enable/disable control mode
	MessageTypeInputKeyDown   = "input-keydown"    // key press
	MessageTypeInputKeyUp     = "input-keyup"      // key release
	MessageTypeInputMouseMove = "input-mousemove"  // mouse movement
	MessageTypeInputMouseBtn  = "input-mousebtn"   // mouse button press/release
	MessageTypeInputScroll    = "input-scroll"     // mouse scroll
	MessageTypeCursorPos      = "cursor-pos"       // cursor position update from server
	MessageTypeScreenSize     = "screen-size"      // screen dimensions from server
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
