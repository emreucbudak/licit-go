package bidding

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/licit/licit-go/internal/config"
	redis "github.com/redis/go-redis/v9"
)

const (
	bidIdempotencyTTL = 24 * time.Hour
	bidStateMinTTL    = 24 * time.Hour
)

const prepareBidScript = `
local idem_key = KEYS[1]
local auction_key = KEYS[2]

local now_ms = tonumber(ARGV[1])
local amount = tonumber(ARGV[2])
local db_current = tonumber(ARGV[3])
local min_increment = tonumber(ARGV[4])
local bid_id = ARGV[5]
local user_id = ARGV[6]
local auction_id = ARGV[7]
local idem_ttl = tonumber(ARGV[8])
local state_ttl = tonumber(ARGV[9])

if redis.call("EXISTS", idem_key) == 1 then
	local existing = redis.call("HMGET", idem_key, "status", "bid_id", "message", "amount")
	return {0, existing[1] or "processing", existing[2] or "", existing[3] or "tekrar eden idempotency key", existing[4] or "0", 0, 1}
end

local state = redis.call("HMGET", auction_key, "current_price", "min_increment", "current_bid_id", "current_user_id")
local current = tonumber(state[1])
if current == nil or db_current > current then
	current = db_current
end

local current_min_increment = tonumber(state[2])
if current_min_increment == nil or current_min_increment ~= min_increment then
	current_min_increment = min_increment
end

local previous_bid_id = state[3] or ""
local previous_user_id = state[4] or ""
local required = current + current_min_increment

if amount < required then
	local message = "teklif en az " .. string.format("%.2f", required / 100) .. " olmali"
	redis.call("HSET", idem_key,
		"status", "rejected",
		"bid_id", bid_id,
		"message", message,
		"amount", amount,
		"auction_id", auction_id,
		"user_id", user_id,
		"created_at", now_ms
	)
	redis.call("PEXPIRE", idem_key, idem_ttl)
	redis.call("HSET", auction_key, "current_price", current, "min_increment", current_min_increment, "updated_at", now_ms)
	redis.call("PEXPIRE", auction_key, state_ttl)
	return {0, "rejected", bid_id, message, amount, required, 0}
end

redis.call("HSET", idem_key,
	"status", "processing",
	"bid_id", bid_id,
	"message", "teklif isleniyor",
	"amount", amount,
	"auction_id", auction_id,
	"user_id", user_id,
	"previous_price", current,
	"previous_bid_id", previous_bid_id,
	"previous_user_id", previous_user_id,
	"created_at", now_ms,
	"updated_at", now_ms
)
redis.call("PEXPIRE", idem_key, idem_ttl)

redis.call("HSET", auction_key,
	"current_price", amount,
	"min_increment", current_min_increment,
	"current_bid_id", bid_id,
	"current_user_id", user_id,
	"updated_at", now_ms
)
redis.call("PEXPIRE", auction_key, state_ttl)

return {1, "processing", bid_id, "teklif isleniyor", amount, required, 0}
`

const completeBidScript = `
local idem_key = KEYS[1]
local auction_key = KEYS[2]

local bid_id = ARGV[1]
local final_status = ARGV[2]
local message = ARGV[3]
local now_ms = tonumber(ARGV[4])
local idem_ttl = tonumber(ARGV[5])
local state_ttl = tonumber(ARGV[6])

if redis.call("EXISTS", idem_key) == 0 then
	return {0, "missing"}
end

local idem = redis.call("HMGET", idem_key, "bid_id", "previous_price", "previous_bid_id", "previous_user_id")
if idem[1] ~= bid_id then
	return {0, "bid_mismatch"}
end

if final_status ~= "accepted" then
	local current_bid_id = redis.call("HGET", auction_key, "current_bid_id")
	if current_bid_id == bid_id then
		local previous_price = idem[2] or "0"
		local previous_bid_id = idem[3] or ""
		local previous_user_id = idem[4] or ""
		redis.call("HSET", auction_key,
			"current_price", previous_price,
			"current_bid_id", previous_bid_id,
			"current_user_id", previous_user_id,
			"updated_at", now_ms
		)
	end
end

redis.call("HSET", idem_key, "status", final_status, "message", message, "updated_at", now_ms)
redis.call("PEXPIRE", idem_key, idem_ttl)
redis.call("PEXPIRE", auction_key, state_ttl)

return {1, final_status}
`

var (
	redisPrepareBidScript  = redis.NewScript(prepareBidScript)
	redisCompleteBidScript = redis.NewScript(completeBidScript)
)

type bidProcessor interface {
	PrepareBid(ctx context.Context, cmd prepareBidCommand) (*bidProcessingResult, error)
	CompleteBid(ctx context.Context, cmd completeBidCommand) error
	Close() error
}

type prepareBidCommand struct {
	UserID          string
	AuctionID       string
	RequestID       string
	BidID           string
	Amount          float64
	CurrentPrice    float64
	MinIncrement    float64
	AuctionEndsAt   time.Time
	IdempotencyTTL  time.Duration
	AuctionStateTTL time.Duration
}

