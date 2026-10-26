package model

// ManagedReservation is a root-issued, single-use permit bound to the actual
// wire body. Retaining the token index also prevents bypass via a missing nonce.
type ManagedReservation struct {
	Nonce        string `gorm:"primaryKey;type:varchar(32)"`
	TokenID      int    `gorm:"index;not null"`
	Model        string `gorm:"type:varchar(256);not null"`
	BodySHA256   string `gorm:"type:varchar(64);not null"`
	InputTokens  int
	OutputTokens int
	NativeQuota  int
	ExpiresAt    int64
	Consumed     bool
}
