package bidding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// writeJSON / writeError helper tests (direct unit tests, no mocks needed)
// ---------------------------------------------------------------------------

func TestWriteJSON(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		data       any
		wantStatus int
		wantBody   string
	}{
		{
			name:       "200 with map",
			status:     http.StatusOK,
			data:       map[string]string{"message": "ok"},
			wantStatus: http.StatusOK,
			wantBody:   `{"message":"ok"}`,
		},
		{
			name:       "201 with struct",
			status:     http.StatusCreated,
			data:       PlaceBidResponse{BidID: "b1", Status: "accepted", Message: "done"},
			wantStatus: http.StatusCreated,
			wantBody:   `{"bid_id":"b1","status":"accepted","message":"done"}`,
		},
		{
			name:       "404 with error",
			status:     http.StatusNotFound,
			data:       map[string]string{"error": "not found"},
			wantStatus: http.StatusNotFound,
			wantBody:   `{"error":"not found"}`,
		},
		{
			name:       "200 with empty list",
			status:     http.StatusOK,
			data:       AuctionListResponse{Auctions: []Auction{}, Total: 0},
			wantStatus: http.StatusOK,
			wantBody:   `{"auctions":[],"total":0}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeJSON(w, tt.status, tt.data)

			assert.Equal(t, tt.wantStatus, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

			// Compare JSON structurally (to ignore whitespace differences from Encode's trailing newline)
			var expected, actual interface{}
			require.NoError(t, json.Unmarshal([]byte(tt.wantBody), &expected))
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &actual))
			assert.Equal(t, expected, actual)
		})
	}
}

func TestWriteError(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		message    string
		wantStatus int
	}{
		{
			name:       "bad request",
			status:     http.StatusBadRequest,
			message:    "gecersiz istek",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "internal error",
			status:     http.StatusInternalServerError,
			message:    "sunucu hatasi",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "unauthorized",
			status:     http.StatusUnauthorized,
			message:    "yetkisiz erisim",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeError(w, tt.status, tt.message)

			assert.Equal(t, tt.wantStatus, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

			var body map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, tt.message, body["error"])
		})
	}
}

// ---------------------------------------------------------------------------
// Service interface for handler-level mocking
// ---------------------------------------------------------------------------

type handlerServiceInterface interface {
	GetActiveAuctions(ctx context.Context) ([]Auction, error)
	GetAuction(ctx context.Context, id string) (*Auction, error)
	GetBidsByAuction(ctx context.Context, auctionID string) ([]Bid, error)
	PlaceBid(ctx context.Context, userID string, req PlaceBidRequest) (*PlaceBidResponse, error)
}

type mockHandlerService struct {
	auctions    []Auction
	auctionsErr error
	auction     *Auction
	auctionErr  error
	bids        []Bid
	bidsErr     error
	bidResp     *PlaceBidResponse
	bidErr      error
}

func (m *mockHandlerService) GetActiveAuctions(_ context.Context) ([]Auction, error) {
	return m.auctions, m.auctionsErr
}

func (m *mockHandlerService) GetAuction(_ context.Context, _ string) (*Auction, error) {
	return m.auction, m.auctionErr
}

func (m *mockHandlerService) GetBidsByAuction(_ context.Context, _ string) ([]Bid, error) {
	return m.bids, m.bidsErr
}

func (m *mockHandlerService) PlaceBid(_ context.Context, _ string, _ PlaceBidRequest) (*PlaceBidResponse, error) {
	return m.bidResp, m.bidErr
}

// ---------------------------------------------------------------------------
// testHandler wraps handlers that use the mockable interface.
// This mirrors Handler but uses the interface.
// ---------------------------------------------------------------------------

type testHandler struct {
	svc handlerServiceInterface
}

func (h *testHandler) routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/auctions", h.listActiveAuctions)
	r.Get("/auctions/{id}", h.getAuction)
	r.Get("/auctions/{id}/bids", h.listBids)
	r.Post("/auctions/{id}/bids", h.placeBid)
	return r
}

func (h *testHandler) listActiveAuctions(w http.ResponseWriter, r *http.Request) {
	auctions, err := h.svc.GetActiveAuctions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if auctions == nil {
		auctions = []Auction{}
	}
	writeJSON(w, http.StatusOK, AuctionListResponse{Auctions: auctions, Total: len(auctions)})
}

func (h *testHandler) getAuction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	auction, err := h.svc.GetAuction(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "ihale bulunamadi")
		return
	}
	writeJSON(w, http.StatusOK, auction)
}

func (h *testHandler) listBids(w http.ResponseWriter, r *http.Request) {
	auctionID := chi.URLParam(r, "id")
	bids, err := h.svc.GetBidsByAuction(r.Context(), auctionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if bids == nil {
		bids = []Bid{}
	}
	writeJSON(w, http.StatusOK, bids)
}

func (h *testHandler) placeBid(w http.ResponseWriter, r *http.Request) {
	auctionID := chi.URLParam(r, "id")

	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "kullanici kimlik dogrulamasi gerekli")
		return
	}

	var req PlaceBidRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "gecersiz istek")
		return
	}
	req.AuctionID = auctionID

	resp, err := h.svc.PlaceBid(r.Context(), userID, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "teklif isleme hatasi")
		return
	}

	status := http.StatusOK
	if resp.Status == "rejected" {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, resp)
}

// ---------------------------------------------------------------------------
// HTTP endpoint tests
// ---------------------------------------------------------------------------

func TestListActiveAuctions_Success(t *testing.T) {
	svc := &mockHandlerService{
		auctions: []Auction{
			{ID: "a1", Title: "Auction 1", Status: "active"},
			{ID: "a2", Title: "Auction 2", Status: "active"},
		},
	}
	h := &testHandler{svc: svc}
	router := h.routes()

	req := httptest.NewRequest(http.MethodGet, "/auctions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var body AuctionListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, 2, body.Total)
	assert.Len(t, body.Auctions, 2)
}

func TestListActiveAuctions_Empty(t *testing.T) {
	svc := &mockHandlerService{auctions: nil}
	h := &testHandler{svc: svc}
	router := h.routes()

	req := httptest.NewRequest(http.MethodGet, "/auctions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var body AuctionListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, 0, body.Total)
	assert.Empty(t, body.Auctions)
}

func TestListActiveAuctions_ServiceError(t *testing.T) {
	svc := &mockHandlerService{auctionsErr: fmt.Errorf("db failure")}
	h := &testHandler{svc: svc}
	router := h.routes()

	req := httptest.NewRequest(http.MethodGet, "/auctions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Contains(t, body["error"], "db failure")
}

func TestGetAuction_Success(t *testing.T) {
	svc := &mockHandlerService{
		auction: &Auction{ID: "550e8400-e29b-41d4-a716-446655440000", Title: "Found Auction"},
	}
	h := &testHandler{svc: svc}
	router := h.routes()

	req := httptest.NewRequest(http.MethodGet, "/auctions/550e8400-e29b-41d4-a716-446655440000", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var body Auction
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "Found Auction", body.Title)
}

func TestGetAuctionHandler_NotFound(t *testing.T) {
	svc := &mockHandlerService{auctionErr: fmt.Errorf("not found")}
	h := &testHandler{svc: svc}
	router := h.routes()

	req := httptest.NewRequest(http.MethodGet, "/auctions/550e8400-e29b-41d4-a716-446655440000", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "ihale bulunamadi", body["error"])
}

func TestListBids_Success(t *testing.T) {
	svc := &mockHandlerService{
		bids: []Bid{
			{ID: "b1", Amount: 2000, Status: "accepted"},
			{ID: "b2", Amount: 1500, Status: "accepted"},
		},
	}
	h := &testHandler{svc: svc}
	router := h.routes()

	req := httptest.NewRequest(http.MethodGet, "/auctions/auction-1/bids", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var body []Bid
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Len(t, body, 2)
}

func TestListBids_Empty(t *testing.T) {
	svc := &mockHandlerService{bids: nil}
	h := &testHandler{svc: svc}
	router := h.routes()

	req := httptest.NewRequest(http.MethodGet, "/auctions/auction-1/bids", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var body []Bid
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Empty(t, body)
}

func TestPlaceBid_NoUserID(t *testing.T) {
	svc := &mockHandlerService{}
	h := &testHandler{svc: svc}
	router := h.routes()

	body := `{"amount": 2000}`
	req := httptest.NewRequest(http.MethodPost, "/auctions/auction-1/bids", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	// No X-User-ID header
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)

	var respBody map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &respBody))
	assert.Equal(t, "kullanici kimlik dogrulamasi gerekli", respBody["error"])
}

func TestPlaceBid_InvalidJSON(t *testing.T) {
	svc := &mockHandlerService{}
	h := &testHandler{svc: svc}
	router := h.routes()

	req := httptest.NewRequest(http.MethodPost, "/auctions/auction-1/bids", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "user-1")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "gecersiz istek", body["error"])
}

func TestPlaceBid_Accepted(t *testing.T) {
	svc := &mockHandlerService{
		bidResp: &PlaceBidResponse{
			BidID:   "bid-new",
			Status:  "accepted",
			Message: "teklif kabul edildi",
		},
	}
	h := &testHandler{svc: svc}
	router := h.routes()

	body := `{"amount": 2000}`
	req := httptest.NewRequest(http.MethodPost, "/auctions/auction-1/bids", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "user-1")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var respBody PlaceBidResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &respBody))
	assert.Equal(t, "accepted", respBody.Status)
	assert.Equal(t, "bid-new", respBody.BidID)
}

func TestPlaceBid_Rejected(t *testing.T) {
	svc := &mockHandlerService{
		bidResp: &PlaceBidResponse{
			Status:  "rejected",
			Message: "ihale aktif degil",
		},
	}
	h := &testHandler{svc: svc}
	router := h.routes()

	body := `{"amount": 2000}`
	req := httptest.NewRequest(http.MethodPost, "/auctions/auction-1/bids", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "user-1")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var respBody PlaceBidResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &respBody))
	assert.Equal(t, "rejected", respBody.Status)
}

func TestPlaceBid_ServiceError(t *testing.T) {
	svc := &mockHandlerService{
		bidErr: fmt.Errorf("internal error"),
	}
	h := &testHandler{svc: svc}
	router := h.routes()

	body := `{"amount": 2000}`
	req := httptest.NewRequest(http.MethodPost, "/auctions/auction-1/bids", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "user-1")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var respBody map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &respBody))
	assert.Equal(t, "teklif isleme hatasi", respBody["error"])
}

func TestContentTypeHeader(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, map[string]string{"test": "value"})

	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
}
