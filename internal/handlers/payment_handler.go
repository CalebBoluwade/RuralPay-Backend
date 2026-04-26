package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/providers"
	"github.com/ruralpay/backend/internal/services"
	"github.com/ruralpay/backend/internal/utils"
)

type PaymentHandler struct {
	db          *sql.DB
	providerMap map[models.PaymentMode]providers.PaymentProvider
	validator   *services.ValidationHelper
	bankService *services.BankService
	redis       *redis.Client
}

func NewPaymentHandler(db *sql.DB, redisClient *redis.Client, hsm hsm.HSMInterface, bankService *services.BankService) *PaymentHandler {
	return &PaymentHandler{
		db: db,
		providerMap: map[models.PaymentMode]providers.PaymentProvider{
			models.PaymentModeCard:         providers.NewCardPaymentProvider(db, redisClient, hsm),
			models.PaymentModeQR:           providers.NewQRPaymentProvider(db, redisClient, hsm),
			models.PaymentModeBankTransfer: providers.NewBankTransferPaymentProvider(db, redisClient, hsm),
			models.PaymentModeUSSD:         providers.NewUSSDPaymentProvider(db, redisClient, hsm),
			models.PaymentModeVoice:        providers.NewVoicePaymentProvider(db, redisClient, hsm),
			models.PaymentModeAirtime:      providers.NewAirtimeDataProvider(db, redisClient, hsm),
			models.PaymentModeData:         providers.NewDataProvider(db, redisClient, hsm),
		},
		bankService: bankService,
		validator:   services.NewValidationHelper(),
		redis:       redisClient,
	}
}

func (h *PaymentHandler) checkIdempotency(ctx context.Context, txID string) (models.TransactionStatus, bool) {
	key := fmt.Sprintf("idempotency:%s", txID)

	val, err := h.redis.Get(ctx, key).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			// Log real errors so you know if Redis is failing
			slog.Error("Redis error checking idempotency for %s: %v", txID, err)
		}
		return "", false
	}

	// Cast the string result to your custom Status type
	return models.TransactionStatus(val), true
}

// HandlePayment processes a payment request by routing to the appropriate provider
// @Summary Process Payment
// @Description Process a payment using the specified payment mode (CARD, QR, BANK_TRANSFER, USSD, VOICE)
// @Tags Payments
// @Accept json
// @Produce json
// @Param payment body models.PaymentRequest true "Payment request"
// @Success 200 {object} models.PaymentResponse
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /payment [post]
// @Security BearerAuth
func (h *PaymentHandler) HandlePayment(w http.ResponseWriter, r *http.Request) {
	// Read body bytes to allow multiple reads
	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1_048_576))
	if err != nil {
		slog.Error("payment.handler.read_body_failed", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	var req models.PaymentRequest
	dec := json.NewDecoder(bytes.NewBuffer(bodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		slog.Error("payment.handler.decode_failed", "err", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		slog.Warn("payment.handler.multiple_json_objects")
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	slog.Info(fmt.Sprintf("Incoming.Payment.handler. Transaction ID --> [%s] Mode --> [%s]", req.TransactionID, req.PaymentMode))

	if req.PaymentMode == "" {
		slog.Error("payment.handler.missing_payment_mode")
		utils.SendErrorResponse(w, utils.InvalidPaymentMode, http.StatusBadRequest, nil)
		return
	}

	provider, exists := h.providerMap[req.PaymentMode]
	if !exists {
		slog.Error("[PaymentHandler] Invalid Payment Mode: %s", "payment_mode", req.PaymentMode)
		utils.SendErrorResponse(w, utils.InvalidPaymentMode, http.StatusBadRequest, nil)
		return
	}

	if req.TransactionID != "" {
		if cachedStatus, found := h.checkIdempotency(r.Context(), req.TransactionID); found {
			slog.Debug(fmt.Sprintf("[PaymentHandler] Idempotent Request Check. Transaction [%s] Status [%s]", req.TransactionID, cachedStatus))

			if cachedStatus == models.TransactionStatusSuccess || cachedStatus == models.TransactionStatusPending {
				utils.SendSuccessResponse(w, "Payment Already Processed", map[string]any{
					"transactionId": req.TransactionID,
					"status":        cachedStatus,
					"paymentMode":   req.PaymentMode,
				}, http.StatusOK)
			} else {
				utils.SendErrorResponse(w, utils.PaymentFailed, http.StatusBadRequest, nil)
			}
			return
		}
	}

	// Restore body for provider to read
	r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	slog.Info("payment.handler.routing", "tx_id", req.TransactionID, "mode", req.PaymentMode)
	provider.HandlePayment(w, r)
	slog.Info("payment.handler.done", "tx_id", req.TransactionID, "mode", req.PaymentMode)
}

// GetBeneficiaries retrieves saved beneficiaries for the authenticated user
// @Summary Get beneficiaries
// @Description Retrieve all saved beneficiaries for the authenticated user
// @Tags Accounts
// @Produce json
// @Success 200 {object} object{beneficiaries=array}
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /payment/beneficiaries [get]
func (h *PaymentHandler) GetBeneficiaries(w http.ResponseWriter, r *http.Request) {
	slog.Info("account.GetBeneficiaries.start")
	ctx := r.Context()
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, ctx)
	cacheKey := fmt.Sprintf("beneficiaries:%d", userID)

	if h.redis != nil {
		if cached, err := h.redis.Get(ctx, cacheKey).Bytes(); err == nil {
			slog.Info("account.beneficiaries.cache_hit", "user_id", userID)
			w.Header().Set("Content-Type", "application/json")
			w.Write(cached)
			return
		}
		slog.Info("account.beneficiaries.cache_miss", "user_id", userID)
	}

	rows, err := h.db.QueryContext(ctx, `
		SELECT id, account_number, account_name, bank_name, bank_code
		FROM beneficiaries
		WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		slog.Error("account.beneficiaries.query_failed", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, "Failed to fetch beneficiaries", http.StatusFailedDependency, nil)
		return
	}
	defer rows.Close()

	var beneficiaries []map[string]any
	for rows.Next() {
		var id int
		var accountNumber, accountName, bankName, bankCode string
		if err := rows.Scan(&id, &accountNumber, &accountName, &bankName, &bankCode); err != nil {
			slog.Error("account.beneficiaries.scan_error", "user_id", userID, "error", err)
			continue
		}
		beneficiaries = append(beneficiaries, map[string]any{
			"id":            id,
			"accountNumber": accountNumber,
			"accountName":   accountName,
			"bankName":      bankName,
			"bankCode":      bankCode,
			"bankLogo":      h.bankService.LoadLogo(bankCode),
		})
	}

	slog.Info("account.get.beneficiaries.done", "user_id", userID, "count", len(beneficiaries))

	payload, _ := json.Marshal(map[string]any{"beneficiaries": beneficiaries})
	if h.redis != nil {
		slog.Info("account.get.beneficiaries.caching_result", "user_id", userID)
		h.redis.Set(ctx, cacheKey, payload, 10*time.Minute)
	}

	utils.SendSuccessResponse(w, "Returning Beneficiaries", beneficiaries, http.StatusOK)
}
