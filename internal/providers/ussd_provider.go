package providers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
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
	slog.Info("ussd.validate", "from", req.FromAccount, "amount", req.Amount)

	if req.Metadata == nil || req.Metadata["ussdCode"] == nil {
		return errors.New("USSD code is required")
	}

	ussdCode, ok := req.Metadata["ussdCode"].(string)
	if !ok {
		return errors.New("invalid USSD code format")
	}

	codeType := "PUSH"
	if req.Metadata["codeType"] != nil {
		if ct, ok := req.Metadata["codeType"].(string); ok {
			codeType = ct
		}
	}

	hashedCode := p.hashCode(ussdCode)

	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("ussd.validate.tx_begin_failed", "error", err)
		return err
	}
	defer tx.Rollback()

	var transactionId, userID string
	var amount int64
	var expiresAt time.Time
	var used bool
	err = tx.QueryRowContext(ctx, `
		SELECT transaction_id, user_id, amount, expires_at, used
		FROM ussd_codes
		WHERE code_hash = $1 AND code_type = $2
		FOR UPDATE
	`, hashedCode, codeType).Scan(&transactionId, &userID, &amount, &expiresAt, &used)

	if err == sql.ErrNoRows {
		return errors.New("invalid code")
	}
	if err != nil {
		slog.Error("ussd.validate.db_error", "error", err)
		return err
	}

	if used {
		return errors.New("code already used")
	}

	if time.Now().After(expiresAt) {
		return errors.New("code expired")
	}

	if userID != req.UserID {
		return errors.New("USSD code does not belong to user")
	}

	if amount != req.Amount {
		return errors.New("amount mismatch")
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE ussd_codes
		SET used = true, used_at = $1
		WHERE code_hash = $2
	`, time.Now(), hashedCode)

	if err != nil {
		slog.Error("ussd.validate.mark_used_failed", "error", err)
		return err
	}

	if err := tx.Commit(); err != nil {
		slog.Error("ussd.validate.commit_failed", "error", err)
		return err
	}

	if req.Amount <= 0 {
		return errors.New("amount must be positive")
	}

	var balance int64
	var status string
	err = p.DB.QueryRowContext(ctx, `SELECT balance, status FROM accounts WHERE account_id = $1`, req.FromAccount).Scan(&balance, &status)

	if err != nil {
		if err == sql.ErrNoRows {
			return errors.New("account not found")
		}
		slog.Error("ussd.validate.account_db_error", "error", err)
		return errors.New(string(utils.ValidationError))
	}

	if status != "ACTIVE" {
		return errors.New("account not active")
	}

	if balance < req.Amount {
		return errors.New("insufficient balance")
	}

	return nil
}

func (p *USSDPaymentProvider) ProcessPayment(ctx context.Context, req *models.PaymentRequest) (*models.PaymentResponse, error) {
	slog.Info("ussd.process.start", "tx_id", req.TransactionID)

	if err := p.ValidatePayment(ctx, req); err != nil {
		slog.Warn("ussd.process.validation_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       err.Error(),
			PaymentMode:   models.PaymentModeUSSD,
			Timestamp:     time.Now(),
		}, err
	}

	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("ussd.process.tx_begin_failed", "error", err)
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance - $1 WHERE account_id = $2`, req.Amount, req.FromAccount)
	if err != nil {
		slog.Error("ussd.process.debit_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Transfer Failed",
			PaymentMode:   models.PaymentModeUSSD,
			Timestamp:     time.Now(),
		}, err
	}

	_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance + $1 WHERE account_id = $2`, req.Amount, req.BeneficiaryAccountNumber)
	if err != nil {
		slog.Error("ussd.process.credit_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Transfer Failed",
			PaymentMode:   models.PaymentModeUSSD,
			Timestamp:     time.Now(),
		}, err
	}

	metadata, _ := json.Marshal(req.Metadata)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO transactions 
		(transaction_id, debit_id, credit_id, amount, currency, narration, type, payment_mode, status, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'DEBIT', 'USSD', 'COMPLETED', $7, NOW())
	`, req.TransactionID, req.FromAccount, req.BeneficiaryAccountNumber, req.Amount, req.Currency, req.Narration, metadata)

	if err != nil {
		slog.Error("ussd.process.insert_failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		slog.Error("ussd.process.commit_failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}

	slog.Info("ussd.process.success", "tx_id", req.TransactionID)
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
