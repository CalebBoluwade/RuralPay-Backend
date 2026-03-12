package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"

	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/services"
	"github.com/ruralpay/backend/internal/utils"
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
	slog.Info("validate.card.request", "amount", req.Amount)

	if req.FromAccount == "" {
		return errors.New("Debit Card is required")
	}
	if req.Amount <= 0 {
		return errors.New("amount must be positive")
	}

	var balance int64
	var status string
	err := p.DB.QueryRowContext(ctx, `SELECT balance, status FROM accounts WHERE card_id = $1`, req.FromAccount).Scan(&balance, &status)

	if err != nil {
		if err == sql.ErrNoRows {
			return errors.New("card not found")
		}
		slog.Error("card.validate.db_error", "error", err)
		return errors.New(string(utils.ValidationError))
	}

	if status != "ACTIVE" {
		return errors.New("card not active")
	}

	if balance < req.Amount {
		return errors.New("insufficient balance")
	}

	return nil
}

func (p *CardPaymentProvider) ProcessPayment(ctx context.Context, req *models.PaymentRequest) (*models.PaymentResponse, error) {
	slog.Info("card.process.start", "tx_id", req.TransactionID)

	cardReq, ok := req.Metadata["cardPaymentRequest"].(*models.CardPaymentRequest)
	if !ok {
		slog.Error("card.process.invalid_request", "tx_id", req.TransactionID)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Invalid Card Payment Request",
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, errors.New("invalid card payment request")
	}

	slog.Info("card.process.building_iso8583", "tx_id", req.TransactionID)
	isoMsg, err := p.iso8583Service.BuildISO8583Message(cardReq)
	if err != nil {
		slog.Error("card.process.iso8583_build_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Failed to Build Payment Message",
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, err
	}

	_, err = p.iso8583Service.ProcessMessage(ctx, isoMsg)
	if err != nil {
		slog.Error("card.process.iso8583_process_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Payment Processing Failed",
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, err
	}

	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("card.process.tx_begin_failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}
	defer tx.Rollback()

	// Generate HSM signature for the transaction
	timestamp := time.Now()
	nonce := uuid.New().String()

	hsmTx := &hsm.Transaction{
		ID:            req.TransactionID,
		FromAccountID: req.FromAccount,
		ToAccountID:   req.BeneficiaryAccountNumber,
		Amount:        float64(req.Amount),
		Timestamp:     timestamp,
		Nonce:         nonce,
	}
	signature, err := p.HSM.SignTransaction(hsmTx)
	if err != nil {
		slog.Error("card.process.sign_failed", "tx_id", req.TransactionID, "error", err)
		return nil, errors.New("security signing failed")
	}

	slog.Info("card.process.inserting", "tx_id", req.TransactionID)

	// Update metadata with signing info
	if req.Metadata == nil {
		req.Metadata = make(map[string]any)
	}
	req.Metadata["signing_nonce"] = nonce
	req.Metadata["signing_timestamp"] = timestamp
	metadataJSON, _ := json.Marshal(req.Metadata)

	_, err = tx.ExecContext(ctx, `
		WITH merchant_info AS (
			SELECT m.account_id 
			FROM merchants m 
			WHERE m.id = $2 
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
		(transaction_id, debit_id, credit_id, amount, total_amount, type, payment_mode, status, user_id, created_at, signature, metadata)
		SELECT $1, $2, $3, $4, $4, 'DEBIT', 'CARD', 'COMPLETED', ui.user_id, NOW(), $5, $6
		FROM user_info ui
	`, req.TransactionID, req.FromAccount, req.BeneficiaryAccountNumber, req.Amount, signature, metadataJSON)

	if err != nil {
		slog.Error("card.process.insert_failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		slog.Error("card.process.commit_failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}

	resp, err := p.nibssClient.ProcessCardSettlement(isoMsg)
	if err != nil {
		slog.Error("card.process.settlement_failed", "tx_id", req.TransactionID, "error", err)
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

	slog.Info("card.process.success", "tx_id", req.TransactionID)
	return &models.PaymentResponse{
		Success:       true,
		TransactionID: req.TransactionID,
		Status:        "COMPLETED",
		Message:       "Payment Successful",
		PaymentMode:   models.PaymentModeCard,
		Timestamp:     time.Now(),
	}, nil
}

func (p *CardPaymentProvider) DecryptPIICredentials(encryptedText string) string {
	plaintext, err := p.HSM.DecryptPII(encryptedText)
	if err != nil {
		slog.Error("card.decrypt_pii_failed", "error", err)
		return ""
	}
	return plaintext
}

func (p *CardPaymentProvider) HandlePayment(w http.ResponseWriter, r *http.Request) {
	slog.Info("card.handle_payment.start")

	userID, merchantID := utils.ExtractUserMerchantInfoFromContext(w, r.Context())
	slog.Info("card.handle_payment.context", "user_id", userID, "merchant_id", merchantID)

	var cardReq models.CardPaymentRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&cardReq); err != nil {
		slog.Error("card.handle_payment.decode_failed", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	slog.Info("card.handle_payment.request", "tx_id", cardReq.TransactionID, "amount", cardReq.Amount, "type", cardReq.TxType)

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		slog.Warn("card.handle_payment.multiple_json_objects")
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	req := &models.PaymentRequest{
		TransactionID:            cardReq.TransactionID,
		UserID:                   strconv.Itoa(userID),
		FromAccount:              p.DecryptPIICredentials(cardReq.CardInfo.EncryptedPAN),
		BeneficiaryAccountNumber: p.DecryptPIICredentials(cardReq.CardInfo.EncryptedPAN),
		Amount:                   cardReq.Amount,
		PaymentMode:              models.PaymentModeCard,
		Metadata: map[string]any{
			"cardPaymentRequest": &cardReq,
		},
		Location: cardReq.Location,
	}

	if cachedStatus, found := p.checkIdempotency(req.TransactionID); found {
		slog.Info("card.handle_payment.idempotent", "tx_id", req.TransactionID, "status", cachedStatus)
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
		slog.Error("card.handle_payment.failed", "tx_id", req.TransactionID, "error", err)
		p.Audit.LogError(req.TransactionID, req.FromAccount, err)
		utils.SendErrorResponse(w, "Payment Processing Failed", http.StatusFailedDependency, nil)
		return
	}

	p.setIdempotency(req.TransactionID, response.Status)
	p.Audit.LogTransfer(req.TransactionID, req.FromAccount, req.BeneficiaryAccountNumber, req.Amount, response.Status)

	slog.Info("card.handle_payment.response", "tx_id", req.TransactionID, "success", response.Success, "status", response.Status)
	w.Header().Set("Content-Type", "application/json")
	if response.Success {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusBadRequest)
	}
	json.NewEncoder(w).Encode(response)
}
