package bidding

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Get("/auctions", h.listActiveAuctions)
	r.Get("/auctions/{id}", h.getAuction)
	r.Get("/auctions/{id}/bids", h.listBids)
	r.Post("/auctions/{id}/bids", h.placeBid)

	return r
}

func (h *Handler) listActiveAuctions(w http.ResponseWriter, r *http.Request) {
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

func (h *Handler) getAuction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "gecersiz auction ID formati")
		return
	}
	auction, err := h.svc.GetAuction(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "ihale bulunamadi")
		return
	}
	writeJSON(w, http.StatusOK, auction)
}

func (h *Handler) listBids(w http.ResponseWriter, r *http.Request) {
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

func (h *Handler) placeBid(w http.ResponseWriter, r *http.Request) {
	auctionID := chi.URLParam(r, "id")

	// UserID normally comes from JWT middleware
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
	req.IdempotencyKey = normalizeIdempotencyKey(req.IdempotencyKey)
	if headerKey := normalizeIdempotencyKey(r.Header.Get(idempotencyKeyHeader)); headerKey != "" {
		req.IdempotencyKey = headerKey
	}

	if !validateIdempotencyKey(req.IdempotencyKey) {
		writeError(w, http.StatusBadRequest, "idempotency key uuid formatinda olmali")
		return
	}

	resp, err := h.svc.PlaceBid(r.Context(), userID, req)
	if err != nil {
		slog.Error("place bid error", "error", err)
		writeError(w, http.StatusInternalServerError, "teklif isleme hatasi")
		return
	}

	status := http.StatusOK
	if resp.Status == "rejected" {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, resp)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("writeJSON encode failed", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
