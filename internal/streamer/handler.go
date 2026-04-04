package streamer

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/licit/licit-go/internal/messaging"
	"github.com/licit/licit-go/pkg/events"
	"github.com/nats-io/nats.go"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Development mode — restrict in production
	},
}

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
	maxMsgSize = 512
)

type Handler struct {
	hub  *Hub
	nats *messaging.Client
}

func NewHandler(hub *Hub, nc *messaging.Client) *Handler {
	h := &Handler{hub: hub, nats: nc}
	h.subscribeNATSEvents()
	return h
}

// ServeWS handles WebSocket upgrade requests.
func (h *Handler) ServeWS(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = r.Header.Get("X-User-ID")
	}
	if userID == "" {
		http.Error(w, "user_id required", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}

	client := &Client{
		ID:     uuid.NewString(),
		UserID: userID,
		Conn:   conn,
		Send:   make(chan []byte, 256),
		Hub:    h.hub,
		Rooms:  make(map[string]bool),
	}

	h.hub.register <- client

	go h.writePump(client)
	go h.readPump(client)
}

func (h *Handler) readPump(client *Client) {
	defer func() {
		h.hub.unregister <- client
		client.Conn.Close()
	}()

	client.Conn.SetReadLimit(maxMsgSize)
	_ = client.Conn.SetReadDeadline(time.Now().Add(pongWait))
	client.Conn.SetPongHandler(func(string) error {
		return client.Conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, message, err := client.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Error("websocket read error", "error", err, "client_id", client.ID)
			}
			break
		}

		var msg ClientMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			h.sendError(client, "gecersiz mesaj formati")
			continue
		}

		switch msg.Type {
		case MsgTypeJoinAuction:
			if msg.AuctionID == "" {
				h.sendError(client, "auction_id gerekli")
				continue
			}
			h.hub.JoinRoom(client, msg.AuctionID)

		case MsgTypeLeaveAuction:
			if msg.AuctionID != "" {
				h.hub.LeaveRoom(client, msg.AuctionID)
			}

		case MsgTypePing:
			h.sendMessage(client, WSMessage{Type: MsgTypePong, Payload: nil})
		}
	}
}

func (h *Handler) writePump(client *Client) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		client.Conn.Close()
	}()

	for {
		select {
		case message, ok := <-client.Send:
			_ = client.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = client.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			_ = client.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := client.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (h *Handler) sendMessage(client *Client, msg WSMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case client.Send <- data:
	default:
	}
}

func (h *Handler) sendError(client *Client, message string) {
	h.sendMessage(client, WSMessage{Type: MsgTypeError, Payload: map[string]string{"message": message}})
}

// subscribeNATSEvents listens for NATS events and broadcasts to WebSocket rooms.
func (h *Handler) subscribeNATSEvents() {
	// Bid accepted → broadcast to auction room
	h.nats.Subscribe(messaging.SubjectBidAccepted, func(msg *nats.Msg) { //nolint:errcheck
		var event events.BidResultEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			slog.Error("unmarshal bid accepted event", "error", err, "subject", messaging.SubjectBidAccepted)
			return
		}
		wsMsg := WSMessage{Type: MsgTypeBidUpdate, Payload: event}
		data, _ := json.Marshal(wsMsg)
		h.hub.BroadcastToAuction(event.AuctionID, data)
	})

	// Auction update → broadcast current state
	h.nats.Subscribe(messaging.SubjectAuctionUpdate, func(msg *nats.Msg) { //nolint:errcheck
		var event events.AuctionUpdateEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			slog.Error("unmarshal auction update event", "error", err, "subject", messaging.SubjectAuctionUpdate)
			return
		}
		wsMsg := WSMessage{Type: MsgTypeAuctionUpdate, Payload: event}
		data, _ := json.Marshal(wsMsg)
		h.hub.BroadcastToAuction(event.AuctionID, data)
	})

	// Auction started → broadcast to all in room
	h.nats.Subscribe(messaging.SubjectAuctionStarted, func(msg *nats.Msg) { //nolint:errcheck
		var event events.AuctionStartedEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			slog.Error("unmarshal auction started event", "error", err, "subject", messaging.SubjectAuctionStarted)
			return
		}
		wsMsg := WSMessage{Type: MsgTypeAuctionStart, Payload: event}
		data, _ := json.Marshal(wsMsg)
		h.hub.BroadcastToAuction(event.AuctionID, data)
	})

	// Auction ended → broadcast winner info
	h.nats.Subscribe(messaging.SubjectAuctionEnded, func(msg *nats.Msg) { //nolint:errcheck
		var event events.AuctionEndedEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			slog.Error("unmarshal auction ended event", "error", err, "subject", messaging.SubjectAuctionEnded)
			return
		}
		wsMsg := WSMessage{Type: MsgTypeAuctionEnd, Payload: event}
		data, _ := json.Marshal(wsMsg)
		h.hub.BroadcastToAuction(event.AuctionID, data)
	})

	slog.Info("Streamer subscribed to NATS events")
}
