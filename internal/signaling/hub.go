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
}

type inboundMessage struct {
	client  *Client
	message Message
}

func NewHub(handler ServerHandler) *Hub {
	return &Hub{
		register:   make(chan *Client, 16),
		unregister: make(chan *Client, 16),
		inbound:    make(chan inboundMessage, 128),
		rooms:      make(map[string]map[*Client]bool),
		handler:    handler,
	}
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
				inbound.client.sendJSON(Message{
					Type: MessageTypePong,
					Room: inbound.client.room,
					To:   inbound.client.id,
				})
				continue
			}
			if h.handler != nil && (msg.Type == MessageTypeOffer || msg.Type == MessageTypeCandidate) {
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
			if msg.To != "" {
				h.sendTo(inbound.client.room, msg.To, msg)
				continue
			}
			h.broadcast(inbound.client.room, inbound.client, msg)
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
