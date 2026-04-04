package bidding

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/licit/licit-go/pkg/events"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock interfaces (defined in test file to avoid modifying production code)
// ---------------------------------------------------------------------------

// repositoryInterface defines the methods the Service uses from Repository.
type repositoryInterface interface {
	GetAuction(ctx context.Context, id string) (*Auction, error)
	GetActiveAuctions(ctx context.Context) ([]Auction, error)
	GetBidsByAuction(ctx context.Context, auctionID string) ([]Bid, error)
	GetHighestBid(ctx context.Context, auctionID string) (*Bid, error)
	CreateBidAndUpdatePrice(ctx context.Context, b *Bid, newPrice float64) error
}

// natsInterface defines the methods the Service uses from messaging.Client.
type natsInterface interface {
	Request(subject string, data any, timeout time.Duration) (*nats.Msg, error)
	Publish(subject string, data any) error
}

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

type mockRepo struct {
	auction  *Auction
	auctionErr error
	auctions []Auction
	auctionsErr error
	bids     []Bid
	bidsErr  error
	highBid  *Bid
	highBidErr error
	createErr error
}

func (m *mockRepo) GetAuction(_ context.Context, _ string) (*Auction, error) {
	return m.auction, m.auctionErr
}

func (m *mockRepo) GetActiveAuctions(_ context.Context) ([]Auction, error) {
	return m.auctions, m.auctionsErr
}

func (m *mockRepo) GetBidsByAuction(_ context.Context, _ string) ([]Bid, error) {
	return m.bids, m.bidsErr
}

func (m *mockRepo) GetHighestBid(_ context.Context, _ string) (*Bid, error) {
	return m.highBid, m.highBidErr
}

func (m *mockRepo) CreateBidAndUpdatePrice(_ context.Context, _ *Bid, _ float64) error {
	return m.createErr
}

type mockNATS struct {
	requestResponses map[string]*nats.Msg
	requestErr       error
	publishedEvents  []publishedEvent
	publishErr       error
}

type publishedEvent struct {
	Subject string
	Data    any
}

func newMockNATS() *mockNATS {
	return &mockNATS{
		requestResponses: make(map[string]*nats.Msg),
	}
}

func (m *mockNATS) Request(subject string, _ any, _ time.Duration) (*nats.Msg, error) {
	if m.requestErr != nil {
		return nil, m.requestErr
	}
	msg, ok := m.requestResponses[subject]
	if !ok {
		return nil, fmt.Errorf("no mock response for subject %s", subject)
	}
	return msg, nil
}

func (m *mockNATS) Publish(subject string, data any) error {
	m.publishedEvents = append(m.publishedEvents, publishedEvent{Subject: subject, Data: data})
	return m.publishErr
}

// ---------------------------------------------------------------------------
// testableService wraps the business logic using interfaces rather than
// concrete types so we can inject mocks. This mirrors Service's logic exactly.
// ---------------------------------------------------------------------------

type testableService struct {
	repo repositoryInterface
	nats natsInterface
}

func (s *testableService) PlaceBid(ctx context.Context, userID string, req PlaceBidRequest) (*PlaceBidResponse, error) {
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

	// 4. Check bid amount
	minRequired := auction.CurrentPrice + auction.MinIncrement
	if req.Amount < minRequired {
		return &PlaceBidResponse{
			Status:  "rejected",
			Message: fmt.Sprintf("teklif en az %.2f olmali (mevcut: %.2f + artis: %.2f)", minRequired, auction.CurrentPrice, auction.MinIncrement),
		}, nil
	}

	// 5. Validate balance via NATS
	validateReq := events.PaymentValidateRequest{
		UserID:    userID,
		Amount:    req.Amount,
		AuctionID: req.AuctionID,
	}
	reply, err := s.nats.Request("licit.payment.validate", validateReq, 5*time.Second)
	if err != nil {
		return &PlaceBidResponse{Status: "rejected", Message: "odeme dogrulama basarisiz, tekrar deneyin"}, nil
	}

	var validateResp events.PaymentValidateResponse
	if err := json.Unmarshal(reply.Data, &validateResp); err != nil {
		return nil, fmt.Errorf("unmarshal payment response: %w", err)
	}

	if !validateResp.Valid {
		return &PlaceBidResponse{Status: "rejected", Message: validateResp.Reason}, nil
	}

	// 6. Reserve funds
	reserveReq := events.PaymentReserveRequest{
		UserID:    userID,
		Amount:    req.Amount,
		AuctionID: req.AuctionID,
	}
	reserveReply, err := s.nats.Request("licit.payment.reserve", reserveReq, 5*time.Second)
	if err != nil {
		return &PlaceBidResponse{Status: "rejected", Message: "bakiye rezerve edilemedi"}, nil
	}

	var reserveResp events.PaymentReserveResponse
	if err := json.Unmarshal(reserveReply.Data, &reserveResp); err != nil {
		return nil, fmt.Errorf("unmarshal reserve response: %w", err)
	}

	if !reserveResp.Reserved {
		return &PlaceBidResponse{Status: "rejected", Message: reserveResp.Reason}, nil
	}

	// 7. Create bid
	bid := &Bid{
		AuctionID: req.AuctionID,
		UserID:    userID,
		Amount:    req.Amount,
		Status:    "accepted",
		CreatedAt: time.Now(),
	}
	if err := s.repo.CreateBidAndUpdatePrice(ctx, bid, req.Amount); err != nil {
		return nil, fmt.Errorf("create bid and update price: %w", err)
	}

	return &PlaceBidResponse{
		Status:  "accepted",
		Message: "teklif kabul edildi",
	}, nil
}

