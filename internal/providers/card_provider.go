package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
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
		return errors.New("debit Card is required")
	}
	if req.Amount <= 0 {
		return errors.New("amount must be positive")
	}

	return nil
}

func (p *CardPaymentProvider) ProcessPayment(ctx context.Context, req *models.PaymentRequest) (*models.PaymentResponse, error) {
	slog.Info("card.process.start", "tx_id", req.TransactionID)

	//merchantIDStr, _ := ctx.Value("merchantID").(string)
	//merchantID, _ := strconv.Atoi(merchantIDStr)

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
		}, errors.New("invalid Card Payment Request")
	}

	//if merchantIdFromContext != cardReq.MerchantID {
	//	return &models.PaymentResponse{
	//		Success:       false,
	//		TransactionID: req.TransactionID,
	//		Status:        "FAILED",
	//		Message:       "Payment Merchant Mismatch",
	//		PaymentMode:   models.PaymentModeCard,
	//		Timestamp:     time.Now(),
	//	}, errors.New("payment merchant mismatch")
	//}

	if p.checkExpiredCard(cardReq.CardInfo.ExpiryDate) {
		slog.Error("failed.expired.card.validation", "tx_id", req.TransactionID)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       "Expired Card",
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, errors.New("expired Card")
	}

	slog.Info("card.process.building_ISO8583", "tx_id", req.TransactionID)
	isoMsg, err := p.iso8583Service.BuildISO8583Message(cardReq)
	if err != nil {
		slog.Error("card.process.ISO8583_build_failed", "tx_id", req.TransactionID, "error", err)
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
		slog.Error("card.process.ISO8583_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       utils.ProcessingFailed.Response(),
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

	// Encrypt PAN for storage in debit_id
	encryptedPAN, err := p.HSM.EncryptPAN(req.FromAccount)
	if err != nil {
		slog.Error("card.process.pan_encrypt_failed", "tx_id", req.TransactionID, "error", err)
		return nil, errors.New("failed to secure card data")
	}

	// Resolve merchant account_id for credit side
	var merchantAccountID string
	err = tx.QueryRowContext(ctx, `SELECT account_id FROM merchants WHERE id = $1`, cardReq.MerchantID).Scan(&merchantAccountID)
	if err != nil {
		slog.Error("card.process.merchant_lookup_failed", "tx_id", req.TransactionID, "merchant_id", cardReq.MerchantID, "error", err)
		return nil, errors.New("merchant not found")
	}

	// Update metadata with signing info
	if req.Metadata == nil {
		req.Metadata = make(map[string]any)
	}
	req.Metadata["signing_nonce"] = nonce
	req.Metadata["signing_timestamp"] = timestamp
	metadataJSON, _ := json.Marshal(req.Metadata)

	_, err = tx.ExecContext(ctx, `
		INSERT INTO transactions
		(transaction_id, debit_id, credit_id, amount, total_amount, type, payment_mode, status, user_id, created_at, signature, metadata)
		VALUES ($1, $2, $3, $4, $4, 'DEBIT', 'CARD', 'COMPLETED', $5, NOW(), $6, $7)
	`, req.TransactionID, encryptedPAN, merchantAccountID, req.Amount, req.UserID, signature, metadataJSON)

	if err != nil {
		slog.Error("card.process.insert_failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		slog.Error("card.process.commit_failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}

	slog.Info("card.process.sealing_for_nibss", "tx_id", req.TransactionID)
	sealedMsg, err := p.iso8583Service.SignAndSealPayload(isoMsg)
	if err != nil {
		slog.Error("card.process.sign_and_seal_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       utils.ProcessingFailed.Response(),
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, err
	}

	resp, err := p.nibssClient.ProcessCardSettlement(sealedMsg)
	if err != nil {
		slog.Error("card.process.settlement_failed", "tx_id", req.TransactionID, "error", err)
		p.DB.Exec(`UPDATE transactions SET status = $1, updated_at = NOW() WHERE transaction_id = $2`, "FAILED_SETTLEMENT", req.TransactionID)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       utils.ProcessingFailed.Response(),
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, err
	}

	respJSON, _ := json.Marshal(resp)
	p.DB.Exec(`UPDATE transactions SET settlement_response = $1, updated_at = NOW() WHERE transaction_id = $2`, respJSON, req.TransactionID)

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
	slog.Debug("card.decrypt_pii.start", "encoded_len", len(encryptedText))
	plaintext, err := p.HSM.DecryptPII(encryptedText)
	if err != nil {
		slog.Error("card.decrypt_pii_failed", "encoded_len", len(encryptedText), "error", err)
		return ""
	}
	slog.Debug("card.decrypt_pii.success", "plaintext_len", len(plaintext))
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

	decryptedPAN := p.DecryptPIICredentials(cardReq.CardInfo.EncryptedPAN)
	if decryptedPAN == "" {
		slog.Error("card.handle_payment.decrypt_failed", "tx_id", cardReq.TransactionID)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusFailedDependency, nil)
		return
	}

	if cachedStatus, found := p.checkIdempotency(cardReq.TransactionID); found {
		slog.Info("card.handle_payment.idempotent", "tx_id", cardReq.TransactionID, "cached_status", cachedStatus)
		if cachedStatus == "COMPLETED" || cachedStatus == "PENDING" {
			utils.SendSuccessResponse(w, "Payment Already Processed", map[string]any{
				"transactionId": cardReq.TransactionID,
				"status":        cachedStatus,
				"paymentMode":   models.PaymentModeCard,
			}, http.StatusOK)
		} else {
			utils.SendErrorResponse(w, "Payment Failed", http.StatusBadRequest, nil)
		}
		return
	}

	// Reserve the transaction ID before processing to prevent duplicate inserts on retry
	p.setIdempotency(cardReq.TransactionID, "PENDING")

	req := &models.PaymentRequest{
		TransactionID:            cardReq.TransactionID,
		UserID:                   strconv.Itoa(userID),
		FromAccount:              decryptedPAN,
		BeneficiaryAccountNumber: decryptedPAN,
		Amount:                   cardReq.Amount,
		PaymentMode:              models.PaymentModeCard,
		Metadata: map[string]any{
			"cardPaymentRequest": &cardReq,
		},
		Location: cardReq.Location,
	}

	response, err := p.ProcessPayment(r.Context(), req)
	if err != nil {
		slog.Error("card.handle_payment.failed", "tx_id", req.TransactionID, "err", err)
		p.setIdempotency(req.TransactionID, "FAILED")
		p.Audit.LogError(req.TransactionID, req.FromAccount, err)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusFailedDependency, nil)
		return
	}

	p.setIdempotency(req.TransactionID, response.Status)
	p.Audit.LogTransfer(req.TransactionID, req.FromAccount, req.BeneficiaryAccountNumber, req.Amount, response.Status)

	slog.Info("card.handle_payment.response", "tx_id", req.TransactionID, "success", response.Success, "status", response.Status)

	if response.Success {
		utils.SendSuccessResponse(w, "Payment Processed", response, http.StatusOK)
	} else {
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusBadRequest, nil)
	}
}

// checkExpiredCard returns true if the card is expired.
// Format expected: "MM/YY" (e.g., "09/28")
func (p *CardPaymentProvider) checkExpiredCard(expiryDate string) bool {
	// 1. Split the MM/YY string
	parts := strings.Split(expiryDate, "/")
	if len(parts) != 2 {
		return true // Treat malformed dates as expired/invalid
	}

	month, _ := strconv.Atoi(parts[0])
	yearShort, _ := strconv.Atoi(parts[1])

	// 2. Convert YY to full year (e.g., 28 -> 2028)
	// This assumes we are in the 21st century.
	year := 2000 + yearShort

	// 3. Get the current time
	now := time.Now()
	currentYear := now.Year()
	currentMonth := int(now.Month())

	// 4. Compare Year
	if year < currentYear {
		return true
	}

	// 5. If it's the same year, check if the month has passed
	if year == currentYear && month < currentMonth {
		return true
	}

	return false
}
