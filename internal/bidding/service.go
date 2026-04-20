package bidding

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/licit/licit-go/internal/messaging"
	"github.com/licit/licit-go/pkg/events"
	"github.com/nats-io/nats.go"
)

type Service struct {
	repo         *Repository
	nats         *messaging.Client
	bidProcessor bidProcessor
}

func NewService(repo *Repository, nats *messaging.Client, processors ...bidProcessor) *Service {
	var processor bidProcessor
	if len(processors) > 0 {
		processor = processors[0]
	}

	return &Service{repo: repo, nats: nats, bidProcessor: processor}
}

// PlaceBid validates and processes a bid.
// Flow: validate auction -> validate amount -> check balance (NATS req/reply to payment) -> accept/reject
func (s *Service) PlaceBid(ctx context.Context, userID string, req PlaceBidRequest) (*PlaceBidResponse, error) {
	req.IdempotencyKey = normalizeIdempotencyKey(req.IdempotencyKey)
	if !validateIdempotencyKey(req.IdempotencyKey) {
		return &PlaceBidResponse{Status: "rejected", Message: "idempotency key uuid formatinda olmali"}, nil
	}

	// 1. Get auction
	auction, err := s.repo.GetAuction(ctx, req.AuctionID)
	if err != nil {
		return nil, fmt.Errorf("auction not found: %w", err)
	}

	// 2. Check auction is active
	if auction.Status != "active" {
		return &PlaceBidResponse{Status: "rejected", Message: "ihale aktif degil"}, nil
	}

	// 3. Check auction hasn't ended
	if time.Now().After(auction.EndsAt) {
		return &PlaceBidResponse{Status: "rejected", Message: "ihale suresi dolmus"}, nil
	}

	// 4. Check bid amount is valid (must exceed current price + min increment)
	minRequired := auction.CurrentPrice + auction.MinIncrement
	if req.Amount < minRequired {
		return &PlaceBidResponse{
			Status:  "rejected",
			Message: fmt.Sprintf("teklif en az %.2f olmali (mevcut: %.2f + artis: %.2f)", minRequired, auction.CurrentPrice, auction.MinIncrement),
		}, nil
	}

	bidID := uuid.NewString()
	if s.bidProcessor != nil {
		result, err := s.bidProcessor.PrepareBid(ctx, prepareBidCommand{
			UserID:        userID,
			AuctionID:     req.AuctionID,
			RequestID:     req.IdempotencyKey,
			BidID:         bidID,
			Amount:        req.Amount,
			CurrentPrice:  auction.CurrentPrice,
			MinIncrement:  auction.MinIncrement,
			AuctionEndsAt: auction.EndsAt,
		})
		if err != nil {
			return nil, fmt.Errorf("prepare bid in redis: %w", err)
		}

		if result.Duplicate {
			if result.Status == "accepted" {
				return &PlaceBidResponse{
					BidID:   result.BidID,
					Status:  "accepted",
					Message: result.Message,
				}, nil
			}

			return &PlaceBidResponse{
				BidID:   result.BidID,
				Status:  "rejected",
				Message: "tekrar eden idempotency key",
			}, nil
		}

		if !result.Allowed {
			return &PlaceBidResponse{
				BidID:   result.BidID,
				Status:  "rejected",
				Message: result.Message,
			}, nil
		}
	}

	// 5. Validate balance via payment service (NATS request-reply)
	validateReq := events.PaymentValidateRequest{
		UserID:    userID,
		Amount:    req.Amount,
		AuctionID: req.AuctionID,
	}
	reply, err := s.nats.Request(messaging.SubjectPaymentValidate, validateReq, 5*time.Second)
	if err != nil {
		slog.Error("payment validation failed", "error", err)
		s.completeBidInRedis(ctx, userID, req, bidID, "rejected", "odeme dogrulama basarisiz, tekrar deneyin", auction.EndsAt)
		return &PlaceBidResponse{Status: "rejected", Message: "odeme dogrulama basarisiz, tekrar deneyin"}, nil
	}

	var validateResp events.PaymentValidateResponse
	if err := json.Unmarshal(reply.Data, &validateResp); err != nil {
		s.completeBidInRedis(ctx, userID, req, bidID, "rejected", "odeme dogrulama cevabi okunamadi", auction.EndsAt)
		return nil, fmt.Errorf("unmarshal payment response: %w", err)
	}

	if !validateResp.Valid {
		s.completeBidInRedis(ctx, userID, req, bidID, "rejected", validateResp.Reason, auction.EndsAt)
		return &PlaceBidResponse{Status: "rejected", Message: validateResp.Reason}, nil
	}

	// 6. Reserve funds
	reserveReq := events.PaymentReserveRequest{
		UserID:    userID,
		Amount:    req.Amount,
		AuctionID: req.AuctionID,
	}
	reserveReply, err := s.nats.Request(messaging.SubjectPaymentReserve, reserveReq, 5*time.Second)
	if err != nil {
		slog.Error("payment reserve failed", "error", err)
		s.completeBidInRedis(ctx, userID, req, bidID, "rejected", "bakiye rezerve edilemedi", auction.EndsAt)
		return &PlaceBidResponse{Status: "rejected", Message: "bakiye rezerve edilemedi"}, nil
	}

	var reserveResp events.PaymentReserveResponse
	if err := json.Unmarshal(reserveReply.Data, &reserveResp); err != nil {
		s.completeBidInRedis(ctx, userID, req, bidID, "rejected", "rezervasyon cevabi okunamadi", auction.EndsAt)
		return nil, fmt.Errorf("unmarshal reserve response: %w", err)
	}

	if !reserveResp.Reserved {
		s.completeBidInRedis(ctx, userID, req, bidID, "rejected", reserveResp.Reason, auction.EndsAt)
		return &PlaceBidResponse{Status: "rejected", Message: reserveResp.Reason}, nil
	}

	// 7. Create bid record and update auction price atomically
	bid := &Bid{
		ID:        bidID,
		AuctionID: req.AuctionID,
		UserID:    userID,
		Amount:    req.Amount,
		Status:    "accepted",
		CreatedAt: time.Now(),
	}

	if err := s.repo.CreateBidAndUpdatePrice(ctx, bid, req.Amount); err != nil {
		s.completeBidInRedis(ctx, userID, req, bidID, "rejected", "teklif kaydedilemedi", auction.EndsAt)
		s.releaseReservation(reserveResp.ReservationID)
		return nil, fmt.Errorf("create bid and update price: %w", err)
	}
	s.completeBidInRedis(ctx, userID, req, bidID, "accepted", "teklif kabul edildi", auction.EndsAt)

	// 9. Publish bid accepted event
	bidEvent := events.BidResultEvent{
		BidID:     bidID,
		AuctionID: req.AuctionID,
		UserID:    userID,
		Amount:    req.Amount,
		Status:    "accepted",
		Timestamp: time.Now(),
	}
	if err := s.nats.Publish(messaging.SubjectBidAccepted, bidEvent); err != nil {
		slog.Error("failed to publish bid accepted event", "error", err)
	}

	// 10. Publish auction update for streamer
	highest, _ := s.repo.GetHighestBid(ctx, req.AuctionID)
	bidCount := 0
	if bids, err := s.repo.GetBidsByAuction(ctx, req.AuctionID); err == nil {
		bidCount = len(bids)
	}
	currentPrice := req.Amount
	if highest != nil && highest.Amount > currentPrice {
		currentPrice = highest.Amount
	}
	updateEvent := events.AuctionUpdateEvent{
		AuctionID:    req.AuctionID,
		CurrentPrice: currentPrice,
		BidCount:     bidCount,
		LastBidderID: userID,
		TimeLeft:     int(time.Until(auction.EndsAt).Seconds()),
		Timestamp:    time.Now(),
	}
	if err := s.nats.Publish(messaging.SubjectAuctionUpdate, updateEvent); err != nil {
		slog.Error("failed to publish auction update", "error", err)
	}

	slog.Info("bid accepted", "bid_id", bidID, "auction_id", req.AuctionID, "user_id", userID, "amount", req.Amount)

	return &PlaceBidResponse{
		BidID:   bidID,
		Status:  "accepted",
		Message: "teklif kabul edildi",
	}, nil
}

