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
	"strings"
	"time"

	"github.com/go-redis/redis/v8"

	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/services"
	"github.com/ruralpay/backend/internal/utils"
)

type CardPaymentProvider struct {
	*BasePaymentProvider
	iso8583Service *services.ISO8583Service
	nibssClient    *services.NIBSSClient
}

func NewCardPaymentProvider(db *sql.DB, redis *redis.Client, hsmInstance hsm.HSMInterface) *CardPaymentProvider {
	return &CardPaymentProvider{
		BasePaymentProvider: NewBasePaymentProvider(db, redis, hsmInstance),
		iso8583Service:      services.NewISO8583Service(db, hsmInstance),
		// nibssClient:         services.NewNIBSSClient(),
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

	merchantIDStr, _ := ctx.Value("merchantID").(string)
	merchantIdFromContext, err := strconv.Atoi(merchantIDStr)
	if err != nil {
		return errors.New("invalid merchant ID")
	}

	if merchantIdFromContext <= 0 {
		return errors.New("invalid merchant ID")
	}

	if merchantIdFromContext != req.Metadata["cardPaymentRequest"].(*models.CardPaymentRequest).MerchantID {
		return errors.New("payment merchant mismatch")
	}

	return nil
}

func (p *CardPaymentProvider) ProcessPayment(ctx context.Context, req *models.PaymentRequest) (*models.PaymentResponse, error) {
	slog.Info("Card.Processing.Start", "tx_id", req.TransactionID)

	cardReq, ok := req.Metadata["cardPaymentRequest"].(*models.CardPaymentRequest)
	if !ok {
		slog.Error("card.process.invalid_request", "tx_id", req.TransactionID)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        models.TransactionStatusFailed,
			Message:       "Invalid Card Payment Request",
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, errors.New("invalid Card Payment Request")
	}

	if p.checkExpiredCard(cardReq.CardInfo.ExpiryDate) {
		slog.Error("failed.expired.card.validation", "tx_id", req.TransactionID)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        models.TransactionStatusFailed,
			Message:       "Expired Card",
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, errors.New("expired Card")
	}

	slog.Info("card.process.building_ISO8583", "tx_id", req.TransactionID)

	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("card.process.tx_begin_failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}
	defer tx.Rollback()

	slog.Info("card.process.inserting", "tx_id", req.TransactionID)

	event := models.AuditEvent{
		Timestamp: time.Now(),
		EventType: "TRANSFER",
		TxRequest: req,
		Details: map[string]any{
			"merchantID": cardReq.MerchantID,
		},
	}

	if p.Audit.LogTransaction(ctx, tx, event); err != nil {
		slog.ErrorContext(ctx, "card.audit.log.failed", "tx_id", req.TransactionID, "error", err)

		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        models.TransactionStatusFailed,
			Message:       utils.InternalServiceError,
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, errors.New(string(utils.InternalServiceError))
	}

	slog.InfoContext(ctx, "bank_transfer.audit.log.success", "tx_id", req.TransactionID)

	// Build ISO 0800 message for key exchange
	cardISO0800, err := p.iso8583Service.BuildISO0800Message()
	if err != nil {
		slog.Error("card.process.ISO0800_build_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        models.TransactionStatusFailed,
			Message:       utils.ProcessingFailed,
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, err
	}

	cardISO0800_, err := cardISO0800.Pack()
	if err != nil {
		slog.Error("card.process.ISO0800_pack_failed", "tx_id", req.TransactionID, "error", err)
	}
	slog.Info("card.process.ISO0800_built", "tx_id", req.TransactionID, "iso0800_hex", fmt.Sprintf("%X", cardISO0800_))

	// Establish connection and send ISO 0800 message
	conn, err := p.nibssClient.DialISO8583(ctx, time.Now().Add(30*time.Second))
	if err != nil {
		slog.Error("card.process.dial_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        models.TransactionStatusFailed,
			Message:       utils.ProcessingFailed,
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, err
	}
	defer conn.Close()

	// Send ISO 0800 message
	res, err := p.nibssClient.SendAndReceive(conn, cardISO0800_)
	if err != nil {
		slog.Error("card.process.send_ISO0800_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        models.TransactionStatusFailed,
			Message:       utils.ProcessingFailed,
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, err
	}

	slog.Info("card.process.ISO0800_success", "tx_id", req.TransactionID, "response_hex", fmt.Sprintf("%x", res))

	// Read ISO 0810 response
	resp0810, err := p.nibssClient.ReadISOMessage(conn, p.iso8583Service.CreateISO8583_0800_MessageSpec1987())
	if err != nil {
		slog.Error("card.process.read_ISO0810_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        models.TransactionStatusFailed,
			Message:       utils.ProcessingFailed,
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, err
	}

	// Validate 0810 response
	MTI, err := resp0810.GetMTI()
	if err != nil || MTI != "0810" {
		slog.Error("card.process.invalid_0810_response", "tx_id", req.TransactionID, "mti", MTI, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        models.TransactionStatusFailed,
			Message:       utils.ProcessingFailed,
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, err
	}

	// Build ISO 0200 message for settlement
	cardISO0200 := p.iso8583Service.BuildISO0200MessageTest()

	resp, err := p.nibssClient.ProcessCardSettlement(ctx, cardISO0200)
	if err != nil {
		slog.Error("card.process.settlement_failed", "tx_id", req.TransactionID, "error", err)
		p.DB.ExecContext(ctx, `UPDATE transactions SET status = $1, updated_at = NOW() WHERE transaction_id = $2`, "FAILED_SETTLEMENT", req.TransactionID)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        models.TransactionStatusFailed,
			Message:       utils.ProcessingFailed,
			PaymentMode:   models.PaymentModeCard,
			Timestamp:     time.Now(),
		}, err
	}

	respJSON, _ := json.Marshal(resp)
	p.DB.ExecContext(ctx, `UPDATE transactions SET settlement_response = $1, updated_at = NOW() WHERE transaction_id = $2`, respJSON, req.TransactionID)

	slog.Info("card.process.success", "tx_id", req.TransactionID)
	return &models.PaymentResponse{
		Success:       true,
		TransactionID: req.TransactionID,
		Status:        models.TransactionStatusSuccess,
		Message:       utils.PaymentSuccessful,
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
	slog.Info("handle.card.payment.start")

	ctx := r.Context()

	userID, merchantID := utils.ExtractUserMerchantInfoFromContext(w, ctx)
	slog.Info("handle.card.payment.context", "user_id", userID, "merchant_id", merchantID)

	var cardReq models.CardPaymentRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&cardReq); err != nil {
		slog.Error("handle.card.payment.decode_failed", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	slog.Info("handle.card.payment.request", "tx_id", cardReq.TransactionID, "amount", cardReq.Amount, "type", cardReq.TxType)

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		slog.Warn("handle.card.payment.multiple_json_objects")
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	decryptedPAN := p.DecryptPIICredentials(cardReq.CardInfo.EncryptedPAN)
	if decryptedPAN == "" {
		slog.Error("handle.card.payment.decrypt_failed", "tx_id", cardReq.TransactionID)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusFailedDependency, nil)
		return
	}

	if cachedStatus, found := p.checkIdempotency(ctx, cardReq.TransactionID); found {
		slog.Info("handle.card.payment.idempotent", "tx_id", cardReq.TransactionID, "cached_status", cachedStatus)
		if cachedStatus == models.TransactionStatusSuccess || cachedStatus == models.TransactionStatusPending {
			utils.SendSuccessResponse(w, "Payment Already Processed", map[string]any{
				"transactionId": cardReq.TransactionID,
				"status":        cachedStatus,
				"paymentMode":   models.PaymentModeCard,
			}, http.StatusOK)
		} else {
			utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusBadRequest, nil)
		}
		return
	}

	// Reserve the transaction ID before processing to prevent duplicate inserts on retry
	p.setIdempotency(cardReq.TransactionID, models.TransactionStatusPending)

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

	response, err := p.ProcessPayment(ctx, req)
	if err != nil {
		slog.Error("handle.card.payment.failed", "tx_id", req.TransactionID, "err", err)
		p.setIdempotency(req.TransactionID, models.TransactionStatusFailed)
		//p.Audit.LogError(req.TransactionID, req.FromAccount, err)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusFailedDependency, nil)
		return
	}

	p.setIdempotency(req.TransactionID, response.Status)
	//p.Audit.LogTransfer(req.TransactionID, req.FromAccount, req.BeneficiaryAccountNumber, req.Amount, response.Status)

	slog.Info("handle.card.payment.response", "tx_id", req.TransactionID, "success", response.Success, "status", response.Status)

	if response.Success {
		utils.SendSuccessResponse(w, utils.PaymentSuccessful, response, http.StatusOK)
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