type completeBidCommand struct {
	UserID          string
	RequestID       string
	AuctionID       string
	BidID           string
	Status          string
	Message         string
	IdempotencyTTL  time.Duration
	AuctionStateTTL time.Duration
}

type bidProcessingResult struct {
	Allowed   bool
	Duplicate bool
	Status    string
	BidID     string
	Message   string
	Amount    float64
	Required  float64
}

type redisBidProcessor struct {
	client *redis.Client
}

func NewRedisBidProcessor(cfg config.RedisConfig) (*redisBidProcessor, error) {
	resolved, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(resolved.Addr) == "" {
		return nil, errors.New("bidding idempotency requires redis address")
	}

	var tlsConfig *tls.Config
	if resolved.TLS {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	return &redisBidProcessor{
		client: redis.NewClient(&redis.Options{
			Addr:      strings.TrimSpace(resolved.Addr),
			Password:  resolved.Password,
			TLSConfig: tlsConfig,
		}),
	}, nil
}

func (p *redisBidProcessor) PrepareBid(ctx context.Context, cmd prepareBidCommand) (*bidProcessingResult, error) {
	idemTTL := durationOrDefault(cmd.IdempotencyTTL, bidIdempotencyTTL)
	stateTTL := durationOrDefault(cmd.AuctionStateTTL, auctionStateTTL(cmd.AuctionEndsAt))

	result, err := redisPrepareBidScript.Run(ctx, p.client, []string{
		bidIdempotencyRedisKey(cmd.UserID, cmd.RequestID),
		auctionBidStateRedisKey(cmd.AuctionID),
	},
		time.Now().UnixMilli(),
		moneyToCents(cmd.Amount),
		moneyToCents(cmd.CurrentPrice),
		moneyToCents(cmd.MinIncrement),
		cmd.BidID,
		cmd.UserID,
		cmd.AuctionID,
		idemTTL.Milliseconds(),
		stateTTL.Milliseconds(),
	).Result()
	if err != nil {
		return nil, err
	}

	return parsePrepareBidResult(result)
}

func (p *redisBidProcessor) CompleteBid(ctx context.Context, cmd completeBidCommand) error {
	idemTTL := durationOrDefault(cmd.IdempotencyTTL, bidIdempotencyTTL)
	stateTTL := durationOrDefault(cmd.AuctionStateTTL, bidStateMinTTL)

	_, err := redisCompleteBidScript.Run(ctx, p.client, []string{
		bidIdempotencyRedisKey(cmd.UserID, cmd.RequestID),
		auctionBidStateRedisKey(cmd.AuctionID),
	},
		cmd.BidID,
		cmd.Status,
		cmd.Message,
		time.Now().UnixMilli(),
		idemTTL.Milliseconds(),
		stateTTL.Milliseconds(),
	).Result()

	return err
}

func (p *redisBidProcessor) Close() error {
	return p.client.Close()
}

func bidIdempotencyRedisKey(userID, requestID string) string {
	return "user:bid:" + userID + ":" + requestID
}

func auctionBidStateRedisKey(auctionID string) string {
	return "auction:bid:" + auctionID
}

func auctionStateTTL(endsAt time.Time) time.Duration {
	if endsAt.IsZero() {
		return bidStateMinTTL
	}

	ttl := time.Until(endsAt) + bidStateMinTTL
	if ttl < bidStateMinTTL {
		return bidStateMinTTL
	}

	return ttl
}

func durationOrDefault(duration, fallback time.Duration) time.Duration {
	if duration <= 0 {
		return fallback
	}

	return duration
}

func parsePrepareBidResult(result any) (*bidProcessingResult, error) {
	values, ok := result.([]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected redis bid result %T", result)
	}
	if len(values) != 7 {
		return nil, fmt.Errorf("unexpected redis bid result length %d", len(values))
	}

	allowed, err := redisIntValue(values[0])
	if err != nil {
		return nil, err
	}
	amount, err := redisIntValue(values[4])
	if err != nil {
		return nil, err
	}
	required, err := redisIntValue(values[5])
	if err != nil {
		return nil, err
	}
	duplicate, err := redisIntValue(values[6])
	if err != nil {
		return nil, err
	}

	status := redisStringValue(values[1])
	return &bidProcessingResult{
		Allowed:   allowed == 1,
		Duplicate: duplicate == 1,
		Status:    status,
		BidID:     redisStringValue(values[2]),
		Message:   redisStringValue(values[3]),
		Amount:    centsToMoney(amount),
		Required:  centsToMoney(required),
	}, nil
}

func moneyToCents(amount float64) int64 {
	return int64(math.Round(amount * 100))
}

func centsToMoney(cents int64) float64 {
	return float64(cents) / 100
}

func redisStringValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func redisIntValue(value any) (int64, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case float64:
		return int64(v), nil
	case string:
		if v == "" {
			return 0, nil
		}
		return strconv.ParseInt(v, 10, 64)
	case []byte:
		if len(v) == 0 {
			return 0, nil
		}
		return strconv.ParseInt(string(v), 10, 64)
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("unexpected redis integer value %T", value)
	}
}
