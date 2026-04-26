package models

import (
	"time"
)

type LedgerEntry struct {
	ID            int         `json:"id" db:"id"`
	TransactionID string      `json:"transaction_id" db:"transaction_id"`
	AccountID     string      `json:"account_id" db:"account_id"`
	Amount        int64       `json:"amount" db:"amount"`         // in cents
	EntryType     PaymentType `json:"entry_type" db:"entry_type"` // DEBIT or CREDIT
	Balance       int64       `json:"balance" db:"balance"`
	CreatedAt     time.Time   `json:"created_at" db:"created_at"`
}

type Account struct {
	ID        string    `json:"id" db:"id"`
	Balance   int64     `json:"balance" db:"balance"`
	Version   int       `json:"version" db:"version"` // for optimistic locking
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}
