package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"net/http"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/services"
)

type BankTransferPaymentProvider struct {
	*BasePaymentProvider
	iso20022Service *services.ISO20022Service
	ledgerService   *services.DoubleLedgerService
	feePercentage   float64
	feeFixed        int64
}

func NewBankTransferPaymentProvider(db *sql.DB, redis *redis.Client, hsmInstance hsm.HSMInterface) *BankTransferPaymentProvider {
	return &BankTransferPaymentProvider{
		BasePaymentProvider: NewBasePaymentProvider(db, redis, hsmInstance),
		iso20022Service:     services.NewISO20022Service(),
		ledgerService:       services.NewDoubleLedgerService(db),
		feePercentage:       0.5,
		feeFixed:            10,
	}
}

func (p *BankTransferPaymentProvider) GetPaymentMode() models.PaymentMode {
	return models.PaymentModeBankTransfer
}

func (p *BankTransferPaymentProvider) ValidatePayment(ctx context.Context, req *models.PaymentRequest) error {
	log.Printf("[BankTransferProvider] Validating Payment: [From]=%s, [To]=%s, [Amount]=%d, [Type]=%s", req.FromAccount, req.ToAccount, req.Amount, req.TxType)

	if req.FromAccount == "" {
		log.Printf("[BankTransferProvider] Validation Failed: source account is empty")
		return errors.New("source account is required")
	}
	if req.ToAccount == "" {
		log.Printf("[BankTransferProvider] Validation Failed: destination account is empty")
		return errors.New("destination account is required")
	}
	if req.FromAccount == req.ToAccount {
		log.Printf("[BankTransferProvider] Validation Failed: same account transfer")
		return errors.New("cannot transfer to same account")
	}
	if req.Amount <= 0 {
		log.Printf("[BankTransferProvider] Validation Failed: invalid amount=%d", req.Amount)
		return errors.New("amount must be positive")
	}

	var balance int64
	var status string
	err := p.DB.QueryRowContext(ctx, `SELECT balance, status FROM accounts WHERE account_id = $1 OR card_id = $1`, req.FromAccount).Scan(&balance, &status)

	if err != nil {
		if err == sql.ErrNoRows {
			log.Printf("[BankTransferProvider] Validation failed: source account not found: %s", req.FromAccount)
			return errors.New("source account not found")
		}
		log.Printf("[BankTransferProvider] Database error during validation: %v", err)
		return errors.New("validation failed")
	}

	log.Printf("[BankTransferProvider] Account status: %s, balance: %d", status, balance)
	if status != "ACTIVE" {
		log.Printf("[BankTransferProvider] Validation failed: account not active, status=%s", status)
		return errors.New("source account not active")
	}

	fee := p.calculateFee(req.Amount)
	totalAmount := req.Amount + fee
	log.Printf("[BankTransferProvider] Fee Calculation: [Amount]=%d, [Fee]=%d, [Total]=%d, [Balance]=%d", req.Amount, fee, totalAmount, balance)

	if balance < totalAmount {
		log.Printf("[BankTransferProvider] Validation failed: Insufficient Balance, required=%d, available=%d", totalAmount, balance)
		return errors.New("Insufficient Balance")
	}

	log.Printf("[BankTransferProvider] Validation Passed")
	return nil
}

