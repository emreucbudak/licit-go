package bidding

import "time"

type Auction struct {
	ID           string    `json:"id"`
	TenderID     string    `json:"tender_id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	StartPrice   float64   `json:"start_price"`
	CurrentPrice float64   `json:"current_price"`
	MinIncrement float64   `json:"min_increment"`
	Status       string    `json:"status"` // "pending" | "active" | "ended"
	StartsAt     time.Time `json:"starts_at"`
	EndsAt       time.Time `json:"ends_at"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Bid struct {
	ID        string    `json:"id"`
	AuctionID string    `json:"auction_id"`
	UserID    string    `json:"user_id"`
	Amount    float64   `json:"amount"`
	Status    string    `json:"status"` // "pending" | "accepted" | "rejected"
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type PlaceBidRequest struct {
	AuctionID      string  `json:"auction_id"`
	Amount         float64 `json:"amount"`
	IdempotencyKey string  `json:"idempotency_key,omitempty"`
}

type PlaceBidResponse struct {
	BidID   string `json:"bid_id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type AuctionListResponse struct {
	Auctions []Auction `json:"auctions"`
	Total    int       `json:"total"`
}
