package events

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBidPlacedEvent_JSON(t *testing.T) {
	ts := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
	event := BidPlacedEvent{
		BidID:     "bid-001",
		AuctionID: "auction-001",
		UserID:    "user-001",
		Amount:    1500.50,
		Timestamp: ts,
	}

	data, err := json.Marshal(event)
	require.NoError(t, err)

	var decoded BidPlacedEvent
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, event.BidID, decoded.BidID)
	assert.Equal(t, event.AuctionID, decoded.AuctionID)
	assert.Equal(t, event.UserID, decoded.UserID)
	assert.InDelta(t, event.Amount, decoded.Amount, 0.001)
	assert.True(t, event.Timestamp.Equal(decoded.Timestamp))
}

func TestBidResultEvent_JSON(t *testing.T) {
	tests := []struct {
		name   string
		event  BidResultEvent
		reason string
	}{
		{
			name: "accepted bid",
			event: BidResultEvent{
				BidID:     "bid-100",
				AuctionID: "auction-200",
				UserID:    "user-300",
				Amount:    2000.00,
				Status:    "accepted",
				Timestamp: time.Now().UTC(),
			},
			reason: "",
		},
		{
			name: "rejected bid with reason",
			event: BidResultEvent{
				BidID:     "bid-101",
				AuctionID: "auction-200",
				UserID:    "user-301",
				Amount:    500.00,
				Status:    "rejected",
				Reason:    "insufficient balance",
				Timestamp: time.Now().UTC(),
			},
			reason: "insufficient balance",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.event)
			require.NoError(t, err)

			var decoded BidResultEvent
			err = json.Unmarshal(data, &decoded)
			require.NoError(t, err)

			assert.Equal(t, tt.event.BidID, decoded.BidID)
			assert.Equal(t, tt.event.Status, decoded.Status)
			assert.Equal(t, tt.reason, decoded.Reason)

			// Verify omitempty: reason field should be absent in JSON when empty
			if tt.reason == "" {
				var raw map[string]interface{}
				err = json.Unmarshal(data, &raw)
				require.NoError(t, err)
				_, hasReason := raw["reason"]
				assert.False(t, hasReason, "reason should be omitted from JSON when empty")
			}
		})
	}
}

func TestAuctionStartedEvent_JSON(t *testing.T) {
	ts := time.Date(2025, 7, 1, 14, 0, 0, 0, time.UTC)
	endsAt := ts.Add(2 * time.Hour)

	event := AuctionStartedEvent{
		AuctionID:    "auction-500",
		TenderID:     "tender-100",
		Title:        "Test Auction",
		StartPrice:   1000.00,
		MinIncrement: 50.00,
		EndsAt:       endsAt,
		Timestamp:    ts,
	}

	data, err := json.Marshal(event)
	require.NoError(t, err)

	var decoded AuctionStartedEvent
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, event.AuctionID, decoded.AuctionID)
	assert.Equal(t, event.TenderID, decoded.TenderID)
	assert.Equal(t, event.Title, decoded.Title)
	assert.InDelta(t, event.StartPrice, decoded.StartPrice, 0.001)
	assert.InDelta(t, event.MinIncrement, decoded.MinIncrement, 0.001)
	assert.True(t, event.EndsAt.Equal(decoded.EndsAt))
}

func TestAuctionEndedEvent_JSON(t *testing.T) {
	tests := []struct {
		name  string
		event AuctionEndedEvent
	}{
		{
			name: "auction with winner",
			event: AuctionEndedEvent{
				AuctionID:    "auction-600",
				WinnerUserID: "user-winner",
				WinningBid:   5000.00,
				TotalBids:    12,
				Timestamp:    time.Now().UTC(),
			},
		},
		{
			name: "auction with no bids",
			event: AuctionEndedEvent{
				AuctionID:    "auction-601",
				WinnerUserID: "",
				WinningBid:   0,
				TotalBids:    0,
				Timestamp:    time.Now().UTC(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.event)
			require.NoError(t, err)

			var decoded AuctionEndedEvent
			err = json.Unmarshal(data, &decoded)
			require.NoError(t, err)

			assert.Equal(t, tt.event.AuctionID, decoded.AuctionID)
			assert.Equal(t, tt.event.WinnerUserID, decoded.WinnerUserID)
			assert.InDelta(t, tt.event.WinningBid, decoded.WinningBid, 0.001)
			assert.Equal(t, tt.event.TotalBids, decoded.TotalBids)

			// Verify omitempty for winner_user_id
			if tt.event.WinnerUserID == "" {
				var raw map[string]interface{}
				err = json.Unmarshal(data, &raw)
				require.NoError(t, err)
				_, hasWinner := raw["winner_user_id"]
				assert.False(t, hasWinner, "winner_user_id should be omitted when empty")
			}
		})
	}
}

