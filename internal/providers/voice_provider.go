package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
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
	log.Printf("[VoiceProvider] Validating payment: from=%s, amount=%d", req.FromAccount, req.Amount)

	if req.Metadata == nil || req.Metadata["voiceCommand"] == nil {
		log.Printf("[VoiceProvider] Validation failed: voice command is missing")
		return errors.New("voice command is required")
	}

	voiceCommand, ok := req.Metadata["voiceCommand"].(string)
	if !ok {
		log.Printf("[VoiceProvider] Validation failed: invalid voice command format")
		return errors.New("invalid voice command format")
	}

	log.Printf("[VoiceProvider] Validating voice command: %s", voiceCommand)
	if !p.isValidPaymentCommand(voiceCommand) {
		log.Printf("[VoiceProvider] Validation failed: invalid payment command")
		return errors.New("invalid payment command")
	}

	if req.Amount <= 0 {
		log.Printf("[VoiceProvider] Validation failed: invalid amount=%d", req.Amount)
		return errors.New("amount must be positive")
	}

	var balance int64
	var status string
	err := p.DB.QueryRowContext(ctx, `SELECT balance, status FROM accounts WHERE card_id = $1 OR account_id = $1`, req.FromAccount).Scan(&balance, &status)

	if err != nil {
		if err == sql.ErrNoRows {
			log.Printf("[VoiceProvider] Validation failed: account not found: %s", req.FromAccount)
			return errors.New("account not found")
		}
		log.Printf("[VoiceProvider] Database error during validation: %v", err)
		return errors.New("validation failed")
	}

	log.Printf("[VoiceProvider] Account status: %s, balance: %d", status, balance)
	if status != "ACTIVE" {
		log.Printf("[VoiceProvider] Validation failed: account not active, status=%s", status)
		return errors.New("account not active")
	}

	if balance < req.Amount {
		log.Printf("[VoiceProvider] Validation failed: insufficient balance, required=%d, available=%d", req.Amount, balance)
		return errors.New("insufficient balance")
	}

	log.Printf("[VoiceProvider] Validation passed")
	return nil
}

func (p *VoicePaymentProvider) ProcessPayment(ctx context.Context, req *models.PaymentRequest) (*models.PaymentResponse, error) {
	log.Printf("[VoiceProvider] Starting payment processing: txID=%s", req.TransactionID)

	if err := p.ValidatePayment(ctx, req); err != nil {
		log.Printf("[VoiceProvider] Payment validation failed: %v", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       err.Error(),
			PaymentMode:   models.PaymentModeVoice,
			Timestamp:     time.Now(),
		}, err
	}

	log.Printf("[VoiceProvider] Beginning database transaction")
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("[VoiceProvider] Failed to begin transaction: %v", err)
		return nil, err
	}
	defer tx.Rollback()

	log.Printf("[VoiceProvider] Debiting account: %s, amount: %d", req.FromAccount, req.Amount)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance - $1 WHERE card_id = $2 OR account_id = $2`, req.Amount, req.FromAccount)
	if err != nil {
		log.Printf("[VoiceProvider] Failed to debit account: %v", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Transfer failed",
			PaymentMode:   models.PaymentModeVoice,
			Timestamp:     time.Now(),
		}, err
	}

	log.Printf("[VoiceProvider] Crediting account: %s, amount: %d", req.ToAccount, req.Amount)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance + $1 WHERE card_id = $2 OR account_id = $2`, req.Amount, req.ToAccount)
	if err != nil {
		log.Printf("[VoiceProvider] Failed to credit account: %v", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Transfer failed",
			PaymentMode:   models.PaymentModeVoice,
			Timestamp:     time.Now(),
		}, err
	}

	log.Printf("[VoiceProvider] Inserting transaction record")
	metadata, _ := json.Marshal(req.Metadata)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO transactions 
		(transaction_id, from_card_id, to_card_id, amount, currency, narration, type, payment_mode, status, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'DEBIT', 'VOICE', 'COMPLETED', $7, NOW())
	`, req.TransactionID, req.FromAccount, req.ToAccount, req.Amount, req.Currency, req.Narration, metadata)

	if err != nil {
		log.Printf("[VoiceProvider] Failed to insert transaction: %v", err)
		return nil, err
	}

	log.Printf("[VoiceProvider] Committing transaction")
	if err := tx.Commit(); err != nil {
		log.Printf("[VoiceProvider] Failed to commit transaction: %v", err)
		return nil, err
	}

	log.Printf("[VoiceProvider] Payment successful")
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
