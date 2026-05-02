package providers

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"net/http"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/services"
	"github.com/ruralpay/backend/internal/utils"
)

type BankTransferPaymentProvider struct {
	*BasePaymentProvider
	NIBSSClient   *services.NIBSSClient
	NIPService    *services.NIBSSNIPService
	ledgerService *services.DoubleLedgerService
	acctService   *services.AccountService
	feePercentage float64
	feeFixed      int64
}

func NewBankTransferPaymentProvider(db *sql.DB, redis *redis.Client, hsmInstance hsm.HSMInterface) *BankTransferPaymentProvider {
	return &BankTransferPaymentProvider{
		BasePaymentProvider: NewBasePaymentProvider(db, redis, hsmInstance),
		ledgerService:       services.NewDoubleLedgerService(db),

		NIBSSClient:   services.NewNIBSSClient(redis),
		NIPService:    services.NewNIBSSNIPService(),
		acctService:   services.NewAccountService(db, redis),
		feePercentage: 0.5,
		feeFixed:      10,
	}
}

func (p *BankTransferPaymentProvider) GetPaymentMode() models.PaymentMode {
	return models.PaymentModeBankTransfer
}

var accountNumberRe = regexp.MustCompile(`[^a-zA-Z0-9]`)

func (p *BankTransferPaymentProvider) sanitizeRequest(req *models.PaymentRequest) error {
	req.FromAccount = accountNumberRe.ReplaceAllString(strings.TrimSpace(req.FromAccount), "")
	req.BeneficiaryAccountNumber = accountNumberRe.ReplaceAllString(strings.TrimSpace(req.BeneficiaryAccountNumber), "")
	req.Narration = strings.TrimSpace(req.Narration)
	if utf8.RuneCountInString(req.Narration) > 100 {
		return errors.New("narration must not exceed 100 characters")
	}
	if len(req.FromAccount) > 20 {
		return errors.New("source account number too long")
	}
	if len(req.BeneficiaryAccountNumber) > 20 {
		return errors.New("destination account number too long")
	}
	return nil
}

func (p *BankTransferPaymentProvider) ValidatePayment(ctx context.Context, req *models.PaymentRequest) error {
	slog.Info("bank_transfer.validate", "from", req.FromAccount, "to", req.BeneficiaryAccountNumber, "amount", req.Amount, "type", req.TxType)

	if err := p.sanitizeRequest(req); err != nil {
		return err
	}

	isValid2FA := p.acctService.ValidateUser2FA(ctx, req.UserID, req.OneTimeCode, req.TwoFAType)
	if !isValid2FA {
		slog.Warn("account.verify_otp.not_found_or_expired")
		return errors.New(utils.MultiFactorAuthError.Response())
	}

	if req.FromAccount == "" {
		return errors.New("source account is required")
	}
	if req.BeneficiaryAccountNumber == "" {
		return errors.New("destination account is required")
	}
	if req.FromAccount == req.BeneficiaryAccountNumber {
		return errors.New("cannot transfer to same account")
	}
	if req.Amount <= 0 {
		return errors.New("amount must be positive")
	}

	var balance int64
	var status string
	err := p.DB.QueryRowContext(ctx, `SELECT balance, status FROM accounts WHERE account_id = $1`, req.FromAccount).Scan(&balance, &status)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("source account not found")
		}
		slog.Error("bank_transfer.validate.db_error", "error", err)
		return errors.New(utils.ValidationError.Response())
	}

	if status != "ACTIVE" {
		return errors.New("Source Account Not Active")
	}

	fee := p.calculateFee(req.Amount)
	totalAmount := req.Amount + fee
	slog.Info("bank_transfer.validate.fee", "tx_id", req.TransactionID, "amount", req.Amount, "fee", fee, "total", totalAmount, "balance", balance)

	if balance < totalAmount {
		return errors.New("Insufficient Balance")
	}

	return nil
}

