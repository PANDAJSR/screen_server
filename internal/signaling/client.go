package signaling

import (
	"log"
	"time"

	"github.com/gorilla/websocket"
)

type Client struct {
	id   string
	room string
	hub  *Hub
	conn *websocket.Conn
	send chan Message
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		var msg Message
		if err := c.conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("signaling read failed room=%s id=%s err=%v", c.room, c.id, err)
			}
			return
		}
		if msg.Type == "" {
			c.sendJSON(Message{Type: MessageTypeError, Payload: mustJSON(map[string]string{"message": "missing message type"})})
			continue
		}
		c.hub.inbound <- inboundMessage{client: c, message: msg}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.conn.WriteJSON(msg); err != nil {
				return
			}

		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Client) sendJSON(msg Message) {
	select {
	case c.send <- msg:
	default:
		// Backpressure protection: a stalled signaling client should not block
		// the hub goroutine. Dropping signaling messages is preferable to leaking
		// goroutines; WebRTC state will reconnect/re-negotiate in later steps.
		log.Printf("signaling send queue full room=%s id=%s", c.room, c.id)
		select {
		case c.hub.unregister <- c:
		default:
			_ = c.conn.Close()
		}
	}
}