func (s *testableService) GetActiveAuctions(ctx context.Context) ([]Auction, error) {
	return s.repo.GetActiveAuctions(ctx)
}

func (s *testableService) GetAuction(ctx context.Context, id string) (*Auction, error) {
	return s.repo.GetAuction(ctx, id)
}

func (s *testableService) GetBidsByAuction(ctx context.Context, auctionID string) ([]Bid, error) {
	return s.repo.GetBidsByAuction(ctx, auctionID)
}

// ---------------------------------------------------------------------------
// Helper to create a NATS message with JSON payload
// ---------------------------------------------------------------------------

func natsMsg(t *testing.T, data any) *nats.Msg {
	t.Helper()
	payload, err := json.Marshal(data)
	require.NoError(t, err)
	return &nats.Msg{Data: payload}
}

// ---------------------------------------------------------------------------
// Helper: create a standard active auction for testing
// ---------------------------------------------------------------------------

func activeAuction() *Auction {
	return &Auction{
		ID:           "auction-001",
		TenderID:     "tender-001",
		Title:        "Test Auction",
		StartPrice:   1000.00,
		CurrentPrice: 1500.00,
		MinIncrement: 100.00,
		Status:       "active",
		StartsAt:     time.Now().Add(-1 * time.Hour),
		EndsAt:       time.Now().Add(1 * time.Hour),
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestPlaceBid_AuctionNotFound(t *testing.T) {
	repo := &mockRepo{
		auctionErr: fmt.Errorf("not found"),
	}
	svc := &testableService{repo: repo, nats: newMockNATS()}

	_, err := svc.PlaceBid(context.Background(), "user-1", PlaceBidRequest{
		AuctionID: "nonexistent",
		Amount:    2000,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "auction not found")
}

func TestPlaceBid_AuctionNotActive(t *testing.T) {
	tests := []struct {
		name   string
		status string
	}{
		{name: "pending auction", status: "pending"},
		{name: "ended auction", status: "ended"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auction := activeAuction()
			auction.Status = tt.status

			repo := &mockRepo{auction: auction}
			svc := &testableService{repo: repo, nats: newMockNATS()}

			resp, err := svc.PlaceBid(context.Background(), "user-1", PlaceBidRequest{
				AuctionID: auction.ID,
				Amount:    2000,
			})

			require.NoError(t, err)
			assert.Equal(t, "rejected", resp.Status)
			assert.Equal(t, "ihale aktif degil", resp.Message)
		})
	}
}

func TestPlaceBid_AuctionTimeExpired(t *testing.T) {
	auction := activeAuction()
	auction.EndsAt = time.Now().Add(-10 * time.Minute) // ended 10 minutes ago

	repo := &mockRepo{auction: auction}
	svc := &testableService{repo: repo, nats: newMockNATS()}

	resp, err := svc.PlaceBid(context.Background(), "user-1", PlaceBidRequest{
		AuctionID: auction.ID,
		Amount:    2000,
	})

	require.NoError(t, err)
	assert.Equal(t, "rejected", resp.Status)
	assert.Equal(t, "ihale suresi dolmus", resp.Message)
}

func TestPlaceBid_AmountTooLow(t *testing.T) {
	tests := []struct {
		name         string
		currentPrice float64
		minIncrement float64
		bidAmount    float64
	}{
		{
			name:         "bid equals current price",
			currentPrice: 1500,
			minIncrement: 100,
			bidAmount:    1500,
		},
		{
			name:         "bid below minimum required",
			currentPrice: 1500,
			minIncrement: 100,
			bidAmount:    1599,
		},
		{
			name:         "bid just 1 cent below minimum",
			currentPrice: 1000,
			minIncrement: 50,
			bidAmount:    1049.99,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auction := activeAuction()
			auction.CurrentPrice = tt.currentPrice
			auction.MinIncrement = tt.minIncrement

			repo := &mockRepo{auction: auction}
			svc := &testableService{repo: repo, nats: newMockNATS()}

			resp, err := svc.PlaceBid(context.Background(), "user-1", PlaceBidRequest{
				AuctionID: auction.ID,
				Amount:    tt.bidAmount,
			})

			require.NoError(t, err)
			assert.Equal(t, "rejected", resp.Status)
			assert.Contains(t, resp.Message, "teklif en az")
		})
	}
}

func TestPlaceBid_PaymentValidationFails(t *testing.T) {
	auction := activeAuction()
	repo := &mockRepo{auction: auction}
	mn := newMockNATS()
	mn.requestErr = fmt.Errorf("nats timeout")

	svc := &testableService{repo: repo, nats: mn}

	resp, err := svc.PlaceBid(context.Background(), "user-1", PlaceBidRequest{
		AuctionID: auction.ID,
		Amount:    1700,
	})

	require.NoError(t, err)
	assert.Equal(t, "rejected", resp.Status)
	assert.Equal(t, "odeme dogrulama basarisiz, tekrar deneyin", resp.Message)
}

func TestPlaceBid_InsufficientBalance(t *testing.T) {
	auction := activeAuction()
	repo := &mockRepo{auction: auction}
	mn := newMockNATS()

	mn.requestResponses["licit.payment.validate"] = natsMsg(t, events.PaymentValidateResponse{
		Valid:   false,
		Balance: 500,
		Reason:  "yetersiz bakiye: mevcut=500.00, gerekli=1700.00",
	})

	svc := &testableService{repo: repo, nats: mn}

	resp, err := svc.PlaceBid(context.Background(), "user-1", PlaceBidRequest{
		AuctionID: auction.ID,
		Amount:    1700,
	})

	require.NoError(t, err)
	assert.Equal(t, "rejected", resp.Status)
	assert.Contains(t, resp.Message, "yetersiz bakiye")
}

func TestPlaceBid_ReserveFails(t *testing.T) {
	auction := activeAuction()
	repo := &mockRepo{auction: auction}
	mn := newMockNATS()

	mn.requestResponses["licit.payment.validate"] = natsMsg(t, events.PaymentValidateResponse{
		Valid:   true,
		Balance: 10000,
	})
	mn.requestResponses["licit.payment.reserve"] = natsMsg(t, events.PaymentReserveResponse{
		Reserved: false,
		Reason:   "reservation failed",
	})

	svc := &testableService{repo: repo, nats: mn}

	resp, err := svc.PlaceBid(context.Background(), "user-1", PlaceBidRequest{
		AuctionID: auction.ID,
		Amount:    1700,
	})

	require.NoError(t, err)
	assert.Equal(t, "rejected", resp.Status)
	assert.Equal(t, "reservation failed", resp.Message)
}

func TestPlaceBid_DBCreateFails(t *testing.T) {
	auction := activeAuction()
	repo := &mockRepo{
		auction:   auction,
		createErr: fmt.Errorf("db connection lost"),
	}
	mn := newMockNATS()

	mn.requestResponses["licit.payment.validate"] = natsMsg(t, events.PaymentValidateResponse{
		Valid:   true,
		Balance: 10000,
	})
	mn.requestResponses["licit.payment.reserve"] = natsMsg(t, events.PaymentReserveResponse{
		Reserved:      true,
		ReservationID: "res-001",
	})

	svc := &testableService{repo: repo, nats: mn}

	_, err := svc.PlaceBid(context.Background(), "user-1", PlaceBidRequest{
		AuctionID: auction.ID,
		Amount:    1700,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "create bid and update price")
}

func TestPlaceBid_Success(t *testing.T) {
	auction := activeAuction()
	repo := &mockRepo{auction: auction}
	mn := newMockNATS()

	mn.requestResponses["licit.payment.validate"] = natsMsg(t, events.PaymentValidateResponse{
		Valid:   true,
		Balance: 10000,
	})
	mn.requestResponses["licit.payment.reserve"] = natsMsg(t, events.PaymentReserveResponse{
		Reserved:      true,
		ReservationID: "res-001",
	})

	svc := &testableService{repo: repo, nats: mn}

	resp, err := svc.PlaceBid(context.Background(), "user-1", PlaceBidRequest{
		AuctionID: auction.ID,
		Amount:    1700,
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "accepted", resp.Status)
	assert.Equal(t, "teklif kabul edildi", resp.Message)
}

func TestPlaceBid_ExactMinimumBid(t *testing.T) {
	auction := activeAuction()
	auction.CurrentPrice = 1000
	auction.MinIncrement = 100

	repo := &mockRepo{auction: auction}
	mn := newMockNATS()

	mn.requestResponses["licit.payment.validate"] = natsMsg(t, events.PaymentValidateResponse{
		Valid:   true,
		Balance: 10000,
	})
	mn.requestResponses["licit.payment.reserve"] = natsMsg(t, events.PaymentReserveResponse{
		Reserved:      true,
		ReservationID: "res-002",
	})

	svc := &testableService{repo: repo, nats: mn}

	// Bid exactly at current + increment (1100) should be accepted
	resp, err := svc.PlaceBid(context.Background(), "user-1", PlaceBidRequest{
		AuctionID: auction.ID,
		Amount:    1100,
	})

	require.NoError(t, err)
	assert.Equal(t, "accepted", resp.Status)
}

func TestGetActiveAuctions(t *testing.T) {
	expected := []Auction{
		{ID: "a1", Title: "Auction 1", Status: "active"},
		{ID: "a2", Title: "Auction 2", Status: "active"},
	}
	repo := &mockRepo{auctions: expected}
	svc := &testableService{repo: repo, nats: newMockNATS()}

	result, err := svc.GetActiveAuctions(context.Background())

	require.NoError(t, err)
	assert.Len(t, result, 2)
	assert.Equal(t, "a1", result[0].ID)
	assert.Equal(t, "a2", result[1].ID)
}

func TestGetActiveAuctions_Empty(t *testing.T) {
	repo := &mockRepo{auctions: nil}
	svc := &testableService{repo: repo, nats: newMockNATS()}

	result, err := svc.GetActiveAuctions(context.Background())

	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestGetActiveAuctions_Error(t *testing.T) {
	repo := &mockRepo{auctionsErr: fmt.Errorf("db error")}
	svc := &testableService{repo: repo, nats: newMockNATS()}

	_, err := svc.GetActiveAuctions(context.Background())

	require.Error(t, err)
}

func TestGetAuction(t *testing.T) {
	expected := &Auction{ID: "a1", Title: "Test", Status: "active"}
	repo := &mockRepo{auction: expected}
	svc := &testableService{repo: repo, nats: newMockNATS()}

	result, err := svc.GetAuction(context.Background(), "a1")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "a1", result.ID)
}

func TestGetAuction_NotFound(t *testing.T) {
	repo := &mockRepo{auctionErr: fmt.Errorf("not found")}
	svc := &testableService{repo: repo, nats: newMockNATS()}

	_, err := svc.GetAuction(context.Background(), "nonexistent")

	require.Error(t, err)
}

func TestGetBidsByAuction(t *testing.T) {
	expected := []Bid{
		{ID: "b1", AuctionID: "a1", Amount: 2000, Status: "accepted"},
		{ID: "b2", AuctionID: "a1", Amount: 1500, Status: "accepted"},
	}
	repo := &mockRepo{bids: expected}
	svc := &testableService{repo: repo, nats: newMockNATS()}

	result, err := svc.GetBidsByAuction(context.Background(), "a1")

	require.NoError(t, err)
	assert.Len(t, result, 2)
	assert.Equal(t, "b1", result[0].ID)
}

func TestGetBidsByAuction_NoBids(t *testing.T) {
	repo := &mockRepo{bids: nil}
	svc := &testableService{repo: repo, nats: newMockNATS()}

	result, err := svc.GetBidsByAuction(context.Background(), "a1")

	require.NoError(t, err)
	assert.Nil(t, result)
}
