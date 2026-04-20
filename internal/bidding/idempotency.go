package bidding

import (
	"strings"

	"github.com/google/uuid"
)

const idempotencyKeyHeader = "Idempotency-Key"

func normalizeIdempotencyKey(key string) string {
	return strings.TrimSpace(key)
}

func validateIdempotencyKey(key string) bool {
	key = normalizeIdempotencyKey(key)
	if key == "" {
		return false
	}

	_, err := uuid.Parse(key)
	return err == nil
}
