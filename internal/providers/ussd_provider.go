package providers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
)

type USSDPaymentProvider struct {
	*BasePaymentProvider
}

func NewUSSDPaymentProvider(db *sql.DB, redis *redis.Client, hsmInstance hsm.HSMInterface) *USSDPaymentProvider {
	return &USSDPaymentProvider{
		BasePaymentProvider: NewBasePaymentProvider(db, redis, hsmInstance),
	}
}

func (p *USSDPaymentProvider) GetPaymentMode() models.PaymentMode {
	return models.PaymentModeUSSD
}

func (p *USSDPaymentProvider) ValidatePayment(ctx context.Context, req *models.PaymentRequest) error {
	log.Printf("[USSDProvider] Validating payment: from=%s, amount=%d", req.FromAccount, req.Amount)

	if req.Metadata == nil || req.Metadata["ussdCode"] == nil {
		log.Printf("[USSDProvider] Validation failed: USSD code is missing")
		return errors.New("USSD code is required")
	}

	ussdCode, ok := req.Metadata["ussdCode"].(string)
	if !ok {
		log.Printf("[USSDProvider] Validation failed: invalid USSD code format")
		return errors.New("invalid USSD code format")
	}

	codeType := "PUSH"
	if req.Metadata["codeType"] != nil {
		if ct, ok := req.Metadata["codeType"].(string); ok {
			codeType = ct
		}
	}

	log.Printf("[USSDProvider] Validating USSD code, type: %s", codeType)
	hashedCode := p.hashCode(ussdCode)

	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("[USSDProvider] Failed to begin transaction: %v", err)
		return err
	}
	defer tx.Rollback()

	var transactionID, userID string
	var amount int64
	var expiresAt time.Time
	var used bool
	err = tx.QueryRowContext(ctx, `
		SELECT transaction_id, user_id, amount, expires_at, used
		FROM ussd_codes
		WHERE code_hash = $1 AND code_type = $2
		FOR UPDATE
	`, hashedCode, codeType).Scan(&transactionID, &userID, &amount, &expiresAt, &used)

	if err == sql.ErrNoRows {
		log.Printf("[USSDProvider] Validation failed: invalid USSD code")
		return errors.New("invalid code")
	}
	if err != nil {
		log.Printf("[USSDProvider] Database error: %v", err)
		return err
	}

	log.Printf("[USSDProvider] USSD code found: used=%v, expires=%v", used, expiresAt)
	if used {
		log.Printf("[USSDProvider] Validation failed: code already used")
		return errors.New("code already used")
	}

	if time.Now().After(expiresAt) {
		log.Printf("[USSDProvider] Validation failed: code expired at %v", expiresAt)
		return errors.New("code expired")
	}

	if userID != req.UserID {
		log.Printf("[USSDProvider] Validation failed: code belongs to different user")
		return errors.New("USSD code does not belong to user")
	}

	if amount != req.Amount {
		log.Printf("[USSDProvider] Validation failed: amount mismatch, expected=%d, got=%d", amount, req.Amount)
		return errors.New("amount mismatch")
	}

	log.Printf("[USSDProvider] Marking USSD code as used")
	_, err = tx.ExecContext(ctx, `
		UPDATE ussd_codes
		SET used = true, used_at = $1
		WHERE code_hash = $2
	`, time.Now(), hashedCode)

	if err != nil {
		log.Printf("[USSDProvider] Failed to mark code as used: %v", err)
		return err
	}

	if err := tx.Commit(); err != nil {
		log.Printf("[USSDProvider] Failed to commit USSD code update: %v", err)
		return err
	}

	if req.Amount <= 0 {
		log.Printf("[USSDProvider] Validation failed: invalid amount=%d", req.Amount)
		return errors.New("amount must be positive")
	}

	var balance int64
	var status string
	err = p.DB.QueryRowContext(ctx, `SELECT balance, status FROM accounts WHERE card_id = $1 OR account_id = $1`, req.FromAccount).Scan(&balance, &status)

	if err != nil {
		if err == sql.ErrNoRows {
			log.Printf("[USSDProvider] Validation failed: account not found: %s", req.FromAccount)
			return errors.New("account not found")
		}
		log.Printf("[USSDProvider] Database error during account check: %v", err)
		return errors.New("validation failed")
	}

	log.Printf("[USSDProvider] Account status: %s, balance: %d", status, balance)
	if status != "ACTIVE" {
		log.Printf("[USSDProvider] Validation failed: account not active, status=%s", status)
		return errors.New("account not active")
	}

	if balance < req.Amount {
		log.Printf("[USSDProvider] Validation failed: insufficient balance, required=%d, available=%d", req.Amount, balance)
		return errors.New("insufficient balance")
	}

	log.Printf("[USSDProvider] Validation passed")
	return nil
}