func (s *Service) completeBidInRedis(ctx context.Context, userID string, req PlaceBidRequest, bidID, status, message string, auctionEndsAt time.Time) {
	if s.bidProcessor == nil {
		return
	}

	if err := s.bidProcessor.CompleteBid(ctx, completeBidCommand{
		UserID:          userID,
		RequestID:       req.IdempotencyKey,
		AuctionID:       req.AuctionID,
		BidID:           bidID,
		Status:          status,
		Message:         message,
		AuctionStateTTL: auctionStateTTL(auctionEndsAt),
	}); err != nil {
		slog.Error("complete bid in redis failed", "error", err, "bid_id", bidID, "status", status)
	}
}

func (s *Service) releaseReservation(reservationID string) {
	if reservationID == "" {
		return
	}

	if err := s.nats.Publish(messaging.SubjectPaymentRelease, map[string]string{
		"reservation_id": reservationID,
	}); err != nil {
		slog.Error("failed to release reservation", "error", err, "reservation_id", reservationID)
	}
}

func (s *Service) GetActiveAuctions(ctx context.Context) ([]Auction, error) {
	return s.repo.GetActiveAuctions(ctx)
}

func (s *Service) GetAuction(ctx context.Context, id string) (*Auction, error) {
	return s.repo.GetAuction(ctx, id)
}

