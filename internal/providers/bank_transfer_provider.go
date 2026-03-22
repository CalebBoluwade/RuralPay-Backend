package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"net/http"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/services"
	"github.com/ruralpay/backend/internal/utils"
)

type BankTransferPaymentProvider struct {
	*BasePaymentProvider
	iso20022Service *services.ISO20022Service
	ledgerService   *services.DoubleLedgerService
	acctService     *services.AccountService
	feePercentage   float64
	feeFixed        int64
}

func NewBankTransferPaymentProvider(db *sql.DB, redis *redis.Client, hsmInstance hsm.HSMInterface) *BankTransferPaymentProvider {
	return &BankTransferPaymentProvider{
		BasePaymentProvider: NewBasePaymentProvider(db, redis, hsmInstance),
		iso20022Service:     services.NewISO20022Service(),
		ledgerService:       services.NewDoubleLedgerService(db),
		acctService:         services.NewAccountService(db, redis),
		feePercentage:       0.5,
		feeFixed:            10,
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

	isValid2FA := p.acctService.ValidateUserOTP(req.UserID, req.OneTimeCode, "2FA-CODE")
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
		if err == sql.ErrNoRows {
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
			Status:        "FAILED",
			Message:       err.Error(),
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
		slog.Error("bank_transfer.process.sign_failed", "tx_id", req.TransactionID, "error", err)
		return nil, errors.New("security signing failed")
	}

	slog.Info("bank_transfer.process.ledger_transfer", "tx_id", req.TransactionID)
	if err := p.ledgerService.TransferTx(tx, req.FromAccount, req.BeneficiaryAccountNumber, req.TransactionID, totalAmount); err != nil {
		slog.Error("bank_transfer.process.ledger_failed", "tx_id", req.TransactionID, "error", err)
		return &models.PaymentResponse{
			Reference:     "-",
			Success:       false,
			TransactionID: req.TransactionID,
			Status:        "FAILED",
			Message:       err.Error(),
			PaymentMode:   models.PaymentModeBankTransfer,
			Timestamp:     time.Now(),
		}, err
	}

	slog.Info("bank_transfer.process.inserting", "tx_id", req.TransactionID)
	// Update metadata with signing info
	if req.Metadata == nil {
		req.Metadata = make(map[string]any)
	}
	req.Metadata["signing_nonce"] = nonce
	req.Metadata["signing_timestamp"] = timestamp
	metadata, _ := json.Marshal(req.Metadata)
	locationJSON, _ := json.Marshal(req.Location)

	_, err = tx.ExecContext(ctx, `
		WITH user_info AS (
			SELECT a.user_id 
			FROM accounts a 
			WHERE a.account_id = $2
			LIMIT 1
		),
		limit_update AS (
			UPDATE user_limits ul
			SET updated_at = NOW()
			FROM user_info ui
			WHERE ul.user_id = ui.user_id
			RETURNING ul.user_id
		)
		INSERT INTO transactions 
		(transaction_id, debit_id, credit_id, amount, fee, total_amount, currency, narration, type, payment_mode, status, location, metadata, user_id, created_at, signature)
		SELECT $3, $2, $4, $5, $6, $1, $7, $8, $9, 'BANK_TRANSFER', 'PENDING', $10, $11, ui.user_id, NOW(), $12
		FROM user_info ui
	`, totalAmount, req.FromAccount, req.TransactionID, req.BeneficiaryAccountNumber, req.Amount, fee, req.Currency, req.Narration, req.TxType, locationJSON, metadata, signature)

	if err != nil {
		slog.Error("bank_transfer.process.insert_failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		slog.Error("bank_transfer.process.commit_failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}

	slog.Info("bank_transfer.process.success", "tx_id", req.TransactionID)
	// settlementErr, shouldReverse := p.sendToSettlement(req)
	// if settlementErr != nil {
	// 	if shouldReverse {
	// 		log.Printf("[BankTransferProvider] Settlement Failed. Reversing transaction --> [Transaction ID]=%s %v", req.TransactionID, settlementErr)
	// 		if revErr := p.ledgerService.Reverse(req.TransactionID); revErr != nil {
	// 			log.Printf("[BankTransferProvider] Reversal Failed --> [Transaction ID]=%s %v", req.TransactionID, revErr)
	// 		}
	// 	} else {
	// 		log.Printf("[BankTransferProvider] Settlement Failed (retryable). Transaction remains pending --> [Transaction ID]=%s %v", req.TransactionID, settlementErr)
	// 	}
	// 	return &models.PaymentResponse{
	// 		Success:       false,
	// 		TransactionID: req.TransactionID,
	// 		Status:        "FAILED",
	// 		Message:       fmt.Sprintf("Settlement Failed: %v", settlementErr),
	// 		PaymentMode:   models.PaymentModeBankTransfer,
	// 		Timestamp:     time.Now(),
	// 	}, nil
	// }

	return &models.PaymentResponse{
		Success:       true,
		Reference:     "000000000000000000000000000",
		TransactionID: req.TransactionID,
		Status:        "COMPLETED",
		Message:       "Bank transfer successful",
		PaymentMode:   models.PaymentModeBankTransfer,
		Timestamp:     time.Now(),
	}, nil
}

func (p *BankTransferPaymentProvider) calculateFee(amount int64) int64 {
	fee := int64(float64(amount) * p.feePercentage / 100)
	return fee + p.feeFixed
}

func (p *BankTransferPaymentProvider) sendToSettlement(req *models.PaymentRequest) (error, bool) {
	slog.Info("bank_transfer.settlement.start", "tx_id", req.TransactionID)
	modelTx := &models.TransactionRecord{
		TransactionID: req.TransactionID,
		FromAccountID: req.FromAccount,
		ToAccountID:   req.BeneficiaryAccountNumber,
		Type:          string(req.TxType),
		Amount:        req.Amount,
		Currency:      req.Currency,
		Status:        "PENDING",
	}

	if bankCode, ok := req.Metadata["toBankCode"].(string); ok {
		modelTx.ToBankCode = bankCode
	}

	doc, err := p.iso20022Service.ConvertTransaction(modelTx)
	if err != nil {
		slog.Error("bank_transfer.settlement.iso_conversion_failed", "tx_id", req.TransactionID, "error", err)
		if _, dbErr := p.DB.Exec(`UPDATE transactions SET status = $1 WHERE transaction_id = $2`, "FAILED_ISO_CONVERSION", req.TransactionID); dbErr != nil {
			slog.Error("bank_transfer.settlement.status_update_failed", "tx_id", req.TransactionID, "error", dbErr)
		}
		return err, true
	}

	slog.Info("bank_transfer.settlement.sending", "tx_id", req.TransactionID)
	resp, err := p.iso20022Service.SendToSettlement(doc)
	if err != nil {
		slog.Error("bank_transfer.settlement.failed", "tx_id", req.TransactionID, "error", err)
		shouldReverse := p.shouldReverseOnSettlementFailure(err, &resp)
		if shouldReverse {
			if _, dbErr := p.DB.Exec(`UPDATE transactions SET status = $1 WHERE transaction_id = $2`, "FAILED_SETTLEMENT", req.TransactionID); dbErr != nil {
				slog.Error("bank_transfer.settlement.status_update_failed", "tx_id", req.TransactionID, "error", dbErr)
			}
		} else {
			if _, dbErr := p.DB.Exec(`UPDATE transactions SET status = $1 WHERE transaction_id = $2`, "PENDING_RETRY", req.TransactionID); dbErr != nil {
				slog.Error("bank_transfer.settlement.status_update_failed", "tx_id", req.TransactionID, "error", dbErr)
			}
		}
		return err, shouldReverse
	}

	slog.Info("bank_transfer.settlement.success", "tx_id", req.TransactionID, "status", resp.Status)
	respJSON, _ := json.Marshal(resp)
	if _, dbErr := p.DB.Exec(`UPDATE transactions SET status = $1, settlement_response = $2 WHERE transaction_id = $3`, "SETTLED", respJSON, req.TransactionID); dbErr != nil {
		slog.Error("bank_transfer.settlement.status_update_failed", "tx_id", req.TransactionID, "error", dbErr)
	}
	return nil, false
}

func (p *BankTransferPaymentProvider) shouldReverseOnSettlementFailure(err error, resp *services.FundsTransferSettlementResponse) bool {
	if resp != nil {
		switch resp.Status {
		case "PENDING", "PROCESSING":
			return false
		case "REJECTED", "FAILED":
			return true
		}
	}
	return false
}

func (p *BankTransferPaymentProvider) HandlePayment(w http.ResponseWriter, r *http.Request) {
	p.BasePaymentProvider.HandlePaymentRequest(w, r, p)
}
