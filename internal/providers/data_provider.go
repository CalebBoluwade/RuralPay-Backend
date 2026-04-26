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

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/services"
	"github.com/ruralpay/backend/internal/utils"
)

type DataProvider struct {
	*BasePaymentProvider
	acctService *services.AccountService
}

func NewDataProvider(db *sql.DB, redis *redis.Client, hsmInstance hsm.HSMInterface) *DataProvider {
	return &DataProvider{
		BasePaymentProvider: NewBasePaymentProvider(db, redis, hsmInstance),
		acctService:         services.NewAccountService(db, redis),
	}
}

func (p *DataProvider) GetPaymentMode() models.PaymentMode {
	return models.PaymentModeData
}

func (p *DataProvider) ValidatePayment(ctx context.Context, req *models.PaymentRequest) error {
	isValid2FA := p.acctService.ValidateUser2FA(ctx, req.UserID, req.OneTimeCode, req.TwoFAType)
	if !isValid2FA {
		slog.Warn("account.verify_otp.not_found_or_expired")
		return errors.New(utils.MultiFactorAuthError.Response())
	}

	return nil
}

func (p *DataProvider) validateRequest(req *models.AirtimeDataRequest) error {
	if req.DebitAccount == "" {
		return errors.New("fromAccount is required")
	}
	if req.PhoneNumber == "" {
		return errors.New("phoneNumber is required")
	}
	if req.Network == "" {
		return errors.New("network is required")
	}
	if req.Service != "AIRTIME" && req.Service != "DATA" {
		return errors.New("serviceType must be AIRTIME or DATA")
	}
	if req.Service == "DATA" && req.DataPlan == "" {
		return errors.New("dataPlan is required for DATA serviceType")
	}
	if req.Amount <= 0 {
		return errors.New("amount must be positive")
	}
	return nil
}

func (p *DataProvider) ProcessPayment(ctx context.Context, req *models.PaymentRequest) (*models.PaymentResponse, error) {
	return nil, errors.New("use HandlePayment directly for airtime/data requests")
}

func (p *DataProvider) HandlePayment(w http.ResponseWriter, r *http.Request) {
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	var req models.AirtimeDataRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		slog.Error("airtime_data.handle.decode_failed", "error", err)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusBadRequest, nil)
		return
	}

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	if err := p.validateRequest(&req); err != nil {
		slog.Warn("airtime_data.handle.validation_failed", "error", err)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusBadRequest, nil)
		return
	}

	if err := p.verifyAccountAndKYC(req.DebitAccount, userID); err != nil {
		slog.Warn("airtime_data.handle.ownership_failed", "account", req.DebitAccount, "user_id", userID)
		utils.SendErrorResponse(w, "Unauthorized: Account Does Not Belong To User", http.StatusUnprocessableEntity, nil)
		return
	}

	ctx := r.Context()

	var balance int64
	var status string
	err := p.DB.QueryRowContext(ctx, `SELECT balance, status FROM accounts WHERE account_id = $1`, req.DebitAccount).Scan(&balance, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			utils.SendErrorResponse(w, "Account not found", http.StatusBadRequest, nil)
			return
		}
		slog.Error("airtime_data.handle.account_query_failed", "error", err)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusFailedDependency, nil)
		return
	}
	if status != "ACTIVE" {
		utils.SendErrorResponse(w, "Account Not Active", http.StatusBadRequest, nil)
		return
	}
	if balance < req.Amount {
		utils.SendErrorResponse(w, "Insufficient balance", http.StatusBadRequest, nil)
		return
	}

	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("airtime_data.handle.tx_begin_failed", "error", err)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusInternalServerError, nil)
		return
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance - $1 WHERE account_id = $2`, req.Amount, req.DebitAccount)
	if err != nil {
		slog.Error("airtime_data.handle.debit_failed", "tx_id", req.TransactionID, "error", err)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusFailedDependency, nil)
		return
	}

	metadata, _ := json.Marshal(map[string]any{
		"phoneNumber": req.PhoneNumber,
		"network":     req.Network,
		"serviceType": req.Service,
		"dataPlan":    req.DataPlan,
	})
	_, err = tx.ExecContext(ctx, `
		INSERT INTO transactions
		(transaction_id, debit_id, amount, total_amount, currency, narration, type, payment_mode, status, metadata, user_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'DEBIT', 'AIRTIME_DATA', 'COMPLETED', $7, $8, NOW())
	`, req.TransactionID, req.DebitAccount, req.Amount, req.Voucher.VoucherDiscountAmount+req.Amount, "NGN", req.Narration, metadata, strconv.Itoa(userID))
	if err != nil {
		slog.Error("airtime_data.handle.insert_failed", "tx_id", req.TransactionID, "error", err)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusInternalServerError, nil)
		return
	}

	if err := tx.Commit(); err != nil {
		slog.Error("airtime_data.handle.commit_failed", "tx_id", req.TransactionID, "error", err)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusInternalServerError, nil)
		return
	}

	p.setIdempotency(req.TransactionID, models.TransactionStatusSuccess)

	//p.Audit.LogTransfer(req.TransactionID, req.DebitAccount, req.PhoneNumber, req.Amount, "COMPLETED")

	slog.Info("airtime_data.handle.success", "tx_id", req.TransactionID)

	utils.SendSuccessResponse(w, "Airtime/Data Purchase Successful", map[string]any{
		"transactionId": req.TransactionID,
		"status":        models.TransactionStatusSuccess,
		"paymentMode":   models.PaymentModeAirtime,
	}, http.StatusOK)
}