func (s *Service) GetBidsByAuction(ctx context.Context, auctionID string) ([]Bid, error) {
	return s.repo.GetBidsByAuction(ctx, auctionID)
}

// ListenAuctionCreated listens for auction.created events from .NET TenderingService.
func (s *Service) ListenAuctionCreated() {
	s.nats.QueueSubscribe(messaging.SubjectAuctionCreated, "bidding-engine", func(msg *nats.Msg) { //nolint:errcheck
		var event events.AuctionCreatedEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			slog.Error("unmarshal auction created event", "error", err)
			return
		}

		auction := &Auction{
			ID:           event.AuctionID,
			TenderID:     event.TenderID,
			Title:        event.Title,
			Description:  event.Description,
			StartPrice:   event.StartPrice,
			CurrentPrice: event.StartPrice,
			MinIncrement: event.MinIncrement,
			Status:       "pending",
			StartsAt:     event.StartsAt,
			EndsAt:       event.EndsAt,
			CreatedBy:    event.CreatedBy,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.repo.CreateAuction(ctx, auction); err != nil {
			slog.Error("create auction from event", "error", err, "auction_id", event.AuctionID)
			return
		}

		slog.Info("auction created from .NET event", "auction_id", event.AuctionID, "title", event.Title)
	})
}

// StartAuctionScheduler periodically checks and activates/ends auctions based on time.
func (s *Service) StartAuctionScheduler(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				s.checkAuctionTimers(ctx)
			}
		}
	}()
}

func (s *Service) checkAuctionTimers(ctx context.Context) {
	// Activate pending auctions whose start time has passed
	now := time.Now()

	rows, err := s.repo.db.Query(ctx, `SELECT id, tender_id, title, start_price, min_increment, ends_at FROM auctions WHERE status = 'pending' AND starts_at <= $1`, now)
	if err != nil {
		slog.Error("query pending auctions failed", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, tenderID, title string
		var startPrice, minIncrement float64
		var endsAt time.Time
		if err := rows.Scan(&id, &tenderID, &title, &startPrice, &minIncrement, &endsAt); err != nil {
			continue
		}
		if err := s.repo.UpdateAuctionStatus(ctx, id, "active"); err != nil {
			slog.Error("update auction status to active", "error", err, "auction_id", id)
			continue
		}
		if err := s.nats.Publish(messaging.SubjectAuctionStarted, events.AuctionStartedEvent{
			AuctionID:    id,
			TenderID:     tenderID,
			Title:        title,
			StartPrice:   startPrice,
			MinIncrement: minIncrement,
			EndsAt:       endsAt,
			Timestamp:    now,
		}); err != nil {
			slog.Error("publish auction started event", "error", err, "auction_id", id)
		}
		slog.Info("auction activated", "auction_id", id)
	}

	// End active auctions whose end time has passed
	endRows, err := s.repo.db.Query(ctx, `SELECT id FROM auctions WHERE status = 'active' AND ends_at <= $1`, now)
	if err != nil {
		slog.Error("query ended auctions failed", "error", err)
		return
	}
	defer endRows.Close()

	for endRows.Next() {
		var id string
		if err := endRows.Scan(&id); err != nil {
			continue
		}
		if err := s.repo.UpdateAuctionStatus(ctx, id, "ended"); err != nil {
			slog.Error("update auction status to ended", "error", err, "auction_id", id)
			continue
		}

		highest, _ := s.repo.GetHighestBid(ctx, id)
		endEvent := events.AuctionEndedEvent{
			AuctionID: id,
			Timestamp: now,
		}
		if highest != nil {
			endEvent.WinnerUserID = highest.UserID
			endEvent.WinningBid = highest.Amount
		}
		if bids, err := s.repo.GetBidsByAuction(ctx, id); err == nil {
			endEvent.TotalBids = len(bids)
		}

		if err := s.nats.Publish(messaging.SubjectAuctionEnded, endEvent); err != nil {
			slog.Error("publish auction ended event", "error", err, "auction_id", id)
		}
		slog.Info("auction ended", "auction_id", id, "winner", endEvent.WinnerUserID)
	}
}
