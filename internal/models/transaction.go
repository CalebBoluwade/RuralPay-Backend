package models

import (
	"time"

	"github.com/ruralpay/backend/internal/utils"
)

type PaymentMode string
type PaymentType string
type TransactionStatus string

const (
	PaymentModeCard         PaymentMode = "CARD"
	PaymentModeQR           PaymentMode = "QR"
	PaymentModeBankTransfer PaymentMode = "BANK_TRANSFER"
	PaymentModeUSSD         PaymentMode = "USSD"
	PaymentModeVoice        PaymentMode = "VOICE"
	PaymentModeAirtime      PaymentMode = "AIRTIME"
	PaymentModeData         PaymentMode = "DATA"
)

const (
	DebitPayment      PaymentType = "DEBIT"
	CreditPayment     PaymentType = "CREDIT"
	WithdrawalPayment PaymentType = "WITHDRAWAL"
)

const (
	TransactionStatusPending       TransactionStatus = "PENDING"
	TransactionStatusSuccess       TransactionStatus = "COMPLETED"
	TransactionStatusFailed        TransactionStatus = "FAILED"
	TransactionStatusCancelled     TransactionStatus = "CANCELLED"
	TransactionSettlementFailed    TransactionStatus = "FAILED_SETTLEMENT"
	TransactionStatusISOCONVFailed TransactionStatus = "FAILED_ISO_CONVERSION"
)

type USSDCodeType string

const (
	PushPayment USSDCodeType = "PUSH"
	PullPayment USSDCodeType = "PULL"
)

// Metadata type for JSONB fields
type Metadata map[string]any

type PaymentRequest struct {
	TransactionID            string      `json:"transactionId" validate:"required"`
	UserID                   string      `json:"userId"`
	FromAccount              string      `json:"fromAccount"`
	BeneficiaryAccountNumber string      `json:"beneficiaryAccountNumber"`
	BeneficiaryAccountName   string      `json:"beneficiaryAccountName"`
	BeneficiaryBankName      string      `json:"beneficiaryBankName"`
	BeneficiaryBankCode      string      `json:"beneficiaryBankCode"`
	Amount                   int64       `json:"amount"`
	Currency                 string      `json:"currency"`
	Metadata                 Metadata    `json:"metadata"`
	Narration                string      `json:"narration"`
	TxType                   PaymentType `json:"txType"`
	PaymentMode              PaymentMode `json:"paymentMode"`
	SaveBeneficiary          bool        `json:"saveBeneficiary"`
	OneTimeCode              string      `json:"oneTimeCode" validate:"required,len=8,numeric"`
	TwoFAType                string      `json:"twoFAType" validate:"required,oneof=BYPASS OTP BIOMETRIC"`
	Location                 *Location   `json:"location,omitempty"`
	IPAddress                string      `json:"IPAddress,omitempty"`
}

type PaymentResponse struct {
	Success       bool                  `json:"success"`
	TransactionID string                `json:"transactionId"`
	Reference     string                `json:"reference"`
	Status        TransactionStatus     `json:"status"`
	Message       utils.ResponseMessage `json:"message"`
	Metadata      Metadata              `json:"metadata"`
	PaymentMode   PaymentMode           `json:"paymentMode"`
	Timestamp     time.Time             `json:"timestamp"`
}

// Location represents geographical location data
type Location struct {
	Latitude  float64 `json:"latitude" db:"latitude"`
	Longitude float64 `json:"longitude" db:"longitude"`
	Accuracy  float64 `json:"accuracy" db:"accuracy"`
	Address   string  `json:"address" db:"address"`
}

type AirtimeDataRequest struct {
	TransactionID string      `json:"transactionId"`
	DebitAccount  string      `json:"debitAccount" validate:"required"`
	PhoneNumber   string      `json:"beneficiaryPhoneNumber" validate:"required"`
	Network       string      `json:"network" validate:"required"`
	Service       string      `json:"service" validate:"required,oneof=AIRTIME DATA"`
	DataPlan      string      `json:"dataPlanId,omitempty"`
	Amount        int64       `json:"amount" validate:"required,gt=0"`
	Narration     string      `json:"narration,omitempty"`
	PaymentMode   PaymentMode `json:"paymentMode"`
	Voucher       Voucher     `json:"voucher,omitempty"`
	OneTimeCode   string      `json:"oneTimeCode" validate:"required,len=8,numeric"`
	TwoFAType     string      `json:"twoFAType" validate:"required,oneof=BYPASS,OTP,BIOMETRIC"`
	Location      *Location   `json:"location,omitempty"`
}

// TransactionRecord represents a payment transaction
type TransactionRecord struct {
	ID            int               `json:"id" db:"id"`
	TransactionID string            `json:"transactionId" db:"transaction_id"`
	Reference     string            `json:"reference" db:"reference"`
	FromAccountID string            `json:"from_account_id" db:"from_account_id"`
	ToAccountID   string            `json:"to_account_id" db:"to_account_id"`
	Amount        int64             `json:"amount" db:"amount"`
	Fee           int64             `json:"fee" db:"fee"`
	TotalAmount   int64             `json:"total_amount" db:"total_amount"`
	Currency      string            `json:"currency" db:"currency"`
	Status        TransactionStatus `json:"status" db:"status"`
	Type          string            `json:"type" db:"type"`
	Signature     string            `json:"signature" db:"signature"`
	DeviceID      string            `json:"device_id" db:"device_id"`
	Location      Location          `json:"location" db:"location"`
	SyncStatus    string            `json:"sync_status" db:"sync_status"`
	ErrorMessage  string            `json:"error_message" db:"error_message"`
	Metadata      Metadata          `json:"metadata" db:"metadata"`
	ToBankCode    string            `json:"to_bank_code,omitempty"`
	CreatedAt     time.Time         `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at" db:" updated_at"`
	SettledAt     *time.Time        `json:"settled_at" db:"settled_at"`
	ProcessedAt   *time.Time        `json:"processed_at" db:"processed_at"`
}

type AuditEvent struct {
	Timestamp time.Time       `json:"timestamp"`
	EventType string          `json:"event_type"`
	TxRequest *PaymentRequest `json:"request"`
	Error     string          `json:"error,omitempty"`
	Details   map[string]any  `json:"details"`
}
