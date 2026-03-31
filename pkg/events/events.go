package events

import "time"

// BidPlacedEvent is published when a user places a bid.
type BidPlacedEvent struct {
	BidID     string    `json:"bid_id"`
	AuctionID string    `json:"auction_id"`
	UserID    string    `json:"user_id"`
	Amount    float64   `json:"amount"`
	Timestamp time.Time `json:"timestamp"`
}

// BidResultEvent is published when a bid is accepted or rejected.
type BidResultEvent struct {
	BidID     string    `json:"bid_id"`
	AuctionID string    `json:"auction_id"`
	UserID    string    `json:"user_id"`
	Amount    float64   `json:"amount"`
	Status    string    `json:"status"` // "accepted" | "rejected"
	Reason    string    `json:"reason,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// AuctionStartedEvent is published when an auction goes live.
type AuctionStartedEvent struct {
	AuctionID    string    `json:"auction_id"`
	TenderID     string    `json:"tender_id"`
	Title        string    `json:"title"`
	StartPrice   float64   `json:"start_price"`
	MinIncrement float64   `json:"min_increment"`
	EndsAt       time.Time `json:"ends_at"`
	Timestamp    time.Time `json:"timestamp"`
}

// AuctionEndedEvent is published when an auction concludes.
type AuctionEndedEvent struct {
	AuctionID    string    `json:"auction_id"`
	WinnerUserID string    `json:"winner_user_id,omitempty"`
	WinningBid   float64   `json:"winning_bid"`
	TotalBids    int       `json:"total_bids"`
	Timestamp    time.Time `json:"timestamp"`
}

// AuctionUpdateEvent is a real-time update pushed to streamer clients.
type AuctionUpdateEvent struct {
	AuctionID    string    `json:"auction_id"`
	CurrentPrice float64   `json:"current_price"`
	BidCount     int       `json:"bid_count"`
	LastBidderID string    `json:"last_bidder_id"`
	TimeLeft     int       `json:"time_left_seconds"`
	Timestamp    time.Time `json:"timestamp"`
}

// PaymentValidateRequest is sent to the payment service to check wallet balance.
type PaymentValidateRequest struct {
	UserID    string  `json:"user_id"`
	Amount    float64 `json:"amount"`
	AuctionID string  `json:"auction_id"`
}

// PaymentValidateResponse is the reply from payment validation.
type PaymentValidateResponse struct {
	Valid   bool    `json:"valid"`
	Balance float64 `json:"balance"`
	Reason  string  `json:"reason,omitempty"`
}

// PaymentReserveRequest holds funds in the user's wallet during active bidding.
type PaymentReserveRequest struct {
	UserID    string  `json:"user_id"`
	Amount    float64 `json:"amount"`
	AuctionID string  `json:"auction_id"`
}

// PaymentReserveResponse is the reply from fund reservation.
type PaymentReserveResponse struct {
	Reserved      bool   `json:"reserved"`
	ReservationID string `json:"reservation_id,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// AuctionCreatedEvent is received from .NET TenderingService when a new auction is created.
type AuctionCreatedEvent struct {
	AuctionID    string    `json:"auction_id"`
	TenderID     string    `json:"tender_id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	StartPrice   float64   `json:"start_price"`
	MinIncrement float64   `json:"min_increment"`
	StartsAt     time.Time `json:"starts_at"`
	EndsAt       time.Time `json:"ends_at"`
	CreatedBy    string    `json:"created_by"`
}
