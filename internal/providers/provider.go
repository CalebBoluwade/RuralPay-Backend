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
		Audit:           hsm.NewAuditLogger(),
		Validator:       services.NewValidationHelper(),
		notificationSVC: services.NewNotificationService(),
	}
}

func (base *BasePaymentProvider) HandlePaymentRequest(w http.ResponseWriter, r *http.Request, provider PaymentProvider) {
	slog.Info("payment.handle.start", "mode", provider.GetPaymentMode())

	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	var req models.PaymentRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		slog.Error("payment.handle.decode_failed", "error", err)
		utils.SendErrorResponse(w, "Unable To Process This Request At This Time", http.StatusBadRequest, nil)
		return
	}
	slog.Info("payment.handle.request", "tx_id", req.TransactionID, "from", req.FromAccount, "to", req.BeneficiaryAccountNumber, "amount", req.Amount)

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		slog.Warn("payment.handle.multiple_json_objects")
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	req.UserID = strconv.Itoa(userID)
	req.PaymentMode = provider.GetPaymentMode()

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

	response, err := provider.ProcessPayment(r.Context(), &req)
	if err != nil {
		slog.Error("payment.handle.failed", "tx_id", req.TransactionID, "error", err)
		base.Audit.LogError(req.TransactionID, req.FromAccount, err)

		go func() {
			user := base.fetchUserForNotification(userID)

			// Sender
			err := base.notificationSVC.SendPaymentNotification(&models.TransactionRecord{
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
			}, user, models.PaymentFailed)

			if err != nil {
				slog.Error("Failed To Send Transaction Notification", "err", err)
			}
		}()

		utils.SendErrorResponse(w, utils.PaymentFailed, http.StatusBadRequest, nil)
		return
	}

	slog.Info("payment.handle.processed", "tx_id", req.TransactionID, "success", response.Success, "status", response.Status)
	base.Audit.LogTransfer(req.TransactionID, req.FromAccount, req.BeneficiaryAccountNumber, req.Amount, response.Status)

	go func() {
		user := base.fetchUserForNotification(userID)

		// Sender
		err = base.notificationSVC.SendPaymentNotification(&models.TransactionRecord{
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
		}, user, models.PaymentSent)

		// Receiver (If A Rural Pay User)
		err = base.notificationSVC.SendPaymentNotification(&models.TransactionRecord{
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
		}, user, models.PaymentReceived)

		if err != nil {
			slog.Error("Failed To Send Transaction Notification", "err", err)
		}
	}()

	if response.Success {
		utils.SendSuccessResponse(w, utils.ResponseMessage(response.Message), response, http.StatusOK)
	} else {
		slog.Warn("payment.handle.unsuccessful", "tx_id", req.TransactionID, "message", response.Message)
		utils.SendErrorResponse(w, utils.ResponseMessage(response.Message), http.StatusBadRequest, nil)
	}
}

func (base *BasePaymentProvider) fetchUserForNotification(id int) *models.User {
	user := &models.User{ID: id}
	var pushToken sql.NullString
	err := base.DB.QueryRow(`
		SELECT email, phone_number, push_token, first_name
		FROM users WHERE id = $1
	`, id).Scan(&user.Email, &user.PhoneNumber, &pushToken, &user.FirstName)
	if err != nil {
		slog.Error("payment.fetch_user_failed", "user_id", id, "error", err)
		return user
	}
	user.ExpoPushToken = pushToken.String
	return user
}

func (base *BasePaymentProvider) checkIdempotency(txID string) (string, bool) {
	ctx := context.Background()
	key := fmt.Sprintf("idempotency:%s", txID)
	status, err := base.Redis.Get(ctx, key).Result()
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