func (p *USSDPaymentProvider) ProcessPayment(ctx context.Context, req *models.PaymentRequest) (*models.PaymentResponse, error) {
	log.Printf("[USSDProvider] Starting payment processing: txID=%s", req.TransactionID)

	if err := p.ValidatePayment(ctx, req); err != nil {
		log.Printf("[USSDProvider] Payment validation failed: %v", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       err.Error(),
			PaymentMode:   models.PaymentModeUSSD,
			Timestamp:     time.Now(),
		}, err
	}

	log.Printf("[USSDProvider] Beginning database transaction")
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("[USSDProvider] Failed to begin transaction: %v", err)
		return nil, err
	}
	defer tx.Rollback()

	log.Printf("[USSDProvider] Debiting account: %s, amount: %d", req.FromAccount, req.Amount)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance - $1 WHERE card_id = $2 OR account_id = $2`, req.Amount, req.FromAccount)
	if err != nil {
		log.Printf("[USSDProvider] Failed to debit account: %v", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Transfer Failed",
			PaymentMode:   models.PaymentModeUSSD,
			Timestamp:     time.Now(),
		}, err
	}

	log.Printf("[USSDProvider] Crediting account: %s, amount: %d", req.ToAccount, req.Amount)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance + $1 WHERE card_id = $2 OR account_id = $2`, req.Amount, req.ToAccount)
	if err != nil {
		log.Printf("[USSDProvider] Failed to credit account: %v", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Transfer Failed",
			PaymentMode:   models.PaymentModeUSSD,
			Timestamp:     time.Now(),
		}, err
	}

	log.Printf("[USSDProvider] Inserting transaction record")
	metadata, _ := json.Marshal(req.Metadata)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO transactions 
		(transaction_id, from_card_id, to_card_id, amount, currency, narration, type, payment_mode, status, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'DEBIT', 'USSD', 'COMPLETED', $7, NOW())
	`, req.TransactionID, req.FromAccount, req.ToAccount, req.Amount, req.Currency, req.Narration, metadata)

	if err != nil {
		log.Printf("[USSDProvider] Failed to insert transaction: %v", err)
		return nil, err
	}

	log.Printf("[USSDProvider] Committing transaction")
	if err := tx.Commit(); err != nil {
		log.Printf("[USSDProvider] Failed to commit transaction: %v", err)
		return nil, err
	}

	log.Printf("[USSDProvider] Payment Successful")
	return &models.PaymentResponse{
		Success:       true,
		TransactionID: req.TransactionID,
		Status:        "COMPLETED",
		Message:       "USSD Payment Successful",
		PaymentMode:   models.PaymentModeUSSD,
		Timestamp:     time.Now(),
	}, nil
}

func (p *USSDPaymentProvider) hashCode(code string) string {
	hash := sha256.Sum256([]byte(code))
	for i := 1; i < 10000; i++ {
		hash = sha256.Sum256(hash[:])
	}
	return hex.EncodeToString(hash[:])
}

func (p *USSDPaymentProvider) HandlePayment(w http.ResponseWriter, r *http.Request) {
	p.BasePaymentProvider.HandlePaymentRequest(w, r, p)
}
