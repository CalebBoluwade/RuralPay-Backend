package models

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"

	"github.com/moov-io/iso8583"
	"github.com/ruralpay/backend/internal/hsm"
)

type BINResponse struct {
	BIN        string `json:"bin"`
	Scheme     string `json:"scheme"`     // Visa, Mastercard, Verve
	IssuerBank string `json:"issuerBank"` // GTBank, Zenith, etc.
	Type       string `json:"type"`       // Debit, Credit
	Country    string `json:"country"`    // NG, US
	Currency   string `json:"currency"`   // NGN, USD
	Source     string `json:"source"`     // "internal" or "external"
}

type CardInfo struct {
	// BIN           string `json:"bin"`
	// Scheme        string `json:"scheme"` // Visa, Mastercard, Verve
	EncryptedPAN  string `json:"PAN"`
	EncryptedPIN  string `json:"PIN"`
	ExpiryDate    string `json:"expiryDate"`
	ATC           int    `json:"ATC"`
	CVR           string `json:"CVR"`
	IssuerAppData string `json:"issuerAppData"`
	CountryCode   string `json:"countryCode"`
}

type CardPaymentRequest struct {
	TransactionID   string      `json:"transactionId"`
	TransactionDate int64       `json:"transactionDate"`
	MerchantID      int         `json:"merchantId" validate:"required,gt=0"`
	Amount          int64       `json:"amount" validate:"required,gt=0"`
	PaymentMode     PaymentMode `json:"paymentMode"`
	CardInfo        CardInfo    `json:"cardInfo"`
	TxType          string      `json:"txType"`
	Location        *Location   `json:"location,omitempty"`
}

type ISO8583ServiceImpl struct {
	db  *sql.DB
	hsm hsm.HSMInterface
}

type ISO8583Service interface {
	BuildISO8583Message(cardReq *CardPaymentRequest) ([]byte, error)
	ProcessMessage(ctx context.Context, rawMsg []byte) ([]byte, error)
	BuildAuthorizationResponse(msg *iso8583.Message, responseCode string) (*iso8583.Message, error)
	BuildFinancialResponse(msg *iso8583.Message, responseCode string) (*iso8583.Message, error)
}

type AuthorizationRequest struct {
	PAN              string
	ProcessingCode   string
	Amount           int64
	TransmissionTime time.Time
	STAN             string
	MerchantType     string
	AcquirerID       string
	ForwardingID     string
	Track2Data       string
	RRN              string
	TerminalID       string
	MerchantID       string
	AdditionalData   string
}

type AuthorizationResponse struct {
	ResponseCode      string
	AuthorizationCode string
	TransactionID     string
	STAN              string
	RRN               string
	ResponseMessage   string
	Timestamp         time.Time
}

type FinancialRequest struct {
	PAN               string
	ProcessingCode    string
	Amount            int64
	TransmissionTime  time.Time
	STAN              string
	MerchantType      string
	AcquirerID        string
	RRN               string
	AuthorizationCode string
	TerminalID        string
	MerchantID        string
	AdditionalData    string
}

type FinancialResponse struct {
	ResponseCode    string
	TransactionID   string
	STAN            string
	RRN             string
	ResponseMessage string
	Timestamp       time.Time
}

type ReversalRequest struct {
	PAN              string
	Amount           int64
	TransmissionTime time.Time
	STAN             string
	OriginalSTAN     string
	OriginalRRN      string
	AcquirerID       string
	TerminalID       string
	MerchantID       string
}

type ReversalResponse struct {
	ResponseCode    string
	STAN            string
	RRN             string
	ResponseMessage string
	Timestamp       time.Time
}

// CardSyncRequest represents card sync data from mobile
type CardSyncRequest struct {
	CardID       string              `json:"card_id" binding:"required"`
	Balance      float64             `json:"balance" binding:"required"`
	TxCounter    int                 `json:"tx_counter" binding:"required"`
	LastSyncAt   time.Time           `json:"last_sync_at"`
	Transactions []TransactionRecord `json:"transactions"`
	Signature    string              `json:"signature" binding:"required"`
}

// CardSyncResponse represents server response to card sync
type CardSyncResponse struct {
	CardID         string                `json:"card_id"`
	Balance        float64               `json:"balance"`
	Currency       string                `json:"currency"`
	LastSyncAt     time.Time             `json:"last_sync_at"`
	PendingUpdates []TransactionUpdate   `json:"pending_updates,omitempty"`
	Conflicts      []TransactionConflict `json:"conflicts,omitempty"`
	DailyLimit     float64               `json:"daily_limit"`
	DailySpent     float64               `json:"daily_spent"`
	IsActive       bool                  `json:"is_active"`
}

// TransactionUpdate represents pending transaction updates
type TransactionUpdate struct {
	TransactionID string     `json:"transaction_id"`
	Status        string     `json:"status"`
	SettledAt     *time.Time `json:"settled_at,omitempty"`
}

// TransactionConflict represents conflicting transactions
type TransactionConflict struct {
	LocalTransaction  TransactionRecord `json:"local_transaction"`
	ServerTransaction TransactionRecord `json:"server_transaction"`
	Resolution        string            `json:"resolution"` // "keep_local", "use_server", "manual"
}

// CardIssueRequest represents new card issuance
type CardIssueRequest struct {
	UserID         int     `json:"user_id" binding:"required"`
	CardType       string  `json:"card_type"`
	InitialBalance float64 `json:"initial_balance"`
	MaxBalance     float64 `json:"max_balance"`
}

// CardStatus represents card status
const (
	CardStatusActive   = "active"
	CardStatusInactive = "inactive"
	CardStatusBlocked  = "blocked"
	CardStatusLost     = "lost"
	CardStatusExpired  = "expired"
)

// Value implements driver.Valuer for Metadata
func (m Metadata) Value() (driver.Value, error) {
	if m == nil {
		return nil, nil
	}
	return json.Marshal(m)
}

// Scan implements sql.Scanner for Metadata
func (m *Metadata) Scan(value any) error {
	if value == nil {
		*m = nil
		return nil
	}

	b, ok := value.([]byte)
	if !ok {
		return errors.New("type assertion to []byte failed")
	}

	return json.Unmarshal(b, m)
}
