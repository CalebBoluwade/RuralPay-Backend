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
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/services"
)

type PaymentProvider interface {
	ProcessPayment(ctx context.Context, req *models.PaymentRequest) (*models.PaymentResponse, error)
	ValidatePayment(ctx context.Context, req *models.PaymentRequest) error
	GetPaymentMode() models.PaymentMode
	HandlePayment(w http.ResponseWriter, r *http.Request)
}

type BasePaymentProvider struct {
	DB              *sql.DB
	Redis           *redis.Client
	HSM             hsm.HSMInterface
	Audit           *hsm.AuditLogger
	Validator       *services.ValidationHelper
	notificationSVC *services.NotificationService
}

func NewBasePaymentProvider(db *sql.DB, redis *redis.Client, hsmInstance hsm.HSMInterface) *BasePaymentProvider {
	return &BasePaymentProvider{
		DB:              db,
		Redis:           redis,
		HSM:             hsmInstance,
		Audit:           hsm.NewAuditLogger(),
		Validator:       services.NewValidationHelper(),
		notificationSVC: services.NewNotificationService(),
	}
}

func (base *BasePaymentProvider) HandlePaymentRequest(w http.ResponseWriter, r *http.Request, provider PaymentProvider) {
	log.Printf("[BasePaymentProvider] Starting Payment Request Handling for Mode: %s", provider.GetPaymentMode())

	userID, ok := r.Context().Value("userID").(string)
	if !ok || userID == "" {
		log.Printf("[BasePaymentProvider] Unauthorized: userID not found in context")
		services.SendErrorResponse(w, "Unauthorized", http.StatusUnauthorized, nil)
		return
	}
	log.Printf("[BasePaymentProvider] Context User ID: %s", userID)

	var req models.PaymentRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		log.Printf("[BasePaymentProvider] Error Decoding Payment Request: %v", err)
		services.SendErrorResponse(w, "Unable To Process This Request At This Time", http.StatusBadRequest, nil)
		return
	}
	log.Printf("[BasePaymentProvider] Request decoded: txID=%s, from=%s, to=%s, amount=%d", req.TransactionID, req.FromAccount, req.ToAccount, req.Amount)

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		log.Printf("[BasePaymentProvider] Multiple JSON objects detected in request body")
		services.SendErrorResponse(w, "Request body must only contain a single JSON object", http.StatusBadRequest, nil)
		return
	}

	req.UserID = userID
	req.PaymentMode = provider.GetPaymentMode()

	if req.TransactionID == "" {
		req.TransactionID = fmt.Sprintf("PAY-%d", time.Now().UnixNano())
		log.Printf("[BasePaymentProvider] Generated transaction ID: %s", req.TransactionID)
	}

	log.Printf("[BasePaymentProvider] Verifying Account ownership: [Account]=%s, [User ID]=%s", req.FromAccount, userID)
	if err := base.verifyAccountOwnership(req.FromAccount, userID); err != nil {
		log.Printf("[BasePaymentProvider] Account ownership verification failed: %v", err)
		services.SendErrorResponse(w, "Unauthorized: Account does not belong to user", http.StatusForbidden, nil)
		return
	}
	log.Printf("[BasePaymentProvider] Account ownership verified successfully")

	if cachedStatus, found := base.checkIdempotency(req.TransactionID); found {
		log.Printf("[PAYMENT] Idempotent request: %s, status: %s", req.TransactionID, cachedStatus)
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

	log.Printf("[PAYMENT] Processing %s Payment: [TransactionID]=%s, [SENDER]=%s, [RECEIVER]=%s, [AMOUNT]=%d",
		req.PaymentMode, req.TransactionID, req.FromAccount, req.ToAccount, req.Amount)

	response, err := provider.ProcessPayment(r.Context(), &req)
	if err != nil {
		log.Printf("[PAYMENT] Payment Failed: %v", err)
		base.Audit.LogError(req.TransactionID, req.FromAccount, err)
		services.SendErrorResponse(w, "Payment Processing Failed: "+err.Error(), http.StatusBadRequest, nil)
		return
	}

	log.Printf("[BasePaymentProvider] Payment Processed. [Success]=%v, [Status]=%s, [Message]=%s", response.Success, response.Status, response.Message)
	base.setIdempotency(req.TransactionID, response.Status)
	base.Audit.LogTransfer(req.TransactionID, req.FromAccount, req.ToAccount, req.Amount, response.Status)

	id, _ := strconv.Atoi(userID)

	go base.notificationSVC.SendPaymentNotification(&models.Transaction{
		TransactionID: req.TransactionID,
		FromAccountID: req.FromAccount,
		ToAccountID:   req.ToAccount,
		Amount:        req.Amount,
		Status:        response.Status,
	}, &models.User{
		ID: id,
	}, models.PaymentSent)

	w.Header().Set("Content-Type", "application/json")
	if response.Success {
		w.WriteHeader(http.StatusOK)
	} else {
		log.Printf("[BasePaymentProvider] Returning 400 response: %s", response.Message)
		w.WriteHeader(http.StatusBadRequest)
	}
	json.NewEncoder(w).Encode(response)
}

func (b *BasePaymentProvider) checkIdempotency(txID string) (string, bool) {
	ctx := context.Background()
	key := fmt.Sprintf("idempotency:%s", txID)
	status, err := b.Redis.Get(ctx, key).Result()
	if err == nil {
		return status, true
	}
	return "", false
}

func (base *BasePaymentProvider) setIdempotency(txID, status string) {
	ctx := context.Background()
	key := fmt.Sprintf("idempotency:%s", txID)
	base.Redis.SetEX(ctx, key, status, 24*time.Hour)
}

func (base *BasePaymentProvider) verifyAccountOwnership(accountIdentifier, userID string) error {
	log.Printf("[BasePaymentProvider] Verifying ownership: account=%s, userID=%s", accountIdentifier, userID)

	var ownerID int
	err := base.DB.QueryRow(`
		SELECT user_id 
		FROM accounts
		WHERE (account_id = $1 OR card_id = $1) AND user_id IS NOT NULL
		LIMIT 1
	`, accountIdentifier).Scan(&ownerID)

	if err == sql.ErrNoRows {
		log.Printf("[BasePaymentProvider] Account not found in accounts table, checking cards table")
		err = base.DB.QueryRow(`
			SELECT c.user_id 
			FROM accounts a
			JOIN cards c ON a.card_id = c.card_id
			WHERE a.account_id = $1 OR a.card_id = $1
			LIMIT 1
		`, accountIdentifier).Scan(&ownerID)
	}

	if err != nil {
		if err == sql.ErrNoRows {
			log.Printf("[BasePaymentProvider] Account not found: %s", accountIdentifier)
			return errors.New("account not found")
		}
		log.Printf("[BasePaymentProvider] Database error during ownership verification: %v", err)
		return errors.New("failed to verify account ownership")
	}

	log.Printf("[BasePaymentProvider] Found account owner: %d, expected: %s", ownerID, userID)
	if fmt.Sprintf("%d", ownerID) != userID {
		log.Printf("[BasePaymentProvider] Ownership mismatch: owner=%d, requester=%s", ownerID, userID)
		return errors.New("account does not belong to user")
	}

	return nil
}