func (p *BankTransferPaymentProvider) ProcessPayment(ctx context.Context, req *models.PaymentRequest) (*models.PaymentResponse, error) {
	slog.Info("bank_transfer.process.start", "tx_id", req.TransactionID)

	if err := p.ValidatePayment(ctx, req); err != nil {
		slog.Warn("bank_transfer.process.validation_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        models.TransactionStatusFailed,
			Message:       utils.ResponseMessage(err.Error()),
			PaymentMode:   models.PaymentModeBankTransfer,
			Timestamp:     time.Now(),
		}, err
	}

	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("bank_transfer.process.tx_begin_failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}
	defer tx.Rollback()

	fee := p.calculateFee(req.Amount)
	totalAmount := req.Amount + fee

	slog.Info("bank_transfer.process.ledger_transfer", "tx_id", req.TransactionID)
	if err := p.ledgerService.TransferTx(ctx, tx, req.FromAccount, req.BeneficiaryAccountNumber, req.TransactionID, totalAmount); err != nil {
		slog.Error("bank_transfer.process.ledger_failed", "tx_id", req.TransactionID, "error", err.Error())
		return &models.PaymentResponse{
			Reference:     "-",
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        models.TransactionStatusFailed,
			Message:       utils.InternalServiceError,
			PaymentMode:   models.PaymentModeBankTransfer,
			Timestamp:     time.Now(),
		}, err
	}

	sessionId := utils.GenerateNIPSessionId(p.NIPService.GetNIPBankCode())

	fundsTransferResult, err := p.NIBSSClient.FundsTransfer.DoTransaction(ctx, sessionId, req)

	if err != nil {
		slog.Error("Bank_Transfer.Process.Funds_Transfer_Failed", "tx_id", req.TransactionID, "error", err, "response", fundsTransferResult)
		return &models.PaymentResponse{
			Reference:     sessionId,
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        models.TransactionStatusFailed,
			Message:       utils.PaymentFailed,
			PaymentMode:   models.PaymentModeBankTransfer,
			Timestamp:     time.Now(),
		}, err
	}

	if err = p.Audit.LogTransaction(ctx, tx, models.AuditEvent{
		Timestamp: time.Now(),
		EventType: "SUCCESSFUL_" + string(p.GetPaymentMode()) + "_TRANSATION",
		TxRequest: req,
	}); err != nil {
		slog.ErrorContext(ctx, "Bank_Transfer.Audit.Log.Failed", "tx_id", req.TransactionID, "error", err)
	}

	slog.InfoContext(ctx, "Bank_Transfer.Audit.Log.Success", "tx_id", req.TransactionID)

	// settlementErr, shouldReverse := p.sendToSettlement(ctx, req)
	// if settlementErr != nil {
	// 	if shouldReverse {
	// 		slog.Info(fmt.Sprintf("[BankTransferProvider] Settlement Failed. Reversing transaction --> [Transaction ID]=%s %v", req.TransactionID, settlementErr))
	// 		if revErr := p.ledgerService.Reverse(req.TransactionID); revErr != nil {
	// 			slog.Error(fmt.Sprintf("[BankTransferProvider] Reversal Failed --> [Transaction ID]=%s %v", req.TransactionID, revErr))
	// 		}
	// 	} else {
	// 		slog.Error(fmt.Sprintf("[BankTransferProvider] Settlement Failed (retryable). Transaction remains pending --> [Transaction ID]=%s %v", req.TransactionID, settlementErr))
	// 	}
	// 	return &models.PaymentResponse{
	// 		Success:       false,
	// 		TransactionID: req.TransactionID,
	// 		Status:        models.TransactionStatusFailed,
	// 		Message:       fmt.Sprintf("Settlement Failed: %v", settlementErr),
	// 		PaymentMode:   models.PaymentModeBankTransfer,
	// 		Timestamp:     time.Now(),
	// 	}, nil
	// }

	return &models.PaymentResponse{
		Success:       true,
		Reference:     fundsTransferResult.Reference,
		TransactionID: req.TransactionID,
		Status:        models.TransactionStatusSuccess,
		Message:       "Transaction Successful",
		PaymentMode:   models.PaymentModeBankTransfer,
		Timestamp:     time.Now(),
	}, nil
}

func (p *BankTransferPaymentProvider) calculateFee(amount int64) int64 {
	fee := int64(float64(amount) * p.feePercentage / 100)
	return fee + p.feeFixed
}

// func (p *BankTransferPaymentProvider) sendToSettlement(ctx context.Context, req *models.PaymentRequest) (error, bool) {
// 	slog.Info("bank_transfer.settlement.start", "tx_id", req.TransactionID)
// 	modelTx := &models.TransactionRecord{
// 		TransactionID: req.TransactionID,
// 		OriginatorAccount: req.FromAccount,
// 		BeneficiaryAccount:   req.BeneficiaryAccountNumber,
// 		Type:          string(req.TxType),
// 		Amount:        req.Amount,
// 		Currency:      req.Currency,
// 		Status:        models.TransactionStatusPending,
// 	}

// 	if bankCode, ok := req.Metadata["toBankCode"].(string); ok {
// 		modelTx.ToBankCode = bankCode
// 	}

// 	doc, err := p.iso20022Service.ConvertTransaction(modelTx)
// 	if err != nil {
// 		slog.Error("bank_transfer.settlement.iso_conversion_failed", "tx_id", req.TransactionID, "error", err)
// 		if _, dbErr := p.DB.ExecContext(ctx, `UPDATE transactions SET status = $1, updated_at = NOW() WHERE transaction_id = $2`, models.TransactionStatusISOCONVFailed, req.TransactionID); dbErr != nil {
// 			slog.Error("bank_transfer.settlement.status_update_failed", "tx_id", req.TransactionID, "error", dbErr)
// 		}
// 		return err, true
// 	}

// 	slog.Info("bank_transfer.settlement.sending", "tx_id", req.TransactionID)
// 	resp, err := p.iso20022Service.SendToSettlement(ctx, doc)
// 	if err != nil {
// 		slog.Error("bank_transfer.settlement.failed", "tx_id", req.TransactionID, "error", err)
// 		shouldReverse := p.shouldReverseOnSettlementFailure(resp)
// 		if shouldReverse {
// 			if _, dbErr := p.DB.ExecContext(ctx, `UPDATE transactions SET status = $1, updated_at = NOW() WHERE transaction_id = $2`, models.TransactionSettlementFailed, req.TransactionID); dbErr != nil {
// 				slog.Error("bank_transfer.settlement.status_update_failed", "tx_id", req.TransactionID, "error", dbErr)
// 			}
// 		} else {
// 			if _, dbErr := p.DB.ExecContext(ctx, `UPDATE transactions SET status = $1, updated_at = NOW() WHERE transaction_id = $2`, "PENDING_RETRY", req.TransactionID); dbErr != nil {
// 				slog.Error("bank_transfer.settlement.status_update_failed", "tx_id", req.TransactionID, "error", dbErr)
// 			}
// 		}
// 		return err, shouldReverse
// 	}

// 	slog.Info("bank_transfer.settlement.success", "tx_id", req.TransactionID, "status", resp.Status)
// 	respJSON, _ := json.Marshal(resp)
// 	if _, dbErr := p.DB.ExecContext(ctx, `UPDATE transactions SET status = $1, settlement_response = $2, updated_at = NOW() WHERE transaction_id = $3`, models.TransactionStatusSuccess, respJSON, req.TransactionID); dbErr != nil {
// 		slog.Error("bank_transfer.settlement.status_update_failed", "tx_id", req.TransactionID, "error", dbErr)
// 	}
// 	return nil, false
// }

func (p *BankTransferPaymentProvider) shouldReverseOnSettlementFailure(resp models.SettlementResult) bool {
	switch resp.Status {
	case "RJCT":
		return true
	case "PENDING", "PROCESSING", "ACCP":
		return false
	}
	return false
}

func (p *BankTransferPaymentProvider) HandlePayment(w http.ResponseWriter, r *http.Request) {
	p.BasePaymentProvider.HandlePaymentRequest(w, r, p)
}
