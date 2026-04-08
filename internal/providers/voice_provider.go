package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
)

type VoicePaymentProvider struct {
	*BasePaymentProvider
}

func NewVoicePaymentProvider(db *sql.DB, redis *redis.Client, hsmInstance hsm.HSMInterface) *VoicePaymentProvider {
	return &VoicePaymentProvider{
		BasePaymentProvider: NewBasePaymentProvider(db, redis, hsmInstance),
	}
}

func (p *VoicePaymentProvider) GetPaymentMode() models.PaymentMode {
	return models.PaymentModeVoice
}

func (p *VoicePaymentProvider) ValidatePayment(ctx context.Context, req *models.PaymentRequest) error {
	slog.Info("voice.validate", "from", req.FromAccount, "amount", req.Amount)

	if req.Metadata == nil || req.Metadata["voiceCommand"] == nil {
		return errors.New("voice command is required")
	}

	voiceCommand, ok := req.Metadata["voiceCommand"].(string)
	if !ok {
		return errors.New("invalid voice command format")
	}

	if !p.isValidPaymentCommand(voiceCommand) {
		return errors.New("invalid payment command")
	}

	if req.Amount <= 0 {
		return errors.New("amount must be positive")
	}

	var balance int64
	var status string
	err := p.DB.QueryRowContext(ctx, `SELECT balance, status FROM accounts WHERE account_id = $1`, req.FromAccount).Scan(&balance, &status)

	if err != nil {
		if err == sql.ErrNoRows {
			return errors.New("account not found")
		}
		slog.Error("voice.validate.db_error", "error", err)
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

func (p *VoicePaymentProvider) ProcessPayment(ctx context.Context, req *models.PaymentRequest) (*models.PaymentResponse, error) {
	slog.Info("voice.process.start", "tx_id", req.TransactionID)

	if err := p.ValidatePayment(ctx, req); err != nil {
		slog.Warn("voice.process.validation_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       utils.ResponseMessage(err.Error()),
			PaymentMode:   models.PaymentModeVoice,
			Timestamp:     time.Now(),
		}, err
	}

	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("voice.process.tx_begin_failed", "error", err)
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance - $1 WHERE account_id = $2`, req.Amount, req.FromAccount)
	if err != nil {
		slog.Error("voice.process.debit_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Transfer failed",
			PaymentMode:   models.PaymentModeVoice,
			Timestamp:     time.Now(),
		}, err
	}

	_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance + $1 WHERE account_id = $2`, req.Amount, req.BeneficiaryAccountNumber)
	if err != nil {
		slog.Error("voice.process.credit_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Transfer failed",
			PaymentMode:   models.PaymentModeVoice,
			Timestamp:     time.Now(),
		}, err
	}

	metadata, _ := json.Marshal(req.Metadata)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO transactions 
		(transaction_id, debit_id, credit_id, amount, currency, narration, type, payment_mode, status, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'DEBIT', 'VOICE', 'COMPLETED', $7, NOW())
	`, req.TransactionID, req.FromAccount, req.BeneficiaryAccountNumber, req.Amount, req.Currency, req.Narration, metadata)

	if err != nil {
		slog.Error("voice.process.insert_failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		slog.Error("voice.process.commit_failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}

	slog.Info("voice.process.success", "tx_id", req.TransactionID)
	return &models.PaymentResponse{
		Success:       true,
		TransactionID: req.TransactionID,
		Status:        "COMPLETED",
		Message:       "Voice payment successful",
		PaymentMode:   models.PaymentModeVoice,
		Timestamp:     time.Now(),
	}, nil
}

func (p *VoicePaymentProvider) isValidPaymentCommand(command string) bool {
	command = strings.ToLower(strings.TrimSpace(command))
	validCommands := []string{"pay", "send", "transfer", "payment"}
	for _, valid := range validCommands {
		if strings.Contains(command, valid) {
			return true
		}
	}
	return false
}

func (p *VoicePaymentProvider) HandlePayment(w http.ResponseWriter, r *http.Request) {
	p.BasePaymentProvider.HandlePaymentRequest(w, r, p)
}
