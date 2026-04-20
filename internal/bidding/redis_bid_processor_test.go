package bidding

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePrepareBidResultAccepted(t *testing.T) {
	result, err := parsePrepareBidResult([]interface{}{
		int64(1),
		"processing",
		"bid-1",
		"teklif isleniyor",
		int64(170000),
		int64(160000),
		int64(0),
	})

	require.NoError(t, err)
	assert.True(t, result.Allowed)
	assert.False(t, result.Duplicate)
	assert.Equal(t, "processing", result.Status)
	assert.Equal(t, "bid-1", result.BidID)
	assert.Equal(t, 1700.00, result.Amount)
	assert.Equal(t, 1600.00, result.Required)
}

func TestParsePrepareBidResultDuplicate(t *testing.T) {
	result, err := parsePrepareBidResult([]interface{}{
		int64(0),
		"accepted",
		"bid-1",
		"teklif kabul edildi",
		[]byte("170000"),
		int64(0),
		int64(1),
	})

	require.NoError(t, err)
	assert.False(t, result.Allowed)
	assert.True(t, result.Duplicate)
	assert.Equal(t, "accepted", result.Status)
	assert.Equal(t, "bid-1", result.BidID)
	assert.Equal(t, "teklif kabul edildi", result.Message)
}

func TestMoneyToCents(t *testing.T) {
	assert.Equal(t, int64(170055), moneyToCents(1700.55))
	assert.Equal(t, 1700.55, centsToMoney(170055))
}
