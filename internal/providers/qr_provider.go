package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
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
	slog.Info("qr.validate", "from", req.FromAccount, "amount", req.Amount)

	if req.Metadata == nil || req.Metadata["qrCode"] == nil {
		return errors.New("QR Code Required")
	}

	qrCode, ok := req.Metadata["qrCode"].(string)
	if !ok {
		return errors.New("Invalid QR Code Format")
	}

	key := fmt.Sprintf("qr:%s", qrCode)
	data, err := p.Redis.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return errors.New("invalid or expired QR code")
	}
	if err != nil {
		slog.Error("qr.validate.redis_error", "error", err)
		return err
	}

	var qrData map[string]any
	if err := json.Unmarshal(data, &qrData); err != nil {
		slog.Error("qr.validate.unmarshal_failed", "error", err)
		return err
	}

	if qrData["userId"] != req.UserID {
		return errors.New("QR code does not belong to user")
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
		slog.Error("qr.validate.db_error", "error", err)
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

func (p *QRPaymentProvider) ProcessPayment(ctx context.Context, req *models.PaymentRequest) (*models.PaymentResponse, error) {
	slog.Info("qr.process.start", "tx_id", req.TransactionID)

	if err := p.ValidatePayment(ctx, req); err != nil {
		slog.Warn("qr.process.validation_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       err.Error(),
			PaymentMode:   models.PaymentModeQR,
			Timestamp:     time.Now(),
		}, err
	}

	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("qr.process.tx_begin_failed", "error", err)
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance - $1 WHERE account_id = $2`, req.Amount, req.FromAccount)
	if err != nil {
		slog.Error("qr.process.debit_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Transfer failed",
			PaymentMode:   models.PaymentModeQR,
			Timestamp:     time.Now(),
		}, err
	}

	_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance + $1 WHERE account_id = $2`, req.Amount, req.BeneficiaryAccountNumber)
	if err != nil {
		slog.Error("qr.process.credit_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Transfer failed",
			PaymentMode:   models.PaymentModeQR,
			Timestamp:     time.Now(),
		}, err
	}

	metadata, _ := json.Marshal(req.Metadata)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO transactions 
		(transaction_id, debit_id, credit_id, amount, currency, narration, type, payment_mode, status, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'DEBIT', 'QR', 'COMPLETED', $7, NOW())
	`, req.TransactionID, req.FromAccount, req.BeneficiaryAccountNumber, req.Amount, req.Currency, req.Narration, metadata)

	if err != nil {
		slog.Error("qr.process.insert_failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		slog.Error("qr.process.commit_failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}

	p.Redis.Del(ctx, fmt.Sprintf("qr:%s", req.Metadata["qrCode"]))
	slog.Info("qr.process.success", "tx_id", req.TransactionID)
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
