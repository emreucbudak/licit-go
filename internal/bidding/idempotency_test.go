package bidding

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateIdempotencyKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "valid uuid", key: "550e8400-e29b-41d4-a716-446655440000", want: true},
		{name: "valid uuid with spaces", key: " 550e8400-e29b-41d4-a716-446655440000 ", want: true},
		{name: "empty", key: "", want: false},
		{name: "not uuid", key: "request-123", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, validateIdempotencyKey(normalizeIdempotencyKey(tt.key)))
		})
	}
}

func TestBidRedisKeys(t *testing.T) {
	assert.Equal(t, "user:bid:user-1:550e8400-e29b-41d4-a716-446655440000", bidIdempotencyRedisKey("user-1", "550e8400-e29b-41d4-a716-446655440000"))
	assert.Equal(t, "auction:bid:auction-1", auctionBidStateRedisKey("auction-1"))
}
