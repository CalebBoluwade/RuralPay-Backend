package providers

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
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/services"
	"github.com/ruralpay/backend/internal/utils"
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
		Audit:           hsm.NewAuditLogger(db, hsmInstance),
		Validator:       services.NewValidationHelper(),
		notificationSVC: services.NewNotificationService(db),
	}
}

func (base *BasePaymentProvider) HandlePaymentRequest(w http.ResponseWriter, r *http.Request, provider PaymentProvider) {
	slog.Info(fmt.Sprintf("payment.handle.start.[%s]", provider.GetPaymentMode()))

	ctx := r.Context()

	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, ctx)

	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1_048_576))
	if err != nil {
		slog.Error("payment.handle.read_body_failed", "error", err.Error(), "raw_request", string(bodyBytes))
		utils.SendErrorResponse(w, utils.PaymentFailed, http.StatusBadRequest, nil)
		return
	}

	// restore body so decoder can still use it
	r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	// r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	defer r.Body.Close()

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req models.PaymentRequest
	if err := dec.Decode(&req); err != nil {
		slog.Error("payment.handle.decode_failed", "error", err, "body", r.Body)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusBadRequest, nil)
		return
	}
	slog.Info("payment.handle.request", "tx_id", req.TransactionID, "from", req.FromAccount, "to", req.BeneficiaryAccountNumber, "amount", req.Amount)

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		slog.Warn(fmt.Sprintf("payment.handle.multiple_json_objects.%s", provider.GetPaymentMode()), "mode", provider.GetPaymentMode())
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	req.UserID = strconv.Itoa(userID)
	req.PaymentMode = provider.GetPaymentMode()
	req.IPAddress = utils.RealIP(r)

	if req.TransactionID == "" {
		req.TransactionID = fmt.Sprintf("PAY-%d", time.Now().UnixNano())
	}

	if err := base.verifyAccountAndKYC(req.FromAccount, userID); err != nil {
		slog.Warn("payment.handle.preflight_failed", "account", req.FromAccount, "user_id", userID, "error", err)
		if err.Error() == "KYC verification required" {
			utils.SendErrorResponse(w, "KYC verification required before making payments", http.StatusUnprocessableEntity, nil)
		} else {
			utils.SendErrorResponse(w, "Unauthorized: Account does not belong to user", http.StatusUnprocessableEntity, nil)
		}
		return
	}

	slog.Info("payment.handle.processing", "mode", req.PaymentMode, "tx_id", req.TransactionID, "amount", req.Amount)

	response, err := provider.ProcessPayment(ctx, &req)
	if err != nil {
		go func() {
			// Sender
			err := base.notificationSVC.SendPaymentNotification(ctx, &models.TransactionRecord{
				TransactionID: req.TransactionID,
				FromAccountID: req.FromAccount,
				ToAccountID:   req.BeneficiaryAccountNumber,
				Amount:        req.Amount,
				Currency:      req.Currency,
				Status:        response.Status,
				CreatedAt:     time.Now(),
				Metadata: map[string]any{
					"beneficiaryName":     req.BeneficiaryAccountName,
					"paymentMode":         req.PaymentMode,
					"beneficiaryBankName": req.BeneficiaryBankName,
				},
			}, models.PaymentFailed)

			if err != nil {
				slog.Error("Failed To Send Transaction Notification", "err", err)
			}
		}()

		event := models.AuditEvent{
			Timestamp: time.Now(),
			EventType: "FAILED_TRANSACTION",
			TxRequest: &req,
			Error:     err.Error(),
		}

		if err = base.Audit.LogFailedTransaction(ctx, event); err != nil {
			slog.Error("Failed To Log Failed Transaction", "err", err)
		}

		slog.Error("payment.processing.failed", "tx_id", req.TransactionID, "error", err, "res", response)

		utils.SendErrorResponse(w, response.Message, http.StatusBadRequest, nil)
		return
	}

	slog.Info("payment.handle.processed", "tx_id", req.TransactionID, "success", response.Success, "status", response.Status)

	go func() {
		// Sender
		err = base.notificationSVC.SendPaymentNotification(ctx, &models.TransactionRecord{
			TransactionID: req.TransactionID,
			FromAccountID: req.FromAccount,
			ToAccountID:   req.BeneficiaryAccountNumber,
			Amount:        req.Amount,
			Currency:      req.Currency,
			Status:        response.Status,
			CreatedAt:     time.Now(),
			Metadata: map[string]any{
				"beneficiaryName":     req.BeneficiaryAccountName,
				"paymentMode":         req.PaymentMode,
				"beneficiaryBankName": req.BeneficiaryBankName,
				"reference":           response.Reference,
			},
		}, models.PaymentSent)

		// Receiver (If A Rural Pay User)
		err = base.notificationSVC.SendPaymentNotification(ctx, &models.TransactionRecord{
			TransactionID: req.TransactionID,
			FromAccountID: req.FromAccount,
			ToAccountID:   req.BeneficiaryAccountNumber,
			Amount:        req.Amount,
			Currency:      req.Currency,
			Status:        response.Status,
			CreatedAt:     time.Now(),
			Metadata: map[string]any{
				"oldBalance":          0.00,
				"newBalance":          0.00 + float64(req.Amount),
				"beneficiaryName":     req.BeneficiaryAccountName,
				"paymentMode":         req.PaymentMode,
				"beneficiaryBankName": req.BeneficiaryBankName,
				"reference":           response.Reference,
			},
		}, models.PaymentReceived)

		if err != nil {
			slog.Error("Failed To Send Transaction Notification", "err", err)
		}
	}()

	if response.Success {
		utils.SendSuccessResponse(w, response.Message, response, http.StatusOK)
	} else {
		slog.Warn("payment.handle.unsuccessful", "tx_id", req.TransactionID, "message", response.Message)
		utils.SendErrorResponse(w, response.Message, http.StatusBadRequest, nil)
	}
}

