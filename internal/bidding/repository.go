package bidding

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Migrate(ctx context.Context) error {
	query := `
	CREATE TABLE IF NOT EXISTS auctions (
		id            VARCHAR(36) PRIMARY KEY,
		tender_id     VARCHAR(36) NOT NULL,
		title         VARCHAR(500) NOT NULL,
		description   TEXT,
		start_price   DECIMAL(18,2) NOT NULL,
		current_price DECIMAL(18,2) NOT NULL,
		min_increment DECIMAL(18,2) NOT NULL DEFAULT 1.00,
		status        VARCHAR(20) NOT NULL DEFAULT 'pending',
		starts_at     TIMESTAMPTZ NOT NULL,
		ends_at       TIMESTAMPTZ NOT NULL,
		created_by    VARCHAR(36) NOT NULL,
		created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS bids (
		id         VARCHAR(36) PRIMARY KEY,
		auction_id VARCHAR(36) NOT NULL REFERENCES auctions(id),
		user_id    VARCHAR(36) NOT NULL,
		amount     DECIMAL(18,2) NOT NULL,
		status     VARCHAR(20) NOT NULL DEFAULT 'pending',
		reason     TEXT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_bids_auction_id ON bids(auction_id);
	CREATE INDEX IF NOT EXISTS idx_bids_user_id ON bids(user_id);
	CREATE INDEX IF NOT EXISTS idx_auctions_status ON auctions(status);
	`
	_, err := r.db.Exec(ctx, query)
	return err
}

func (r *Repository) CreateAuction(ctx context.Context, a *Auction) error {
	query := `
		INSERT INTO auctions (id, tender_id, title, description, start_price, current_price, min_increment, status, starts_at, ends_at, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`
	_, err := r.db.Exec(ctx, query,
		a.ID, a.TenderID, a.Title, a.Description, a.StartPrice, a.CurrentPrice,
		a.MinIncrement, a.Status, a.StartsAt, a.EndsAt, a.CreatedBy, a.CreatedAt, a.UpdatedAt,
	)
	return err
}

func (r *Repository) GetAuction(ctx context.Context, id string) (*Auction, error) {
	query := `SELECT id, tender_id, title, description, start_price, current_price, min_increment, status, starts_at, ends_at, created_by, created_at, updated_at FROM auctions WHERE id = $1`
	var a Auction
	err := r.db.QueryRow(ctx, query, id).Scan(
		&a.ID, &a.TenderID, &a.Title, &a.Description, &a.StartPrice, &a.CurrentPrice,
		&a.MinIncrement, &a.Status, &a.StartsAt, &a.EndsAt, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("auction not found: %s", id)
	}
	return &a, err
}

func (r *Repository) GetActiveAuctions(ctx context.Context) ([]Auction, error) {
	query := `SELECT id, tender_id, title, description, start_price, current_price, min_increment, status, starts_at, ends_at, created_by, created_at, updated_at
		FROM auctions WHERE status = 'active' ORDER BY ends_at ASC`
	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var auctions []Auction
	for rows.Next() {
		var a Auction
		if err := rows.Scan(&a.ID, &a.TenderID, &a.Title, &a.Description, &a.StartPrice, &a.CurrentPrice,
			&a.MinIncrement, &a.Status, &a.StartsAt, &a.EndsAt, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		auctions = append(auctions, a)
	}
	return auctions, nil
}

func (r *Repository) UpdateAuctionPrice(ctx context.Context, auctionID string, price float64) error {
	query := `UPDATE auctions SET current_price = $1, updated_at = $2 WHERE id = $3`
	_, err := r.db.Exec(ctx, query, price, time.Now(), auctionID)
	return err
}

func (r *Repository) UpdateAuctionStatus(ctx context.Context, auctionID, status string) error {
	query := `UPDATE auctions SET status = $1, updated_at = $2 WHERE id = $3`
	_, err := r.db.Exec(ctx, query, status, time.Now(), auctionID)
	return err
}

func (r *Repository) CreateBid(ctx context.Context, b *Bid) error {
	query := `INSERT INTO bids (id, auction_id, user_id, amount, status, reason, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`
	_, err := r.db.Exec(ctx, query, b.ID, b.AuctionID, b.UserID, b.Amount, b.Status, b.Reason, b.CreatedAt)
	return err
}

// CreateBidAndUpdatePrice atomically creates a bid and updates the auction price in a single transaction.
func (r *Repository) CreateBidAndUpdatePrice(ctx context.Context, b *Bid, newPrice float64) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	bidQuery := `INSERT INTO bids (id, auction_id, user_id, amount, status, reason, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`
	if _, err := tx.Exec(ctx, bidQuery, b.ID, b.AuctionID, b.UserID, b.Amount, b.Status, b.Reason, b.CreatedAt); err != nil {
		return fmt.Errorf("create bid: %w", err)
	}

	priceQuery := `UPDATE auctions SET current_price = GREATEST(current_price, $1), updated_at = $2 WHERE id = $3`
	if _, err := tx.Exec(ctx, priceQuery, newPrice, time.Now(), b.AuctionID); err != nil {
		return fmt.Errorf("update auction price: %w", err)
	}

	return tx.Commit(ctx)
}

func (r *Repository) UpdateBidStatus(ctx context.Context, bidID, status, reason string) error {
	query := `UPDATE bids SET status = $1, reason = $2 WHERE id = $3`
	_, err := r.db.Exec(ctx, query, status, reason, bidID)
	return err
}

func (r *Repository) GetBidsByAuction(ctx context.Context, auctionID string) ([]Bid, error) {
	query := `SELECT id, auction_id, user_id, amount, status, reason, created_at FROM bids WHERE auction_id = $1 ORDER BY amount DESC`
	rows, err := r.db.Query(ctx, query, auctionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bids []Bid
	for rows.Next() {
		var b Bid
		if err := rows.Scan(&b.ID, &b.AuctionID, &b.UserID, &b.Amount, &b.Status, &b.Reason, &b.CreatedAt); err != nil {
			return nil, err
		}
		bids = append(bids, b)
	}
	return bids, nil
}

func (r *Repository) GetHighestBid(ctx context.Context, auctionID string) (*Bid, error) {
	query := `SELECT id, auction_id, user_id, amount, status, reason, created_at FROM bids WHERE auction_id = $1 AND status = 'accepted' ORDER BY amount DESC LIMIT 1`
	var b Bid
	err := r.db.QueryRow(ctx, query, auctionID).Scan(&b.ID, &b.AuctionID, &b.UserID, &b.Amount, &b.Status, &b.Reason, &b.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &b, err
}
