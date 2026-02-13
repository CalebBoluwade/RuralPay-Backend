package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"

	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/services"
)

type CardPaymentProvider struct {
	*BasePaymentProvider
	iso8583Service models.ISO8583Service
	nibssClient    *services.NIBSSClient
}

func NewCardPaymentProvider(db *sql.DB, redis *redis.Client, hsmInstance hsm.HSMInterface) *CardPaymentProvider {
	return &CardPaymentProvider{
		BasePaymentProvider: NewBasePaymentProvider(db, redis, hsmInstance),
		iso8583Service:      services.NewISO8583Service(db, hsmInstance),
		nibssClient:         services.NewNIBSSClient(),
	}
}

func (p *CardPaymentProvider) GetPaymentMode() models.PaymentMode {
	return models.PaymentModeCard
}

func (p *CardPaymentProvider) ValidatePayment(ctx context.Context, req *models.PaymentRequest) error {
	log.Printf("[CardProvider] Validating payment: card=%s, amount=%d", req.FromAccount, req.Amount)

	if req.FromAccount == "" {
		log.Printf("[CardProvider] Validation failed: card ID is empty")
		return errors.New("card ID is required")
	}
	if req.Amount <= 0 {
		log.Printf("[CardProvider] Validation failed: invalid amount=%d", req.Amount)
		return errors.New("amount must be positive")
	}

	var balance int64
	var status string
	err := p.DB.QueryRowContext(ctx, `SELECT balance, status FROM accounts WHERE card_id = $1`, req.FromAccount).Scan(&balance, &status)

	if err != nil {
		if err == sql.ErrNoRows {
			log.Printf("[CardProvider] Validation failed: card not found: %s", req.FromAccount)
			return errors.New("card not found")
		}
		log.Printf("[CardProvider] Database error during validation: %v", err)
		return errors.New("validation failed")
	}

	log.Printf("[CardProvider] Card status: %s, balance: %d", status, balance)
	if status != "ACTIVE" {
		log.Printf("[CardProvider] Validation failed: card not active, status=%s", status)
		return errors.New("card not active")
	}

	if balance < req.Amount {
		log.Printf("[CardProvider] Validation failed: insufficient balance, required=%d, available=%d", req.Amount, balance)
		return errors.New("insufficient balance")
	}

	log.Printf("[CardProvider] Validation passed")
	return nil
}

func (p *CardPaymentProvider) ProcessPayment(ctx context.Context, req *models.PaymentRequest) (*models.PaymentResponse, error) {
	log.Printf("[CardProvider] Processing Card Payment via ISO 8583: [TransactionID]=%s", req.TransactionID)
	log.Printf("[CardProvider] Payment request metadata: %+v", req.Metadata)

	cardReq, ok := req.Metadata["cardPaymentRequest"].(*models.CardPaymentRequest)
	if !ok {
		log.Printf("[CardProvider] Failed to extract CardPaymentRequest from metadata")
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Invalid Card Payment Request",
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, errors.New("invalid card payment request")
	}

	log.Printf("[CardProvider] Building ISO 8583 message: [PAN]=%s, [Amount]=%d, [TxType]=%s",
		maskPAN(p.DecryptPIICredentials(cardReq.CardInfo.EncryptedPAN)), cardReq.Amount, cardReq.TxType)

	isoMsg, err := p.iso8583Service.BuildISO8583Message(cardReq)
	if err != nil {
		log.Printf("[CardProvider] Failed to build ISO 8583 message: %v", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Failed to build payment message",
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, err
	}

	log.Printf("[CardProvider] ISO 8583 Message Built Successfully, size: %d bytes", len(isoMsg))

	respMsg, err := p.iso8583Service.ProcessMessage(ctx, isoMsg)
	if err != nil {
		log.Printf("[CardProvider] ISO 8583 processing failed: %v", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Payment processing failed",
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, err
	}

	log.Printf("[CardProvider] ISO 8583 response received: %d bytes", len(respMsg))
	log.Printf("[CardProvider] ISO 8583 response (hex): %x", respMsg)

	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("[CardProvider] Failed to begin transaction: %v", err)
		return nil, err
	}
	defer tx.Rollback()

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
		log.Printf("[CardProvider] Failed to sign transaction: %v", err)
		return nil, errors.New("security signing failed")
	}

	log.Printf("[CardProvider] Inserting Transaction and updating daily spent: [txID]=%s, [Card]=%s, [Amount]=%d",
		req.TransactionID, maskPAN(req.FromAccount), req.Amount)

	// Update metadata with signing info
	if req.Metadata == nil {
		req.Metadata = make(map[string]any)
	}
	req.Metadata["signing_nonce"] = nonce
	req.Metadata["signing_timestamp"] = timestamp
	metadataJSON, _ := json.Marshal(req.Metadata)

	_, err = tx.ExecContext(ctx, `
		WITH user_info AS (
			SELECT a.user_id 
			FROM accounts a 
			WHERE a.card_id = $2 
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
		(transaction_id, from_card_id, to_card_id, amount, total_amount, type, payment_mode, status, user_id, created_at, signature, metadata)
		SELECT $1, $2, $3, $4, $4, 'DEBIT', 'CARD', 'COMPLETED', ui.user_id, NOW(), $5, $6
		FROM user_info ui
	`, req.TransactionID, req.FromAccount, req.ToAccount, req.Amount, signature, metadataJSON)

	if err != nil {
		log.Printf("[CardProvider] Failed to Insert Transaction or Update Daily Spent: %v", err)
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		log.Printf("[CardProvider] Failed to Commit Transaction: %v", err)
		return nil, err
	}

	resp, err := p.nibssClient.ProcessCardSettlement(isoMsg)
	if err != nil {
		log.Printf("[CardProvider] Settlement Failed: %v", err)
		p.DB.Exec(`UPDATE transactions SET status = $1 WHERE transaction_id = $2`, "FAILED_SETTLEMENT", req.TransactionID)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       fmt.Sprintf("Settlement Failed: %v", err),
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, err
	}

	respJSON, _ := json.Marshal(resp)
	p.DB.Exec(`UPDATE transactions SET settlement_response = $1 WHERE transaction_id = $2`, respJSON, req.TransactionID)

	log.Printf("[CardProvider] Payment Successful: [txID]=%s", req.TransactionID)
	return &models.PaymentResponse{
		Success:       true,
		TransactionID: req.TransactionID,
		Status:        "COMPLETED",
		Message:       "Payment Successful",
		PaymentMode:   models.PaymentModeCard,
		Timestamp:     time.Now(),
	}, nil
}

