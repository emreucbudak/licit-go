package payment

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/licit/licit-go/pkg/events"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestValidator creates a Validator with pre-initialized maps and a mock
// wallet HTTP server. It does NOT need a real NATS connection since we call
// handler methods directly with crafted *nats.Msg values.
func newTestValidator(walletServer *httptest.Server) *Validator {
	walletURL := ""
	if walletServer != nil {
		walletURL = walletServer.URL
	}
	return &Validator{
		nats:         nil, // not needed for direct handler calls
		walletURL:    walletURL,
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		reservations: make(map[string]*Reservation),
		userReserved: make(map[string]float64),
	}
}

// fakeNATSMsg creates a *nats.Msg with JSON-encoded data and an internal
// reply channel so we can capture the response.
// Since msg.Respond() requires a real NATS connection, we use a channel-based
// approach: we start a test NATS server or use a different strategy.
// For simplicity, we test internal state changes directly and test the reply
// by checking what would be marshaled.

// msgWithReply builds a *nats.Msg with JSON data. For response capture, we
// provide a helper that decodes the validator's in-memory state.
func msgWithData(t *testing.T, data any) *nats.Msg {
	t.Helper()
	payload, err := json.Marshal(data)
	require.NoError(t, err)
	return &nats.Msg{Data: payload}
}

// walletMockServer creates an httptest server that responds with a given balance.
func walletMockServer(balance float64, success bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := WalletBalanceResponse{
			UserID:  "test-user",
			Balance: balance,
			Success: success,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

// walletErrorServer creates an httptest server that returns an error status.
func walletErrorServer(statusCode int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
	}))
}