func (base *BasePaymentProvider) checkIdempotency(ctx context.Context, txID string) (models.TransactionStatus, bool) {
	key := fmt.Sprintf("idempotency:%s", txID)

	val, err := base.Redis.Get(ctx, key).Result()
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

func (base *BasePaymentProvider) setIdempotency(txID string, status models.TransactionStatus) {
	ctx := context.Background()
	key := fmt.Sprintf("idempotency:%s", txID)
	base.Redis.SetEX(ctx, key, status, 24*time.Hour)
}

func (base *BasePaymentProvider) verifyAccountAndKYC(accountIdentifier string, userID int) error {
	var ownerID int
	var kycStatus string
	var kycLevel int

	err := base.DB.QueryRow(`
		SELECT a.user_id, COALESCE(ul.kyc_status, 'UNVERIFIED'), COALESCE(ul.kyc_level, 0)
		FROM accounts a
		LEFT JOIN user_limits ul ON ul.user_id = a.user_id
		WHERE a.account_id = $1 AND a.user_id IS NOT NULL
		LIMIT 1
	`, accountIdentifier).Scan(&ownerID, &kycStatus, &kycLevel)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("account not found")
		}
		slog.Error("payment.verify_account_kyc.db_error", "error", err)
		return errors.New("failed to verify account")
	}

	if fmt.Sprintf("%d", ownerID) != strconv.Itoa(userID) {
		slog.Warn("payment.verify_account_kyc.ownership_mismatch", "owner", ownerID, "requester", userID)
		return errors.New("account does not belong to user")
	}

	if kycStatus != "VERIFIED" || kycLevel < 1 {
		slog.Warn("payment.verify_account_kyc.kyc_blocked", "user_id", userID, "kyc_status", kycStatus, "kyc_level", kycLevel)
		return errors.New("KYC verification required")
	}

	return nil
}