func (p *BankTransferPaymentProvider) ProcessPayment(ctx context.Context, req *models.PaymentRequest) (*models.PaymentResponse, error) {
	log.Printf("[BankTransferProvider] Starting Payment Processing: txID=%s", req.TransactionID)

	if err := p.ValidatePayment(ctx, req); err != nil {
		log.Printf("[BankTransferProvider] Payment Validation Failed: %v", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       err.Error(),
			PaymentMode:   models.PaymentModeBankTransfer,
			Timestamp:     time.Now(),
		}, err
	}

	log.Printf("[BankTransferProvider] Beginning Database Transaction")
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("[BankTransferProvider] Failed to begin transaction: %v", err)
		return nil, err
	}
	defer tx.Rollback()

	fee := p.calculateFee(req.Amount)
	totalAmount := req.Amount + fee

	// Generate HSM signature for the transaction
	timestamp := time.Now()
	nonce := uuid.New().String()

	hsmTx := &hsm.Transaction{
		ID:            req.TransactionID,
		FromAccountID: req.FromAccount,
		ToAccountID:   req.ToAccount,
		Amount:        float64(req.Amount),
		Timestamp:     timestamp,
		Nonce:         nonce,
	}
	signature, err := p.HSM.SignTransaction(hsmTx)
	if err != nil {
		log.Printf("[BankTransferProvider] Failed to sign transaction: %v", err)
		return nil, errors.New("security signing failed")
	}

	log.Printf("[BankTransferProvider] Processing Transfer with Ledger Service")
	if err := p.ledgerService.TransferTx(tx, req.FromAccount, req.ToAccount, req.TransactionID, totalAmount); err != nil {
		log.Printf("[BankTransferProvider] Ledger Transfer Failed: %v", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       err.Error(),
			PaymentMode:   models.PaymentModeBankTransfer,
			Timestamp:     time.Now(),
		}, err
	}

	log.Printf("[BankTransferProvider] Updating daily spent and inserting transaction")
	// Update metadata with signing info
	if req.Metadata == nil {
		req.Metadata = make(map[string]any)
	}
	req.Metadata["signing_nonce"] = nonce
	req.Metadata["signing_timestamp"] = timestamp
	metadata, _ := json.Marshal(req.Metadata)
	locationJSON, _ := json.Marshal(req.Location)

	_, err = tx.ExecContext(ctx, `
		WITH user_info AS (
			SELECT a.user_id 
			FROM accounts a 
			WHERE a.account_id = $2 OR a.card_id = $2 
			LIMIT 1
		),
		limit_update AS (
			UPDATE user_limits ul
			SET updated_at = NOW()
			FROM user_info ui
			WHERE ul.user_id = ui.user_id
			RETURNING ul.user_id
		)
		INSERT INTO transactions 
		(transaction_id, from_card_id, to_card_id, amount, fee, total_amount, currency, narration, type, payment_mode, status, location, metadata, user_id, created_at, signature)
		SELECT $3, $2, $4, $5, $6, $1, $7, $8, $9, 'BANK_TRANSFER', 'PENDING', $10, $11, ui.user_id, NOW(), $12
		FROM user_info ui
	`, totalAmount, req.FromAccount, req.TransactionID, req.ToAccount, req.Amount, fee, req.Currency, req.Narration, req.TxType, locationJSON, metadata, signature)

	if err != nil {
		log.Printf("[BankTransferProvider] Failed to update daily spent or insert transaction: %v", err)
		return nil, err
	}

	log.Printf("[BankTransferProvider] Committing Transaction")
	if err := tx.Commit(); err != nil {
		log.Printf("[BankTransferProvider] Failed to commit transaction: %v", err)
		return nil, err
	}

	log.Printf("[BankTransferProvider] Successfully committed transaction, Sending To Settlement")
	err = p.sendToSettlement(req)
	if err != nil {
		log.Printf("[BankTransferProvider] Settlement failed: %v", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       fmt.Sprintf("Settlement failed: %v", err),
			PaymentMode:   models.PaymentModeBankTransfer,
			Timestamp:     time.Now(),
		}, nil
	}

	return &models.PaymentResponse{
		Success:       true,
		TransactionID: req.TransactionID,
		Status:        "COMPLETED",
		Message:       "Bank transfer successful",
		PaymentMode:   models.PaymentModeBankTransfer,
		Timestamp:     time.Now(),
	}, nil
}

func (p *BankTransferPaymentProvider) calculateFee(amount int64) int64 {
	fee := int64(float64(amount) * p.feePercentage / 100)
	return fee + p.feeFixed
}

func (p *BankTransferPaymentProvider) sendToSettlement(req *models.PaymentRequest) error {
	log.Printf("[BankTransferProvider] Preparing settlement for transaction: %s", req.TransactionID)
	modelTx := &models.Transaction{
		TransactionID: req.TransactionID,
		ReferenceID:   req.TransactionID,
		FromAccountID: req.FromAccount,
		ToAccountID:   req.ToAccount,
		Type:          string(req.TxType),
		Amount:        float64(req.Amount) / 100,
		Currency:      req.Currency,
		Status:        "PENDING",
	}

	if bankCode, ok := req.Metadata["toBankCode"].(string); ok {
		modelTx.ToBankCode = bankCode
	}

	doc, err := p.iso20022Service.ConvertTransaction(modelTx)
	if err != nil {
		log.Printf("[BankTransferProvider] Failed to convert transaction to ISO20022: %v", err)
		if _, dbErr := p.DB.Exec(`UPDATE transactions SET status = $1 WHERE transaction_id = $2`, "FAILED_ISO_CONVERSION", req.TransactionID); dbErr != nil {
			log.Printf("[BankTransferProvider] Failed to update transaction status to FAILED_ISO_CONVERSION: %v", dbErr)
		}
		return err
	}

	log.Printf("[BankTransferProvider] Sending %v To Settlement Service...", req.TransactionID)
	resp, err := p.iso20022Service.SendToSettlement(doc)
	if err != nil {
		log.Printf("[BankTransferProvider] Failed To Send To Settlement: %v ---> %v", err, resp)
		if _, dbErr := p.DB.Exec(`UPDATE transactions SET status = $1 WHERE transaction_id = $2`, "FAILED_SETTLEMENT", req.TransactionID); dbErr != nil {
			log.Printf("[BankTransferProvider] Failed to update transaction status to FAILED_SETTLEMENT: %v", dbErr)
		}
		return err
	}

	log.Printf("[BankTransferProvider] Settlement successful. Response status: %s", resp.Status)
	respJSON, _ := json.Marshal(resp)
	if _, dbErr := p.DB.Exec(`UPDATE transactions SET status = $1, settlement_response = $2 WHERE transaction_id = $3`, "SETTLED", respJSON, req.TransactionID); dbErr != nil {
		log.Printf("[BankTransferProvider] Failed to update transaction status to SETTLED: %v", dbErr)
	}
	return nil
}

func (p *BankTransferPaymentProvider) HandlePayment(w http.ResponseWriter, r *http.Request) {
	p.BasePaymentProvider.HandlePaymentRequest(w, r, p)
}
