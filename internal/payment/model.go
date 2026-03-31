package payment

import "time"

// Reservation tracks reserved funds during active bidding.
type Reservation struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	AuctionID string    `json:"auction_id"`
	Amount    float64   `json:"amount"`
	Status    string    `json:"status"` // "active" | "released" | "charged"
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// WalletBalanceResponse is the response from .NET WalletService API.
type WalletBalanceResponse struct {
	UserID  string  `json:"userId"`
	Balance float64 `json:"balance"`
	Success bool    `json:"success"`
	Message string  `json:"message,omitempty"`
}