func maskPAN(pan string) string {
	if len(pan) < 10 {
		return "****"
	}
	return pan[:6] + "****" + pan[len(pan)-4:]
}

func (p *CardPaymentProvider) DecryptPIICredentials(encryptedText string) string {
	plaintext, err := p.HSM.DecryptPII(encryptedText)
	if err != nil {
		log.Printf("[CardProvider] Failed to decrypt PII: %v", err)
		return ""
	}
	return plaintext
}

func (p *CardPaymentProvider) HandlePayment(w http.ResponseWriter, r *http.Request) {
	log.Printf("[CardProvider] Starting Card Payment")

	userID, ok := r.Context().Value("userID").(string)
	if !ok || userID == "" {
		log.Printf("[CardProvider] Unauthorized: userID not found in context")
		services.SendErrorResponse(w, "Unauthorized", http.StatusUnauthorized, nil)
		return
	}
	log.Printf("[CardProvider] UserID from context: %s", userID)

	var cardReq models.CardPaymentRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&cardReq); err != nil {
		log.Printf("[CardProvider] Error decoding request: %v", err)
		services.SendErrorResponse(w, "Invalid request body", http.StatusBadRequest, nil)
		return
	}

	log.Printf("[CardProvider] Decoded card payment request: txID=%s, amount=%d, txType=%s, PAN=%s",
		cardReq.TransactionID, cardReq.Amount, cardReq.TxType, maskPAN(p.DecryptPIICredentials(cardReq.CardInfo.EncryptedPAN)))

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		log.Printf("[CardProvider] Multiple JSON objects detected")
		services.SendErrorResponse(w, "Request body must only contain a single JSON object", http.StatusBadRequest, nil)
		return
	}

	req := &models.PaymentRequest{
		TransactionID: cardReq.TransactionID,
		UserID:        userID,
		FromAccount:   p.DecryptPIICredentials(cardReq.CardInfo.EncryptedPAN),
		Amount:        cardReq.Amount,
		PaymentMode:   models.PaymentModeCard,
		Metadata: map[string]any{
			"cardPaymentRequest": &cardReq,
		},
		Location: cardReq.Location,
	}

	log.Printf("[CardProvider] Created Payment Request: txID=%s", req.TransactionID)

	if cachedStatus, found := p.checkIdempotency(req.TransactionID); found {
		log.Printf("[CardProvider] Idempotent request detected: txID=%s, status=%s", req.TransactionID, cachedStatus)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"success":       cachedStatus == "COMPLETED" || cachedStatus == "PENDING",
			"transactionId": req.TransactionID,
			"status":        cachedStatus,
			"message":       "Payment Already Processed",
			"paymentMode":   req.PaymentMode,
		})
		return
	}

	response, err := p.ProcessPayment(r.Context(), req)
	if err != nil {
		log.Printf("[CardProvider] Payment Failed: %v", err)
		p.Audit.LogError(req.TransactionID, req.FromAccount, err)
		services.SendErrorResponse(w, "Payment Processing Failed", http.StatusInternalServerError, nil)
		return
	}

	p.setIdempotency(req.TransactionID, response.Status)
	p.Audit.LogTransfer(req.TransactionID, req.FromAccount, req.ToAccount, req.Amount, response.Status)

	log.Printf("[CardProvider] Sending Response: success=%v, status=%s", response.Success, response.Status)
	w.Header().Set("Content-Type", "application/json")
	if response.Success {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusBadRequest)
	}
	json.NewEncoder(w).Encode(response)
}
