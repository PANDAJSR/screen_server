package signaling

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"screen_server/internal/input"
	"screen_server/internal/latency"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 5 * time.Second
	pongWait       = 30 * time.Second
	pingPeriod     = 10 * time.Second
	maxMessageSize = 1 << 20
)

type Hub struct {
	register   chan *Client
	unregister chan *Client
	inbound    chan inboundMessage
	rooms      map[string]map[*Client]bool
	handler    ServerHandler
	inputCtrl  input.Controller
	latencyCtl latency.Controller
}

type inboundMessage struct {
	client  *Client
	message Message
}

func NewHub(handler ServerHandler) (*Hub, error) {
	inputCtrl, err := input.NewController()
	if err != nil {
		log.Printf("input controller not available: %v", err)
	}

	latencyCtl, err := latency.NewController()
	if err != nil {
		log.Printf("latency controller not available: %v", err)
	}

	hub := &Hub{
		register:   make(chan *Client, 16),
		unregister: make(chan *Client, 16),
		inbound:    make(chan inboundMessage, 128),
		rooms:      make(map[string]map[*Client]bool),
		handler:    handler,
		inputCtrl:  inputCtrl,
		latencyCtl: latencyCtl,
	}

	if inputCtrl != nil {
		go hub.cursorPoll()
		go hub.cursorImagePoll()
		go hub.keyStatePoll()
	}

	return hub, nil
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			if h.rooms[client.room] == nil {
				h.rooms[client.room] = make(map[*Client]bool)
			}
			h.rooms[client.room][client] = true
			client.sendJSON(Message{
				Type: MessageTypeWelcome,
				Room: client.room,
				To:   client.id,
				Payload: mustJSON(map[string]string{
					"clientId": client.id,
					"room":     client.room,
				}),
			})

			if h.inputCtrl != nil {
				if w, h, err := h.inputCtrl.GetScreenSize(); err == nil {
					client.sendJSON(Message{
						Type: MessageTypeScreenSize,
						Room: client.room,
						To:   client.id,
						Payload: mustJSON(struct {
							Width  int `json:"width"`
							Height int `json:"height"`
						}{Width: w, Height: h}),
					})
				}
				if info, err := h.inputCtrl.GetCursorInfo(); err == nil && info != nil {
					client.sendJSON(Message{
						Type: MessageTypeCursorImage,
						Room: client.room,
						To:   client.id,
						Payload: mustJSON(struct {
							Data     string `json:"data"`
							Width    int    `json:"width"`
							Height   int    `json:"height"`
							HotspotX int    `json:"hotspotX"`
							HotspotY int    `json:"hotspotY"`
						}{Data: info.ImageData, Width: info.Width, Height: info.Height, HotspotX: info.HotspotX, HotspotY: info.HotspotY}),
					})
				}
			}

			h.broadcast(client.room, client, Message{
				Type: MessageTypePeerJoined,
				Room: client.room,
				From: client.id,
			})
			log.Printf("signaling client joined room=%s id=%s", client.room, client.id)

		case client := <-h.unregister:
			if clients := h.rooms[client.room]; clients != nil {
				if _, ok := clients[client]; ok {
					delete(clients, client)
					close(client.send)
					h.broadcast(client.room, client, Message{
						Type: MessageTypePeerLeft,
						Room: client.room,
						From: client.id,
					})
					if len(clients) == 0 {
						delete(h.rooms, client.room)
					}
					if h.handler != nil {
						go h.handler.OnDisconnect(client.id)
					}
					log.Printf("signaling client left room=%s id=%s", client.room, client.id)
				}
			}

		case inbound := <-h.inbound:
			msg := inbound.message
			msg.Room = inbound.client.room
			msg.From = inbound.client.id
			if msg.Type == MessageTypePing {
				var ts int64
				_ = json.Unmarshal(msg.Payload, &ts)
				inbound.client.sendJSON(Message{
					Type: MessageTypePong,
					Room: inbound.client.room,
					To:   inbound.client.id,
					Payload: mustJSON(map[string]int64{
						"ts": ts,
					}),
				})
				continue
			}
			if h.handler != nil && (msg.Type == MessageTypeOffer || msg.Type == MessageTypeCandidate || msg.Type == MessageTypeInputMode) {
				signal := ServerSignal{
					ClientID: inbound.client.id,
					Room:     inbound.client.room,
					Message:  msg,
					Send: func(reply Message) {
						reply.Room = inbound.client.room
						reply.From = "server"
						reply.To = inbound.client.id
						inbound.client.sendJSON(reply)
					},
				}
				go h.handler.OnSignal(context.Background(), signal)
				continue
			}
			if h.handleLatencyMessage(msg) {
				continue
			}
			if h.handleInputMessage(msg) {
				continue
			}
			if msg.To != "" {
				h.sendTo(inbound.client.room, msg.To, msg)
				continue
			}
			h.broadcast(inbound.client.room, inbound.client, msg)
		}
	}
}

