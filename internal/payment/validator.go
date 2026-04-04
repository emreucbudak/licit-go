package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/licit/licit-go/internal/config"
	"github.com/licit/licit-go/internal/messaging"
	"github.com/licit/licit-go/pkg/events"
	"github.com/nats-io/nats.go"
)

type Validator struct {
	nats         *messaging.Client
	walletURL    string
	httpClient   *http.Client
	reservations map[string]*Reservation // reservationID -> Reservation
	userReserved map[string]float64      // userID:auctionID -> total reserved
	mu           sync.RWMutex
}

func NewValidator(nc *messaging.Client, cfg *config.DotNetConfig) *Validator {
	return &Validator{
		nats:         nc,
		walletURL:    cfg.WalletServiceURL,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		reservations: make(map[string]*Reservation),
		userReserved: make(map[string]float64),
	}
}

// Start begins listening for payment-related NATS messages.
func (v *Validator) Start() {
	// Handle balance validation requests (request-reply)
	v.nats.Subscribe(messaging.SubjectPaymentValidate, v.handleValidate) //nolint:errcheck

	// Handle fund reservation requests (request-reply)
	v.nats.Subscribe(messaging.SubjectPaymentReserve, v.handleReserve) //nolint:errcheck

	// Handle fund release requests
	v.nats.Subscribe(messaging.SubjectPaymentRelease, v.handleRelease) //nolint:errcheck

	// Handle charge requests (when auction is won)
	v.nats.Subscribe(messaging.SubjectPaymentCharge, v.handleCharge) //nolint:errcheck

	// Listen for auction ended events to release non-winner reservations
	v.nats.Subscribe(messaging.SubjectAuctionEnded, v.handleAuctionEnded) //nolint:errcheck

	slog.Info("Payment Validator started, listening on NATS subjects")
}

// handleValidate checks if a user has sufficient wallet balance.
func (v *Validator) handleValidate(msg *nats.Msg) {
	var req events.PaymentValidateRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		v.reply(msg, events.PaymentValidateResponse{Valid: false, Reason: "gecersiz istek"})
		return
	}

	// Get wallet balance from .NET WalletService
	balance, err := v.getWalletBalance(req.UserID)
	if err != nil {
		slog.Error("wallet balance check failed", "error", err, "user_id", req.UserID)
		v.reply(msg, events.PaymentValidateResponse{Valid: false, Reason: "bakiye sorgulanamadi"})
		return
	}

	// Check if balance minus already reserved funds is enough
	reservedKey := fmt.Sprintf("%s:%s", req.UserID, req.AuctionID)
	v.mu.RLock()
	alreadyReserved := v.userReserved[reservedKey]
	v.mu.RUnlock()

	available := balance - alreadyReserved
	if available < req.Amount {
		v.reply(msg, events.PaymentValidateResponse{
			Valid:   false,
			Balance: available,
			Reason:  fmt.Sprintf("yetersiz bakiye: mevcut=%.2f, gerekli=%.2f", available, req.Amount),
		})
		return
	}

	v.reply(msg, events.PaymentValidateResponse{
		Valid:   true,
		Balance: available,
	})

	slog.Info("balance validated", "user_id", req.UserID, "amount", req.Amount, "available", available)
}

// handleReserve reserves funds in the user's wallet for an active bid.
func (v *Validator) handleReserve(msg *nats.Msg) {
	var req events.PaymentReserveRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		v.reply(msg, events.PaymentReserveResponse{Reserved: false, Reason: "gecersiz istek"})
		return
	}

	reservedKey := fmt.Sprintf("%s:%s", req.UserID, req.AuctionID)

	v.mu.Lock()
	// Release any previous reservation for same user+auction (they're increasing their bid)
	for id, res := range v.reservations {
		if res.UserID == req.UserID && res.AuctionID == req.AuctionID && res.Status == "active" {
			res.Status = "released"
			v.userReserved[reservedKey] -= res.Amount
			slog.Info("released previous reservation", "reservation_id", id, "amount", res.Amount)
		}
	}

	reservationID := uuid.NewString()
	v.reservations[reservationID] = &Reservation{
		ID:        reservationID,
		UserID:    req.UserID,
		AuctionID: req.AuctionID,
		Amount:    req.Amount,
		Status:    "active",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	v.userReserved[reservedKey] += req.Amount
	v.mu.Unlock()

	v.reply(msg, events.PaymentReserveResponse{
		Reserved:      true,
		ReservationID: reservationID,
	})

	slog.Info("funds reserved", "reservation_id", reservationID, "user_id", req.UserID, "amount", req.Amount)
}

// handleRelease releases a fund reservation.
func (v *Validator) handleRelease(msg *nats.Msg) {
	var req struct {
		ReservationID string `json:"reservation_id"`
	}
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		slog.Error("unmarshal release request", "error", err, "subject", messaging.SubjectPaymentRelease)
		return
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	res, ok := v.reservations[req.ReservationID]
	if !ok || res.Status != "active" {
		return
	}

	res.Status = "released"
	res.UpdatedAt = time.Now()
	reservedKey := fmt.Sprintf("%s:%s", res.UserID, res.AuctionID)
	v.userReserved[reservedKey] -= res.Amount

	slog.Info("reservation released", "reservation_id", req.ReservationID)
}

// handleCharge finalizes the payment when an auction is won.
func (v *Validator) handleCharge(msg *nats.Msg) {
	var req struct {
		ReservationID string `json:"reservation_id"`
	}
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		slog.Error("unmarshal charge request", "error", err, "subject", messaging.SubjectPaymentCharge)
		return
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	res, ok := v.reservations[req.ReservationID]
	if !ok || res.Status != "active" {
		return
	}

	res.Status = "charged"
	res.UpdatedAt = time.Now()

	// TODO: Call .NET WalletService to actually deduct the balance
	slog.Info("reservation charged", "reservation_id", req.ReservationID, "amount", res.Amount)
}

// handleAuctionEnded releases all non-winner reservations when an auction ends.
func (v *Validator) handleAuctionEnded(msg *nats.Msg) {
	var event events.AuctionEndedEvent
	if err := json.Unmarshal(msg.Data, &event); err != nil {
		slog.Error("unmarshal auction ended event", "error", err, "subject", messaging.SubjectAuctionEnded)
		return
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	for _, res := range v.reservations {
		if res.AuctionID != event.AuctionID || res.Status != "active" {
			continue
		}
		if res.UserID == event.WinnerUserID {
			// Winner's reservation stays active for charging
			continue
		}
		res.Status = "released"
		res.UpdatedAt = time.Now()
		reservedKey := fmt.Sprintf("%s:%s", res.UserID, res.AuctionID)
		v.userReserved[reservedKey] -= res.Amount

		slog.Info("non-winner reservation released", "user_id", res.UserID, "auction_id", event.AuctionID)
	}
}

// getWalletBalance calls the .NET WalletService to get user's current balance.
func (v *Validator) getWalletBalance(userID string) (float64, error) {
	url := fmt.Sprintf("%s/api/wallet/%s/balance", v.walletURL, userID)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("wallet service request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("wallet service returned status %d", resp.StatusCode)
	}

	var walletResp WalletBalanceResponse
	if err := json.NewDecoder(resp.Body).Decode(&walletResp); err != nil {
		return 0, fmt.Errorf("decode wallet response: %w", err)
	}

	return walletResp.Balance, nil
}

func (v *Validator) reply(msg *nats.Msg, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		slog.Error("marshal reply", "error", err)
		return
	}
	if err := msg.Respond(payload); err != nil {
		slog.Error("NATS respond failed", "error", err)
	}
}