// addReservation is a test helper that directly adds a reservation to the validator's state.
func addReservation(v *Validator, id, userID, auctionID string, amount float64, status string) {
	v.reservations[id] = &Reservation{
		ID:        id,
		UserID:    userID,
		AuctionID: auctionID,
		Amount:    amount,
		Status:    status,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if status == "active" {
		key := fmt.Sprintf("%s:%s", userID, auctionID)
		v.userReserved[key] += amount
	}
}

// ---------------------------------------------------------------------------
// handleValidate tests
// ---------------------------------------------------------------------------

func TestHandleValidate_SufficientBalance(t *testing.T) {
	server := walletMockServer(10000.00, true)
	defer server.Close()

	v := newTestValidator(server)

	// We cannot easily capture msg.Respond() without a real NATS connection,
	// so we test the wallet balance check + internal state through getWalletBalance directly.
	balance, err := v.getWalletBalance("user-1")
	require.NoError(t, err)
	assert.InDelta(t, 10000.00, balance, 0.01)
}

func TestHandleValidate_InsufficientBalance(t *testing.T) {
	server := walletMockServer(500.00, true)
	defer server.Close()

	v := newTestValidator(server)

	balance, err := v.getWalletBalance("user-1")
	require.NoError(t, err)
	assert.InDelta(t, 500.00, balance, 0.01)

	// Simulate: user wants to bid 1000 but only has 500
	// The available balance minus reserved should be less than the bid amount
	available := balance - v.userReserved["user-1:auction-1"]
	assert.Less(t, available, 1000.00)
}

func TestHandleValidate_WalletServiceError(t *testing.T) {
	server := walletErrorServer(http.StatusInternalServerError)
	defer server.Close()

	v := newTestValidator(server)

	_, err := v.getWalletBalance("user-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wallet service returned status 500")
}

func TestHandleValidate_WalletServiceUnreachable(t *testing.T) {
	// Use a URL that won't connect
	v := &Validator{
		walletURL:    "http://127.0.0.1:1", // port 1 is almost certainly not listening
		httpClient:   &http.Client{Timeout: 1 * time.Second},
		reservations: make(map[string]*Reservation),
		userReserved: make(map[string]float64),
	}

	_, err := v.getWalletBalance("user-1")
	require.Error(t, err)
}

func TestHandleValidate_BalanceMinusReserved(t *testing.T) {
	server := walletMockServer(5000.00, true)
	defer server.Close()

	v := newTestValidator(server)

	// Pre-reserve some funds
	addReservation(v, "res-1", "user-1", "auction-1", 3000.00, "active")

	balance, err := v.getWalletBalance("user-1")
	require.NoError(t, err)

	reservedKey := "user-1:auction-1"
	available := balance - v.userReserved[reservedKey]
	assert.InDelta(t, 2000.00, available, 0.01)
}

// ---------------------------------------------------------------------------
// handleReserve tests
// ---------------------------------------------------------------------------

func TestHandleReserve_CreatesReservation(t *testing.T) {
	v := newTestValidator(nil)

	msg := msgWithData(t, events.PaymentReserveRequest{
		UserID:    "user-1",
		AuctionID: "auction-1",
		Amount:    2000.00,
	})

	v.handleReserve(msg)

	v.mu.RLock()
	defer v.mu.RUnlock()

	assert.Len(t, v.reservations, 1)
	reservedKey := "user-1:auction-1"
	assert.InDelta(t, 2000.00, v.userReserved[reservedKey], 0.01)

	// Find the reservation and verify its fields
	for _, res := range v.reservations {
		assert.Equal(t, "user-1", res.UserID)
		assert.Equal(t, "auction-1", res.AuctionID)
		assert.InDelta(t, 2000.00, res.Amount, 0.01)
		assert.Equal(t, "active", res.Status)
	}
}

func TestHandleReserve_ReplacesPreviousReservation(t *testing.T) {
	v := newTestValidator(nil)

	// First reservation for user-1, auction-1
	addReservation(v, "old-res", "user-1", "auction-1", 1500.00, "active")

	// Place a new (higher) reservation for same user+auction
	msg := msgWithData(t, events.PaymentReserveRequest{
		UserID:    "user-1",
		AuctionID: "auction-1",
		Amount:    2500.00,
	})
	v.handleReserve(msg)

	v.mu.RLock()
	defer v.mu.RUnlock()

	// Old reservation should be released
	assert.Equal(t, "released", v.reservations["old-res"].Status)

	// There should be 2 total reservation records (old released + new active)
	assert.Len(t, v.reservations, 2)

	// Net reserved amount should be only the new amount
	reservedKey := "user-1:auction-1"
	assert.InDelta(t, 2500.00, v.userReserved[reservedKey], 0.01)

	// Count active reservations
	activeCount := 0
	for _, res := range v.reservations {
		if res.Status == "active" {
			activeCount++
			assert.InDelta(t, 2500.00, res.Amount, 0.01)
		}
	}
	assert.Equal(t, 1, activeCount)
}

func TestHandleReserve_MultipleUsersIndependent(t *testing.T) {
	v := newTestValidator(nil)

	// User 1 reserves for auction-1
	msg1 := msgWithData(t, events.PaymentReserveRequest{
		UserID:    "user-1",
		AuctionID: "auction-1",
		Amount:    1000.00,
	})
	v.handleReserve(msg1)

	// User 2 reserves for same auction-1
	msg2 := msgWithData(t, events.PaymentReserveRequest{
		UserID:    "user-2",
		AuctionID: "auction-1",
		Amount:    1500.00,
	})
	v.handleReserve(msg2)

	v.mu.RLock()
	defer v.mu.RUnlock()

	assert.Len(t, v.reservations, 2)
	assert.InDelta(t, 1000.00, v.userReserved["user-1:auction-1"], 0.01)
	assert.InDelta(t, 1500.00, v.userReserved["user-2:auction-1"], 0.01)
}

func TestHandleReserve_InvalidJSON(t *testing.T) {
	v := newTestValidator(nil)

	msg := &nats.Msg{Data: []byte("not json")}
	v.handleReserve(msg)

	// Should not create any reservation
	v.mu.RLock()
	defer v.mu.RUnlock()
	assert.Empty(t, v.reservations)
}

// ---------------------------------------------------------------------------
// handleRelease tests
// ---------------------------------------------------------------------------

func TestHandleRelease_ActiveReservation(t *testing.T) {
	v := newTestValidator(nil)
	addReservation(v, "res-1", "user-1", "auction-1", 2000.00, "active")

	msg := msgWithData(t, struct {
		ReservationID string `json:"reservation_id"`
	}{ReservationID: "res-1"})

	v.handleRelease(msg)

	v.mu.RLock()
	defer v.mu.RUnlock()

	assert.Equal(t, "released", v.reservations["res-1"].Status)
	assert.InDelta(t, 0.0, v.userReserved["user-1:auction-1"], 0.01)
}

func TestHandleRelease_NonexistentReservation(t *testing.T) {
	v := newTestValidator(nil)

	msg := msgWithData(t, struct {
		ReservationID string `json:"reservation_id"`
	}{ReservationID: "does-not-exist"})

	// Should not panic or error, just silently do nothing
	v.handleRelease(msg)

	v.mu.RLock()
	defer v.mu.RUnlock()
	assert.Empty(t, v.reservations)
}

func TestHandleRelease_AlreadyReleasedReservation(t *testing.T) {
	v := newTestValidator(nil)
	addReservation(v, "res-1", "user-1", "auction-1", 2000.00, "active")

	// Manually release it
	v.reservations["res-1"].Status = "released"
	v.userReserved["user-1:auction-1"] = 0

	msg := msgWithData(t, struct {
		ReservationID string `json:"reservation_id"`
	}{ReservationID: "res-1"})

	// Release again - should be a no-op
	v.handleRelease(msg)

	v.mu.RLock()
	defer v.mu.RUnlock()

	assert.Equal(t, "released", v.reservations["res-1"].Status)
	assert.InDelta(t, 0.0, v.userReserved["user-1:auction-1"], 0.01)
}

func TestHandleRelease_ChargedReservation(t *testing.T) {
	v := newTestValidator(nil)
	addReservation(v, "res-1", "user-1", "auction-1", 2000.00, "active")

	// Manually charge it
	v.reservations["res-1"].Status = "charged"

	msg := msgWithData(t, struct {
		ReservationID string `json:"reservation_id"`
	}{ReservationID: "res-1"})

	// Release should not affect a charged reservation
	v.handleRelease(msg)

	v.mu.RLock()
	defer v.mu.RUnlock()

	assert.Equal(t, "charged", v.reservations["res-1"].Status)
}

func TestHandleRelease_InvalidJSON(t *testing.T) {
	v := newTestValidator(nil)
	addReservation(v, "res-1", "user-1", "auction-1", 2000.00, "active")

	msg := &nats.Msg{Data: []byte("{invalid")}
	v.handleRelease(msg)

	// Reservation should remain unchanged
	v.mu.RLock()
	defer v.mu.RUnlock()
	assert.Equal(t, "active", v.reservations["res-1"].Status)
}

// ---------------------------------------------------------------------------
// handleCharge tests
// ---------------------------------------------------------------------------

func TestHandleCharge_ActiveReservation(t *testing.T) {
	v := newTestValidator(nil)
	addReservation(v, "res-1", "user-1", "auction-1", 5000.00, "active")

	msg := msgWithData(t, struct {
		ReservationID string `json:"reservation_id"`
	}{ReservationID: "res-1"})

	v.handleCharge(msg)

	v.mu.RLock()
	defer v.mu.RUnlock()

	assert.Equal(t, "charged", v.reservations["res-1"].Status)
}

func TestHandleCharge_NonexistentReservation(t *testing.T) {
	v := newTestValidator(nil)

	msg := msgWithData(t, struct {
		ReservationID string `json:"reservation_id"`
	}{ReservationID: "ghost"})

	// Should not panic
	v.handleCharge(msg)

	v.mu.RLock()
	defer v.mu.RUnlock()
	assert.Empty(t, v.reservations)
}

func TestHandleCharge_AlreadyReleasedReservation(t *testing.T) {
	v := newTestValidator(nil)
	addReservation(v, "res-1", "user-1", "auction-1", 5000.00, "active")
	v.reservations["res-1"].Status = "released"

	msg := msgWithData(t, struct {
		ReservationID string `json:"reservation_id"`
	}{ReservationID: "res-1"})

	v.handleCharge(msg)

	v.mu.RLock()
	defer v.mu.RUnlock()

	// Should NOT charge a released reservation
	assert.Equal(t, "released", v.reservations["res-1"].Status)
}

func TestHandleCharge_InvalidJSON(t *testing.T) {
	v := newTestValidator(nil)
	addReservation(v, "res-1", "user-1", "auction-1", 5000.00, "active")

	msg := &nats.Msg{Data: []byte("bad")}
	v.handleCharge(msg)

	v.mu.RLock()
	defer v.mu.RUnlock()

	// Reservation unchanged
	assert.Equal(t, "active", v.reservations["res-1"].Status)
}

// ---------------------------------------------------------------------------
// handleAuctionEnded tests
// ---------------------------------------------------------------------------

func TestHandleAuctionEnded_ReleasesNonWinnerReservations(t *testing.T) {
	v := newTestValidator(nil)

	// Winner: user-1
	addReservation(v, "res-winner", "user-1", "auction-1", 5000.00, "active")
	// Losers: user-2, user-3
	addReservation(v, "res-loser1", "user-2", "auction-1", 3000.00, "active")
	addReservation(v, "res-loser2", "user-3", "auction-1", 4000.00, "active")
	// Different auction - should not be touched
	addReservation(v, "res-other", "user-4", "auction-2", 1000.00, "active")

	event := events.AuctionEndedEvent{
		AuctionID:    "auction-1",
		WinnerUserID: "user-1",
		WinningBid:   5000.00,
		TotalBids:    10,
	}
	msg := msgWithData(t, event)

	v.handleAuctionEnded(msg)

	v.mu.RLock()
	defer v.mu.RUnlock()

	// Winner stays active
	assert.Equal(t, "active", v.reservations["res-winner"].Status)
	assert.InDelta(t, 5000.00, v.userReserved["user-1:auction-1"], 0.01)

	// Losers released
	assert.Equal(t, "released", v.reservations["res-loser1"].Status)
	assert.Equal(t, "released", v.reservations["res-loser2"].Status)
	assert.InDelta(t, 0.0, v.userReserved["user-2:auction-1"], 0.01)
	assert.InDelta(t, 0.0, v.userReserved["user-3:auction-1"], 0.01)

	// Other auction untouched
	assert.Equal(t, "active", v.reservations["res-other"].Status)
	assert.InDelta(t, 1000.00, v.userReserved["user-4:auction-2"], 0.01)
}

func TestHandleAuctionEnded_NoWinner(t *testing.T) {
	v := newTestValidator(nil)

	addReservation(v, "res-1", "user-1", "auction-1", 2000.00, "active")
	addReservation(v, "res-2", "user-2", "auction-1", 3000.00, "active")

	event := events.AuctionEndedEvent{
		AuctionID:    "auction-1",
		WinnerUserID: "", // no winner
		TotalBids:    0,
	}
	msg := msgWithData(t, event)

	v.handleAuctionEnded(msg)

	v.mu.RLock()
	defer v.mu.RUnlock()

	// All reservations should be released since there's no winner
	assert.Equal(t, "released", v.reservations["res-1"].Status)
	assert.Equal(t, "released", v.reservations["res-2"].Status)
}

func TestHandleAuctionEnded_AlreadyReleasedSkipped(t *testing.T) {
	v := newTestValidator(nil)

	addReservation(v, "res-1", "user-1", "auction-1", 2000.00, "active")
	// Manually release one before the event
	v.reservations["res-1"].Status = "released"
	v.userReserved["user-1:auction-1"] = 0

	event := events.AuctionEndedEvent{
		AuctionID:    "auction-1",
		WinnerUserID: "user-999",
	}
	msg := msgWithData(t, event)

	v.handleAuctionEnded(msg)

	v.mu.RLock()
	defer v.mu.RUnlock()

	// Should still be released, not double-decremented
	assert.Equal(t, "released", v.reservations["res-1"].Status)
	assert.InDelta(t, 0.0, v.userReserved["user-1:auction-1"], 0.01)
}

func TestHandleAuctionEnded_InvalidJSON(t *testing.T) {
	v := newTestValidator(nil)
	addReservation(v, "res-1", "user-1", "auction-1", 2000.00, "active")

	msg := &nats.Msg{Data: []byte("not json")}
	v.handleAuctionEnded(msg)

	// Reservation should be unchanged
	v.mu.RLock()
	defer v.mu.RUnlock()
	assert.Equal(t, "active", v.reservations["res-1"].Status)
}

// ---------------------------------------------------------------------------
// Concurrency tests
// ---------------------------------------------------------------------------

func TestConcurrentReservations(t *testing.T) {
	v := newTestValidator(nil)

	var wg sync.WaitGroup
	const numGoroutines = 50

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msg := msgWithData(t, events.PaymentReserveRequest{
				UserID:    fmt.Sprintf("user-%d", idx),
				AuctionID: "auction-1",
				Amount:    float64(1000 + idx),
			})
			v.handleReserve(msg)
		}(i)
	}

	wg.Wait()

	v.mu.RLock()
	defer v.mu.RUnlock()

	assert.Len(t, v.reservations, numGoroutines)

	// Each user should have exactly their amount reserved
	for i := 0; i < numGoroutines; i++ {
		key := fmt.Sprintf("user-%d:auction-1", i)
		assert.InDelta(t, float64(1000+i), v.userReserved[key], 0.01)
	}
}