func TestAuctionUpdateEvent_JSON(t *testing.T) {
	event := AuctionUpdateEvent{
		AuctionID:    "auction-700",
		CurrentPrice: 3500.00,
		BidCount:     8,
		LastBidderID: "user-last",
		TimeLeft:     120,
		Timestamp:    time.Now().UTC(),
	}

	data, err := json.Marshal(event)
	require.NoError(t, err)

	var decoded AuctionUpdateEvent
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, event.AuctionID, decoded.AuctionID)
	assert.InDelta(t, event.CurrentPrice, decoded.CurrentPrice, 0.001)
	assert.Equal(t, event.BidCount, decoded.BidCount)
	assert.Equal(t, event.LastBidderID, decoded.LastBidderID)
	assert.Equal(t, event.TimeLeft, decoded.TimeLeft)
}

func TestPaymentValidateRequest_JSON(t *testing.T) {
	req := PaymentValidateRequest{
		UserID:    "user-pay-1",
		Amount:    2500.75,
		AuctionID: "auction-pay-1",
	}

	data, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded PaymentValidateRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, req.UserID, decoded.UserID)
	assert.InDelta(t, req.Amount, decoded.Amount, 0.001)
	assert.Equal(t, req.AuctionID, decoded.AuctionID)
}

func TestPaymentValidateResponse_JSON(t *testing.T) {
	tests := []struct {
		name string
		resp PaymentValidateResponse
	}{
		{
			name: "valid response",
			resp: PaymentValidateResponse{
				Valid:   true,
				Balance: 10000.00,
			},
		},
		{
			name: "invalid with reason",
			resp: PaymentValidateResponse{
				Valid:   false,
				Balance: 500.00,
				Reason:  "yetersiz bakiye",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.resp)
			require.NoError(t, err)

			var decoded PaymentValidateResponse
			err = json.Unmarshal(data, &decoded)
			require.NoError(t, err)

			assert.Equal(t, tt.resp.Valid, decoded.Valid)
			assert.InDelta(t, tt.resp.Balance, decoded.Balance, 0.001)
			assert.Equal(t, tt.resp.Reason, decoded.Reason)
		})
	}
}

func TestPaymentReserveRequest_JSON(t *testing.T) {
	req := PaymentReserveRequest{
		UserID:    "user-res-1",
		Amount:    3000.00,
		AuctionID: "auction-res-1",
	}

	data, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded PaymentReserveRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, req.UserID, decoded.UserID)
	assert.InDelta(t, req.Amount, decoded.Amount, 0.001)
	assert.Equal(t, req.AuctionID, decoded.AuctionID)
}

func TestPaymentReserveResponse_JSON(t *testing.T) {
	tests := []struct {
		name string
		resp PaymentReserveResponse
	}{
		{
			name: "reserved successfully",
			resp: PaymentReserveResponse{
				Reserved:      true,
				ReservationID: "res-uuid-001",
			},
		},
		{
			name: "reservation failed",
			resp: PaymentReserveResponse{
				Reserved: false,
				Reason:   "insufficient funds",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.resp)
			require.NoError(t, err)

			var decoded PaymentReserveResponse
			err = json.Unmarshal(data, &decoded)
			require.NoError(t, err)

			assert.Equal(t, tt.resp.Reserved, decoded.Reserved)
			assert.Equal(t, tt.resp.ReservationID, decoded.ReservationID)
			assert.Equal(t, tt.resp.Reason, decoded.Reason)
		})
	}
}

func TestAuctionCreatedEvent_JSON(t *testing.T) {
	startsAt := time.Date(2025, 8, 1, 10, 0, 0, 0, time.UTC)
	endsAt := startsAt.Add(4 * time.Hour)

	event := AuctionCreatedEvent{
		AuctionID:    "auction-new",
		TenderID:     "tender-new",
		Title:        "Brand New Auction",
		Description:  "Detailed description of the auction item",
		StartPrice:   5000.00,
		MinIncrement: 100.00,
		StartsAt:     startsAt,
		EndsAt:       endsAt,
		CreatedBy:    "admin-user",
	}

	data, err := json.Marshal(event)
	require.NoError(t, err)

	var decoded AuctionCreatedEvent
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, event.AuctionID, decoded.AuctionID)
	assert.Equal(t, event.TenderID, decoded.TenderID)
	assert.Equal(t, event.Title, decoded.Title)
	assert.Equal(t, event.Description, decoded.Description)
	assert.InDelta(t, event.StartPrice, decoded.StartPrice, 0.001)
	assert.InDelta(t, event.MinIncrement, decoded.MinIncrement, 0.001)
	assert.True(t, event.StartsAt.Equal(decoded.StartsAt))
	assert.True(t, event.EndsAt.Equal(decoded.EndsAt))
	assert.Equal(t, event.CreatedBy, decoded.CreatedBy)
}

func TestBidPlacedEvent_JSONFieldNames(t *testing.T) {
	event := BidPlacedEvent{
		BidID:     "b1",
		AuctionID: "a1",
		UserID:    "u1",
		Amount:    100,
		Timestamp: time.Now().UTC(),
	}

	data, err := json.Marshal(event)
	require.NoError(t, err)

	var raw map[string]interface{}
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)

	expectedKeys := []string{"bid_id", "auction_id", "user_id", "amount", "timestamp"}
	for _, key := range expectedKeys {
		_, exists := raw[key]
		assert.True(t, exists, "expected JSON key %q to be present", key)
	}
}