func (h *Hub) handleLatencyMessage(msg Message) bool {
	switch msg.Type {
	case MessageTypeLatencyStart:
		if h.latencyCtl == nil {
			log.Printf("latency-start ignored: controller unavailable")
			return true
		}
		if err := h.latencyCtl.ShowBlue(); err != nil {
			log.Printf("latency-start error: %v", err)
		}
	case MessageTypeLatencyBlue:
		if h.latencyCtl == nil {
			return true
		}
		if err := h.latencyCtl.ShowRed(); err != nil {
			log.Printf("latency-blue error: %v", err)
		}
	case MessageTypeLatencyRed:
		if h.latencyCtl == nil {
			return true
		}
		if err := h.latencyCtl.Close(); err != nil {
			log.Printf("latency-red error: %v", err)
		}
	default:
		return false
	}
	return true
}

func (h *Hub) handleInputMessage(msg Message) bool {
	if h.inputCtrl == nil {
		return false
	}

	switch msg.Type {
	case MessageTypeInputMouseMove:
		var ev struct {
			X int `json:"x"`
			Y int `json:"y"`
		}
		if err := json.Unmarshal(msg.Payload, &ev); err != nil {
			log.Printf("input mousemove parse error: %v", err)
			return true
		}
		if err := h.inputCtrl.MoveMouse(ev.X, ev.Y); err != nil {
			log.Printf("input mousemove error: %v", err)
		}

	case MessageTypeInputMouseMoveAbs:
		var ev struct {
			X int `json:"x"`
			Y int `json:"y"`
		}
		if err := json.Unmarshal(msg.Payload, &ev); err != nil {
			log.Printf("input mousemove-abs parse error: %v", err)
			return true
		}
		if err := h.inputCtrl.SetCursorPos(ev.X, ev.Y); err != nil {
			log.Printf("input mousemove-abs error: %v", err)
		}

	case MessageTypeInputMouseBtn:
		var ev struct {
			Button  int  `json:"button"`
			Pressed bool `json:"pressed"`
			X       int  `json:"x"`
			Y       int  `json:"y"`
		}
		if err := json.Unmarshal(msg.Payload, &ev); err != nil {
			log.Printf("input mousebtn parse error: %v", err)
			return true
		}
		btn := input.MouseButton(ev.Button)
		if ev.Pressed {
			h.inputCtrl.PressMouse(btn, ev.X, ev.Y)
		} else {
			h.inputCtrl.ReleaseMouse(btn, ev.X, ev.Y)
		}

	case MessageTypeInputScroll:
		var ev struct {
			DX float64 `json:"dx"`
			DY float64 `json:"dy"`
		}
		if err := json.Unmarshal(msg.Payload, &ev); err != nil {
			log.Printf("input scroll parse error: %v", err)
			return true
		}
		if err := h.inputCtrl.Scroll(int(ev.DX), int(ev.DY)); err != nil {
			log.Printf("input scroll error: %v", err)
		}

	case MessageTypeInputKeyDown:
		var ev struct {
			KeyCode int `json:"keyCode"`
		}
		if err := json.Unmarshal(msg.Payload, &ev); err != nil {
			log.Printf("input keydown parse error: %v", err)
			return true
		}
		if err := h.inputCtrl.PressKey(ev.KeyCode); err != nil {
			log.Printf("input keydown error: %v", err)
		}

	case MessageTypeInputKeyUp:
		var ev struct {
			KeyCode int `json:"keyCode"`
		}
		if err := json.Unmarshal(msg.Payload, &ev); err != nil {
			log.Printf("input keyup parse error: %v", err)
			return true
		}
		if err := h.inputCtrl.ReleaseKey(ev.KeyCode); err != nil {
			log.Printf("input keyup error: %v", err)
		}

	case MessageTypeInputReleaseAll:
		if err := h.inputCtrl.ReleaseAllKeys(); err != nil {
			log.Printf("input release-all error: %v", err)
		}

	case MessageTypeInputTouch:
		var ev input.TouchEvent
		if err := json.Unmarshal(msg.Payload, &ev); err != nil {
			log.Printf("input touch parse error: %v", err)
			return true
		}
		if err := h.inputCtrl.InjectTouch(ev.Touches); err != nil {
			log.Printf("input touch error: %v", err)
		}

	default:
		return false
	}
	return true
}

