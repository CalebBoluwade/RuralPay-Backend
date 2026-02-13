package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
)

type QRPaymentProvider struct {
	*BasePaymentProvider
}

func NewQRPaymentProvider(db *sql.DB, redis *redis.Client, hsmInstance hsm.HSMInterface) *QRPaymentProvider {
	return &QRPaymentProvider{
		BasePaymentProvider: NewBasePaymentProvider(db, redis, hsmInstance),
	}
}

func (p *QRPaymentProvider) GetPaymentMode() models.PaymentMode {
	return models.PaymentModeQR
}

func (p *QRPaymentProvider) ValidatePayment(ctx context.Context, req *models.PaymentRequest) error {
	log.Printf("[QRProvider] Validating payment: from=%s, amount=%d", req.FromAccount, req.Amount)

	if req.Metadata == nil || req.Metadata["qrCode"] == nil {
		log.Printf("[QRProvider] Validation failed: QR code is missing")
		return errors.New("QR code is required")
	}

	qrCode, ok := req.Metadata["qrCode"].(string)
	if !ok {
		log.Printf("[QRProvider] Validation failed: invalid QR code format")
		return errors.New("invalid QR code format")
	}

	log.Printf("[QRProvider] Validating QR code: %s", qrCode)
	key := fmt.Sprintf("qr:%s", qrCode)
	data, err := p.Redis.Get(ctx, key).Bytes()
	if err == redis.Nil {
		log.Printf("[QRProvider] Validation failed: QR code not found or expired")
		return errors.New("invalid or expired QR code")
	}
	if err != nil {
		log.Printf("[QRProvider] Redis error: %v", err)
		return err
	}

	var qrData map[string]any
	if err := json.Unmarshal(data, &qrData); err != nil {
		log.Printf("[QRProvider] Failed to unmarshal QR data: %v", err)
		return err
	}

	if qrData["userId"] != req.UserID {
		log.Printf("[QRProvider] Validation failed: QR code belongs to different user")
		return errors.New("QR code does not belong to user")
	}

	if req.Amount <= 0 {
		log.Printf("[QRProvider] Validation failed: invalid amount=%d", req.Amount)
		return errors.New("amount must be positive")
	}

	var balance int64
	var status string
	err = p.DB.QueryRowContext(ctx, `SELECT balance, status FROM accounts WHERE card_id = $1 OR account_id = $1`, req.FromAccount).Scan(&balance, &status)

	if err != nil {
		if err == sql.ErrNoRows {
			log.Printf("[QRProvider] Validation failed: account not found: %s", req.FromAccount)
			return errors.New("account not found")
		}
		log.Printf("[QRProvider] Database error during validation: %v", err)
		return errors.New("validation failed")
	}

	log.Printf("[QRProvider] Account status: %s, balance: %d", status, balance)
	if status != "ACTIVE" {
		log.Printf("[QRProvider] Validation failed: account not active, status=%s", status)
		return errors.New("account not active")
	}

	if balance < req.Amount {
		log.Printf("[QRProvider] Validation failed: insufficient balance, required=%d, available=%d", req.Amount, balance)
		return errors.New("insufficient balance")
	}

	log.Printf("[QRProvider] Validation passed")
	return nil
}

func (p *QRPaymentProvider) ProcessPayment(ctx context.Context, req *models.PaymentRequest) (*models.PaymentResponse, error) {
	log.Printf("[QRProvider] Starting payment processing: txID=%s", req.TransactionID)

	if err := p.ValidatePayment(ctx, req); err != nil {
		log.Printf("[QRProvider] Payment validation failed: %v", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       err.Error(),
			PaymentMode:   models.PaymentModeQR,
			Timestamp:     time.Now(),
		}, err
	}

	log.Printf("[QRProvider] Beginning database transaction")
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("[QRProvider] Failed to begin transaction: %v", err)
		return nil, err
	}
	defer tx.Rollback()

	log.Printf("[QRProvider] Debiting account: %s, amount: %d", req.FromAccount, req.Amount)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance - $1 WHERE card_id = $2 OR account_id = $2`, req.Amount, req.FromAccount)
	if err != nil {
		log.Printf("[QRProvider] Failed to debit account: %v", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Transfer failed",
			PaymentMode:   models.PaymentModeQR,
			Timestamp:     time.Now(),
		}, err
	}

	log.Printf("[QRProvider] Crediting account: %s, amount: %d", req.ToAccount, req.Amount)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance + $1 WHERE card_id = $2 OR account_id = $2`, req.Amount, req.ToAccount)
	if err != nil {
		log.Printf("[QRProvider] Failed to credit account: %v", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Transfer failed",
			PaymentMode:   models.PaymentModeQR,
			Timestamp:     time.Now(),
		}, err
	}

	log.Printf("[QRProvider] Inserting transaction record")
	metadata, _ := json.Marshal(req.Metadata)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO transactions 
		(transaction_id, from_card_id, to_card_id, amount, currency, narration, type, payment_mode, status, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'DEBIT', 'QR', 'COMPLETED', $7, NOW())
	`, req.TransactionID, req.FromAccount, req.ToAccount, req.Amount, req.Currency, req.Narration, metadata)

	if err != nil {
		log.Printf("[QRProvider] Failed to insert transaction: %v", err)
		return nil, err
	}

	log.Printf("[QRProvider] Committing transaction")
	if err := tx.Commit(); err != nil {
		log.Printf("[QRProvider] Failed to commit transaction: %v", err)
		return nil, err
	}

	log.Printf("[QRProvider] Deleting used QR code")
	p.Redis.Del(ctx, fmt.Sprintf("qr:%s", req.Metadata["qrCode"]))

	log.Printf("[QRProvider] Payment successful")
	return &models.PaymentResponse{
		Success:       true,
		TransactionID: req.TransactionID,
		Status:        "COMPLETED",
		Message:       "QR payment successful",
		PaymentMode:   models.PaymentModeQR,
		Timestamp:     time.Now(),
	}, nil
}

func (p *QRPaymentProvider) HandlePayment(w http.ResponseWriter, r *http.Request) {
	p.BasePaymentProvider.HandlePaymentRequest(w, r, p)
}
