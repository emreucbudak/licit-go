package messaging

// NATS subject constants for inter-service communication.
// .NET tarafı ile aynı subject'leri kullanarak haberleşme sağlanır.

const (
	// Bidding events
	SubjectBidPlaced   = "licit.bid.placed"
	SubjectBidAccepted = "licit.bid.accepted"
	SubjectBidRejected = "licit.bid.rejected"

	// Auction lifecycle events
	SubjectAuctionStarted  = "licit.auction.started"
	SubjectAuctionEnded    = "licit.auction.ended"
	SubjectAuctionUpdate   = "licit.auction.update"
	SubjectAuctionCreated  = "licit.auction.created"

	// Payment events (request-reply pattern)
	SubjectPaymentValidate = "licit.payment.validate"
	SubjectPaymentReserve  = "licit.payment.reserve"
	SubjectPaymentRelease  = "licit.payment.release"
	SubjectPaymentCharge   = "licit.payment.charge"
)