func (h *Hub) cursorPoll() {
	ticker := time.NewTicker(time.Second / 30)
	defer ticker.Stop()

	var lastX, lastY int
	for range ticker.C {
		if h.inputCtrl == nil {
			continue
		}
		x, y, err := h.inputCtrl.GetCursorPos()
		if err != nil {
			continue
		}
		if x != lastX || y != lastY {
			lastX, lastY = x, y
			for room := range h.rooms {
				for client := range h.rooms[room] {
					client.sendJSON(Message{
						Type: MessageTypeCursorPos,
						Room: room,
						Payload: mustJSON(struct {
							X int `json:"x"`
							Y int `json:"y"`
						}{X: x, Y: y}),
					})
				}
			}
		}
	}
}

func (h *Hub) cursorImagePoll() {
	ticker := time.NewTicker(time.Second / 10)
	defer ticker.Stop()

	var lastData string
	for range ticker.C {
		if h.inputCtrl == nil {
			continue
		}
		info, err := h.inputCtrl.GetCursorInfo()
		if err != nil || info == nil {
			if err != nil {
				log.Printf("[cursor-poll] GetCursorInfo error: %v", err)
			} else {
				log.Printf("[cursor-poll] GetCursorInfo returned nil")
			}
			continue
		}
		// Only send when the cursor image actually changed
		if info.ImageData == lastData {
			continue
		}
		log.Printf("[cursor-poll] cursor changed: %dx%d hotspot=(%d,%d) dataLen=%d → broadcasting to clients",
			info.Width, info.Height, info.HotspotX, info.HotspotY, len(info.ImageData))
		lastData = info.ImageData

		for room := range h.rooms {
			for client := range h.rooms[room] {
				client.sendJSON(Message{
					Type: MessageTypeCursorImage,
					Room: room,
					Payload: mustJSON(struct {
						Data     string `json:"data"`
						Width    int    `json:"width"`
						Height   int    `json:"height"`
						HotspotX int    `json:"hotspotX"`
						HotspotY int    `json:"hotspotY"`
					}{Data: info.ImageData, Width: info.Width, Height: info.Height, HotspotX: info.HotspotX, HotspotY: info.HotspotY}),
				})
			}
		}
	}
}
func (h *Hub) keyStatePoll() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastKeys string
	for range ticker.C {
		if h.inputCtrl == nil {
			continue
		}
		keys, err := h.inputCtrl.GetKeyState()
		if err != nil {
			continue
		}
		// Only broadcast when key state changes
		keyJSON, _ := json.Marshal(keys)
		keyStr := string(keyJSON)
		if keyStr == lastKeys {
			continue
		}
		lastKeys = keyStr
		for room := range h.rooms {
			for client := range h.rooms[room] {
				client.sendJSON(Message{
					Type:    MessageTypeInputKeyState,
					Room:    room,
					Payload: keyJSON,
				})
			}
		}
	}
}

func (h *Hub) broadcast(room string, except *Client, msg Message) {
	for client := range h.rooms[room] {
		if client == except {
			continue
		}
		client.sendJSON(msg)
	}
}

func (h *Hub) sendTo(room string, targetID string, msg Message) {
	for client := range h.rooms[room] {
		if client.id == targetID {
			client.sendJSON(msg)
			return
		}
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		// Development-friendly default. Lock this down when exposing beyond LAN.
		return true
	},
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	room := strings.TrimSpace(r.URL.Query().Get("room"))
	if room == "" {
		room = "default"
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}

	client := &Client{
		id:   randomID(),
		room: room,
		hub:  h,
		conn: conn,
		send: make(chan Message, 32),
	}
	h.register <- client

	go client.writePump()
	go client.readPump()
}

func randomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b[:])
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
