package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
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
}

func NewPaymentHandler(db *sql.DB, redis *redis.Client, hsm hsm.HSMInterface) *PaymentHandler {
	return &PaymentHandler{
		providerMap: map[models.PaymentMode]providers.PaymentProvider{
			models.PaymentModeCard:         providers.NewCardPaymentProvider(db, redis, hsm),
			models.PaymentModeQR:           providers.NewQRPaymentProvider(db, redis, hsm),
			models.PaymentModeBankTransfer: providers.NewBankTransferPaymentProvider(db, redis, hsm),
			models.PaymentModeUSSD:         providers.NewUSSDPaymentProvider(db, redis, hsm),
			models.PaymentModeVoice:        providers.NewVoicePaymentProvider(db, redis, hsm),
			models.PaymentModeAirtimeData:  providers.NewAirtimeDataProvider(db, redis, hsm),
		},
		validator: services.NewValidationHelper(),
	}
}

// HandlePayment processes a payment request by routing to the appropriate provider
// @Summary Process Payment
// @Description Process a payment using the specified payment mode (CARD, QR, BANK_TRANSFER, USSD, VOICE)
// @Tags Payments
// @Accept json
// @Produce json
// @Param payment body providers.PaymentRequest true "Payment request"
// @Success 200 {object} providers.PaymentResponse
// @Failure 400 {object} services.ErrorResponse
// @Failure 401 {object} services.ErrorResponse
// @Failure 403 {object} services.ErrorResponse
// @Failure 500 {object} services.ErrorResponse
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
		log.Printf("[PaymentHandler] Error unmarshaling request body: %v, body: %s", err, string(body))
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

	log.Printf("[PaymentHandler] Routing Transaction [%s] to [%s] Provider", req.TransactionID, req.PaymentMode)
	r.Body = io.NopCloser(bytes.NewBuffer(body))
	provider.HandlePayment(w, r)
	log.Printf("[PaymentHandler] Provider Processing Completed --> Transaction [%s]: Payment Mode [%s]", req.TransactionID, req.PaymentMode)
}
