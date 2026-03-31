package streamer

import (
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client represents a single WebSocket connection.
type Client struct {
	ID       string
	UserID   string
	Conn     *websocket.Conn
	Send     chan []byte
	Hub      *Hub
	Rooms    map[string]bool // auction IDs this client is watching
	mu       sync.Mutex
}

// Hub manages all WebSocket connections and auction rooms.
type Hub struct {
	clients    map[string]*Client
	rooms      map[string]*AuctionRoom
	register   chan *Client
	unregister chan *Client
	broadcast  chan *RoomMessage
	mu         sync.RWMutex
}

// RoomMessage is a message targeted at a specific auction room.
type RoomMessage struct {
	AuctionID string
	Data      []byte
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[string]*Client),
		rooms:      make(map[string]*AuctionRoom),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan *RoomMessage, 256),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client.ID] = client
			h.mu.Unlock()
			slog.Info("client connected", "client_id", client.ID, "user_id", client.UserID)

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client.ID]; ok {
				// Remove from all rooms
				for auctionID := range client.Rooms {
					if room, ok := h.rooms[auctionID]; ok {
						delete(room.Clients, client.ID)
						if len(room.Clients) == 0 {
							delete(h.rooms, auctionID)
						}
					}
				}
				close(client.Send)
				delete(h.clients, client.ID)
				slog.Info("client disconnected", "client_id", client.ID)
			}
			h.mu.Unlock()

		case msg := <-h.broadcast:
			h.mu.RLock()
			if room, ok := h.rooms[msg.AuctionID]; ok {
				for _, client := range room.Clients {
					select {
					case client.Send <- msg.Data:
					default:
						// Client buffer full, disconnect
						go func(c *Client) {
							h.unregister <- c
						}(client)
					}
				}
			}
			h.mu.RUnlock()
		}
	}
}

// JoinRoom adds a client to an auction room.
func (h *Hub) JoinRoom(client *Client, auctionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	room, ok := h.rooms[auctionID]
	if !ok {
		room = &AuctionRoom{
			AuctionID: auctionID,
			Clients:   make(map[string]*Client),
			CreatedAt: time.Now(),
		}
		h.rooms[auctionID] = room
	}

	room.Clients[client.ID] = client
	client.mu.Lock()
	client.Rooms[auctionID] = true
	client.mu.Unlock()

	slog.Info("client joined auction room", "client_id", client.ID, "auction_id", auctionID)
}

// LeaveRoom removes a client from an auction room.
func (h *Hub) LeaveRoom(client *Client, auctionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if room, ok := h.rooms[auctionID]; ok {
		delete(room.Clients, client.ID)
		if len(room.Clients) == 0 {
			delete(h.rooms, auctionID)
		}
	}

	client.mu.Lock()
	delete(client.Rooms, auctionID)
	client.mu.Unlock()
}

// BroadcastToAuction sends a message to all clients watching a specific auction.
func (h *Hub) BroadcastToAuction(auctionID string, data []byte) {
	h.broadcast <- &RoomMessage{AuctionID: auctionID, Data: data}
}

// GetRoomClientCount returns the number of clients in an auction room.
func (h *Hub) GetRoomClientCount(auctionID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if room, ok := h.rooms[auctionID]; ok {
		return len(room.Clients)
	}
	return 0
}
