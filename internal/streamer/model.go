package streamer

import "time"

// WSMessage is the envelope for all WebSocket messages.
type WSMessage struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

// Message types sent to clients
const (
	MsgTypeBidUpdate     = "bid_update"
	MsgTypeAuctionStart  = "auction_start"
	MsgTypeAuctionEnd    = "auction_end"
	MsgTypeAuctionUpdate = "auction_update"
	MsgTypeError         = "error"
	MsgTypePong          = "pong"
)

// Message types received from clients
const (
	MsgTypeJoinAuction  = "join_auction"
	MsgTypeLeaveAuction = "leave_auction"
	MsgTypePing         = "ping"
)

// ClientMessage is what clients send over WebSocket.
type ClientMessage struct {
	Type      string `json:"type"`
	AuctionID string `json:"auction_id,omitempty"`
}

// AuctionRoom tracks clients watching a specific auction.
type AuctionRoom struct {
	AuctionID string
	Clients   map[string]*Client
	CreatedAt time.Time
}