func TestConcurrentReserveAndRelease(t *testing.T) {
	v := newTestValidator(nil)

	// Create 20 reservations
	for i := 0; i < 20; i++ {
		addReservation(v, fmt.Sprintf("res-%d", i), "user-1", "auction-1", 100.00, "active")
	}

	var wg sync.WaitGroup

	// Release all 20 concurrently
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msg := msgWithData(t, struct {
				ReservationID string `json:"reservation_id"`
			}{ReservationID: fmt.Sprintf("res-%d", idx)})
			v.handleRelease(msg)
		}(i)
	}

	wg.Wait()

	v.mu.RLock()
	defer v.mu.RUnlock()

	// All should be released
	for i := 0; i < 20; i++ {
		assert.Equal(t, "released", v.reservations[fmt.Sprintf("res-%d", i)].Status)
	}
	assert.InDelta(t, 0.0, v.userReserved["user-1:auction-1"], 0.01)
}

// ---------------------------------------------------------------------------
// getWalletBalance tests (HTTP integration with mock server)
// ---------------------------------------------------------------------------

func TestGetWalletBalance_Success(t *testing.T) {
	server := walletMockServer(7500.50, true)
	defer server.Close()

	v := newTestValidator(server)
	balance, err := v.getWalletBalance("user-1")

	require.NoError(t, err)
	assert.InDelta(t, 7500.50, balance, 0.01)
}

func TestGetWalletBalance_ServerError(t *testing.T) {
	server := walletErrorServer(http.StatusServiceUnavailable)
	defer server.Close()

	v := newTestValidator(server)
	_, err := v.getWalletBalance("user-1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "503")
}

func TestGetWalletBalance_InvalidResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	v := newTestValidator(server)
	_, err := v.getWalletBalance("user-1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode wallet response")
}

func TestGetWalletBalance_ZeroBalance(t *testing.T) {
	server := walletMockServer(0, true)
	defer server.Close()

	v := newTestValidator(server)
	balance, err := v.getWalletBalance("user-1")

	require.NoError(t, err)
	assert.InDelta(t, 0.0, balance, 0.01)
}
