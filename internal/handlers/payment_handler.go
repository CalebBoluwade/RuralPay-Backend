package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/providers"
	"github.com/ruralpay/backend/internal/services"
	"github.com/ruralpay/backend/internal/utils"
)

type PaymentHandler struct {
	providerMap map[models.PaymentMode]providers.PaymentProvider
	validator   *services.ValidationHelper
	redis       *redis.Client
}

func NewPaymentHandler(db *sql.DB, redisClient *redis.Client, hsm hsm.HSMInterface) *PaymentHandler {
	return &PaymentHandler{
		providerMap: map[models.PaymentMode]providers.PaymentProvider{
			models.PaymentModeCard:         providers.NewCardPaymentProvider(db, redisClient, hsm),
			models.PaymentModeQR:           providers.NewQRPaymentProvider(db, redisClient, hsm),
			models.PaymentModeBankTransfer: providers.NewBankTransferPaymentProvider(db, redisClient, hsm),
			models.PaymentModeUSSD:         providers.NewUSSDPaymentProvider(db, redisClient, hsm),
			models.PaymentModeVoice:        providers.NewVoicePaymentProvider(db, redisClient, hsm),
			models.PaymentModeAirtimeData:  providers.NewAirtimeDataProvider(db, redisClient, hsm),
		},
		validator: services.NewValidationHelper(),
		redis:     redisClient,
	}
}

func (h *PaymentHandler) checkIdempotency(txID string) (string, bool) {
	if h.redis == nil {
		return "", false
	}
	status, err := h.redis.Get(context.Background(), fmt.Sprintf("idempotency:%s", txID)).Result()
	if err == nil {
		return status, true
	}
	return "", false
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
// @Router /payments [post]
// @Security BearerAuth
func (h *PaymentHandler) HandlePayment(w http.ResponseWriter, r *http.Request) {
	log.Printf("[PaymentHandler] Incoming Request: [Method]=%s, [Path]=%s, [SourceIP]=%s", r.Method, r.URL.Path, r.RemoteAddr)

	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[PaymentHandler] Error Reading Request Body: %v", err)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusBadRequest, nil)
		return
	}
	log.Printf("[PaymentHandler] Request Body Received: %s", string(body))

	var req models.PaymentRequest
	if err := json.Unmarshal(body, &req); err != nil {
		log.Printf("[PaymentHandler] Error Unmarshalling Request Body: %v, body: %s", err, string(body))
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}
	log.Printf("[PaymentHandler] [Payment Mode]=%s", req.PaymentMode)

	if req.PaymentMode == "" {
		log.Printf("[PaymentHandler] Missing Payment Mode in request, body: %s", string(body))
		utils.SendErrorResponse(w, utils.InvalidPaymentMode, http.StatusBadRequest, nil)
		return
	}

	provider, exists := h.providerMap[req.PaymentMode]
	if !exists {
		log.Printf("[PaymentHandler] Invalid Payment Mode: %s", req.PaymentMode)
		utils.SendErrorResponse(w, utils.InvalidPaymentMode, http.StatusBadRequest, nil)
		return
	}

	if req.TransactionID != "" {
		if cachedStatus, found := h.checkIdempotency(req.TransactionID); found {
			log.Printf("[PaymentHandler] Idempotent request: tx=%s status=%s", req.TransactionID, cachedStatus)
			if cachedStatus == "COMPLETED" || cachedStatus == "PENDING" {
				utils.SendSuccessResponse(w, "Payment Already Processed", map[string]any{
					"transactionId": req.TransactionID,
					"status":        cachedStatus,
					"paymentMode":   req.PaymentMode,
				}, http.StatusOK)
			} else {
				utils.SendErrorResponse(w, "Payment Failed", http.StatusBadRequest, nil)
			}
			return
		}
	}

	log.Printf("[PaymentHandler] Routing Transaction [%s] to [%s] Provider", req.TransactionID, req.PaymentMode)
	r.Body = io.NopCloser(bytes.NewBuffer(body))
	provider.HandlePayment(w, r)
	log.Printf("[PaymentHandler] Provider Processing Completed --> Transaction [%s]: Payment Mode [%s]", req.TransactionID, req.PaymentMode)
}

