package services

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
)

type AccountService struct {
	db              *sql.DB
	nibssClient     *NIBSSClient
	redis           *redis.Client
	validator       *validator.Validate
	qrService       *QRService
	bankService     *BankService
	ussdService     *USSDService
	notificationSVC *NotificationService
	// nameEnquiry     NameEnquiryService
}

type LinkAccountRequest struct {
	BankCode      string `json:"bankCode" validate:"required"`
	AccountNumber string `json:"accountNumber" validate:"required"`
	IsPrimary     bool   `json:"isPrimary"`
}

type LinkedAccount struct {
	ID            int    `json:"id"`
	UserID        int    `json:"userId"`
	AccountNumber string `json:"accountNumber"`
	AccountName   string `json:"accountName"`
	BankName      string `json:"bankName"`
	BankCode      string `json:"bankCode"`
	IsPrimary     bool   `json:"isPrimary"`
}

// sanitizeLog strips control characters to prevent log injection.
func sanitizeAcct(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func NewAccountService(db *sql.DB, redisClient *redis.Client) *AccountService {
	return &AccountService{
		db:              db,
		redis:           redisClient,
		nibssClient:     NewNIBSSClient(redisClient),
		validator:       validator.New(),
		bankService:     NewBankService(db),
		qrService:       NewQRService(db, redisClient),
		notificationSVC: NewNotificationService(db),
		// nameEnquiry:     NewNameEnquiryService(),
	}
}

// LinkAccount Adds Users Account
// @Summary Adds Users Account
// @Description Adds Users Account
// @Tags Accounts
// @Produce json
// @Param LinkAccountRequest body LinkAccountRequest true "Link Account Request"
// @Success 200 {object} object{responseCode=string,accountId=string,accountName=string,status=string,source=string}
// @Failure 400 {object} utils.APIErrorResponse
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Security BearerAuth
// @Router /account/link [post]
func (s *AccountService) LinkAccount(w http.ResponseWriter, r *http.Request) {
	slog.Info("Account.Link_Account.Start")
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	var req LinkAccountRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		slog.Error("Account.Link_Account.Decode_Error", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		slog.Warn("Account.Link_Account.Multiple_Json_Objects")
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	// 1. Combined query: Check if account exists AND fetch user BVN (single DB round trip)
	var existingAccountID sql.NullInt64
	var userBVN string
	err := s.db.QueryRow(`
		SELECT 
			(SELECT account_id FROM accounts WHERE user_id = $1 AND account_id = $2 AND bank_code = $3 AND status = 'ACTIVE' LIMIT 1),
			COALESCE((SELECT bvn FROM users WHERE id = $1), '')
	`, userID, req.AccountNumber, req.BankCode).Scan(&existingAccountID, &userBVN)

	if err != nil {
		slog.Error("Account.Link_Account.Check_Account_Failed", "error", err)
		utils.SendErrorResponse(w, "Failed to Check Account", http.StatusFailedDependency, nil)
		return
	}

	if existingAccountID.Valid {
		// Account already exists
		slog.Warn("Account.Link_Account.Account_Already_Linked", "user_id", userID, "account_id", sanitizeAcct(req.AccountNumber), "bank_code", req.BankCode)
		utils.SendErrorResponse(w, "Account Already Linked To This User", http.StatusConflict, nil)
		return
	}

	nipCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	nameEnquiryResp, err := s.nibssClient.NameEnquiry.EnquireName(nipCtx, req.AccountNumber, req.BankCode)
	if err != nil {
		slog.Error("Account.Link_Account.Name_Enquiry_Failed", "error", err)
		if errors.Is(err, context.DeadlineExceeded) || utils.IsNetworkError(err) {
			utils.SendErrorResponse(w, utils.NIPServiceUnavailable, http.StatusServiceUnavailable, nil)
		} else {
			utils.SendErrorResponse(w, "Failed to Verify Account", http.StatusBadGateway, nil)
		}
		return
	}

	// Validate that user's BVN matches the BVN from Name Enquiry
	if userBVN != "" && nameEnquiryResp.BankVerificationNumber != "" && userBVN != nameEnquiryResp.BankVerificationNumber {
		slog.Warn("Account.Link_Account.BVN_Mismatch", "user_id", userID, "account_id", sanitizeAcct(req.AccountNumber))
		utils.SendErrorResponse(w, "BVN does not match the account holder's BVN", http.StatusUnprocessableEntity, nil)
		return
	}

	mandateResp, err := s.nibssClient.MandateAdvice.CreateMandate(r.Context(), &models.CreateMandateRequest{
		DebitAccountName:                  nameEnquiryResp.AccountName,
		DebitAccountNumber:                req.AccountNumber,
		DebitBankCode:                     req.BankCode,
		DebitBankName:                     nameEnquiryResp.BankName,
		DebitBankVerificationNumber:       userBVN,
		DebitKycLevel:                     "3",
		BeneficiaryAccountName:            nameEnquiryResp.AccountName,
		BeneficiaryAccountNumber:          nameEnquiryResp.AccountNumber,
		BeneficiaryKycLevel:               nameEnquiryResp.KYCLevel,
		BeneficiaryBankVerificationNumber: userBVN,
	})
	if err != nil {
		slog.Error("Account.Link_Account.Mandate_Creation_Failed", "error", err)
		utils.SendErrorResponse(w, "Failed to Create Mandate", http.StatusFailedDependency, nil)
		return
	}

	var accountID int
	err = s.db.QueryRow(`
		WITH updated AS (
			UPDATE accounts SET is_primary = false 
			WHERE user_id = $1 AND $2 = true
			RETURNING 1
		)
		INSERT INTO accounts (account_name, account_id, bank_name, bank_code, mandate_code, user_id, is_primary, balance, version, updated_at)
		VALUES ($3, $4, $5, $6, $7, $1, $2, 0, 1, NOW())
		RETURNING account_id`,
		userID, req.IsPrimary, mandateResp.AccountName, req.AccountNumber, nameEnquiryResp.BankName, req.BankCode, mandateResp.MandateCode,
	).Scan(&accountID)
	if err != nil {
		slog.Error("Account.Link_Account.Insert_Failed", "error", err)
		utils.SendErrorResponse(w, "Failed to Link Account", http.StatusFailedDependency, nil)
		return
	}

	var response = LinkedAccount{
		ID:            accountID,
		UserID:        userID,
		AccountNumber: req.AccountNumber,
		AccountName:   mandateResp.AccountName,
		BankName:      nameEnquiryResp.BankName,
		BankCode:      req.BankCode,
		IsPrimary:     req.IsPrimary,
	}
	slog.Info("Account.Link_Account.Success", "account_id", accountID)
	utils.SendSuccessResponse(w, "Account Linked Successfully", response, http.StatusOK)
}

func (s *AccountService) UnlinkAccount(w http.ResponseWriter, r *http.Request) {
	slog.Info("Account.Unlink_Account.Start")
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())
	accountId := chi.URLParam(r, "accountNumber")

	if accountId == "" {
		slog.Warn("Account.Unlink_Account.Missing_Account_ID")
		utils.SendErrorResponse(w, "Account Number Is Required", http.StatusBadRequest, nil)
		return
	}

	rows, err := s.db.QueryContext(r.Context(), `UPDATE accounts SET status = 0 WHERE user_id = $1 AND account_id = $2 RETURNING id`, userID, accountId)
	if err != nil {
		slog.Error("Account.Unlink_Account.Update_Failed", "error", err)
		utils.SendErrorResponse(w, "Failed to Unlink Account", http.StatusFailedDependency, nil)
		return
	}
	defer rows.Close()

	if !rows.Next() {
		slog.Warn("Account.Unlink_Account.Account_Not_Found", "account_id", accountId)
		utils.SendErrorResponse(w, "Account Not Found", http.StatusNotFound, nil)
		return
	}

	var id int64
	if err := rows.Scan(&id); err != nil {
		slog.Error("Account.Unlink_Account.Scan_Failed", "error", err)
		utils.SendErrorResponse(w, "Failed to Unlink Account", http.StatusFailedDependency, nil)
		return
	}

	slog.Info("Account.Unlink_Account.Success", "account_id", accountId)
	utils.SendSuccessResponse(w, "Account Unlinked Successfully", nil, http.StatusOK)
}

// AccountNameEnquiry retrieves account name
// @Summary Get account name
// @Description Retrieve account name for a given account ID
// @Tags Accounts
// @Produce json
// @Param accountId query string true "Account ID"
// @Param bankCode query string false "Bank Code"
// @Success 200 {object} object{responseCode=string,accountId=string,accountName=string,status=string,source=string}
// @Failure 400 {object} utils.APIErrorResponse
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Security BearerAuth
// @Router /account/name-enquiry [get]
func (s *AccountService) AccountNameEnquiry(w http.ResponseWriter, r *http.Request) {
	slog.Info("Account.Name_Enquiry.Start")
	accountId := strings.TrimSpace(r.URL.Query().Get("accountId"))
	bankCode := strings.TrimSpace(r.URL.Query().Get("bankCode"))

	if accountId == "" {
		slog.Warn("Account.Name_Enquiry.Missing_Account_ID", "account_id", accountId)
		utils.SendErrorResponse(w, "Account Number Is Required", http.StatusBadRequest, nil)
		return
	}

	if !IsValidAccountId(accountId) {
		slog.Warn("Account.Name_Enquiry.Invalid_Account_ID", "account_id", sanitizeAcct(accountId))
		utils.SendErrorResponse(w, "invalid Account Number format", http.StatusBadRequest, nil)
		return
	}

	if bankCode != "" && !IsValidBankCode(bankCode) {
		slog.Warn("Account.Name_Enquiry.Invalid_Bank_Code", "bank_code", sanitizeAcct(bankCode))
		utils.SendErrorResponse(w, "invalid bankCode format", http.StatusBadRequest, nil)
		return
	}

	// Check virtual accounts in Redis first
	if s.redis != nil {
		if vaData, err := ValidateVirtualAccount(s.redis, accountId); err == nil {
			slog.Info("Account.Name_Enquiry.Virtual_Account_Found", "account_id", sanitizeAcct(accountId))
			utils.SendSuccessResponse(w, utils.AccountFound, map[string]any{
				"accountId":   accountId,
				"accountName": vaData.AccountName,
				"source":      "local",
			}, http.StatusOK)

			return
		}
	}

	var accountName, status string
	err := s.db.QueryRow(`
		SELECT account_name, status FROM accounts 
		WHERE account_id = $1
		LIMIT 1
	`, accountId).Scan(&accountName, &status)

	if err == nil {
		if status != "ACTIVE" {
			slog.Warn("Account.Name_Enquiry.Account_Not_Active", "account_id", sanitizeAcct(accountId), "status", status)
			utils.SendErrorResponse(w, "Account Not Active", http.StatusUnprocessableEntity, nil)
			return
		}

		slog.Info("Account.Name_Enquiry.Local_Account_Found", "account_id", sanitizeAcct(accountId))
		utils.SendSuccessResponse(w, utils.AccountFound, map[string]any{
			"accountId":   accountId,
			"accountName": accountName,
			"source":      "local",
		}, http.StatusOK)

		return
	}

	slog.Info("Account.Name_Enquiry.Account_Not_Found_Locally", "account_id", sanitizeAcct(accountId), "bank_code", sanitizeAcct(bankCode))

	if bankCode == "" {
		slog.Warn("Account.Name_Enquiry.No_Bank_Code_For_Switch_Lookup", "account_id", sanitizeAcct(accountId))
		utils.SendErrorResponse(w, utils.AccountNotFoundError, http.StatusNotFound, nil)
		return
	}

	nipCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	neResult, err := s.nibssClient.NameEnquiry.EnquireName(nipCtx, accountId, bankCode)
	if err != nil {
		slog.Error("Account.Name_Enquiry.External_Failed", "error", err, "account_id", sanitizeAcct(accountId))
		if errors.Is(err, context.DeadlineExceeded) || utils.IsNetworkError(err) {
			utils.SendErrorResponse(w, utils.NIPServiceUnavailable, http.StatusServiceUnavailable, nil)
		} else {
			utils.SendErrorResponse(w, utils.AccountNotFoundError, http.StatusNotFound, nil)
		}
		return
	}

	slog.Info("Account.Name_Enquiry.External_Found", "account_response", neResult, "account_id", sanitizeAcct(accountId))
	utils.SendSuccessResponse(w, utils.AccountFound, map[string]any{
		"accountId":   accountId,
		"accountName": neResult.AccountName,
		"source":      "External",
	}, http.StatusOK)
}

// AccountBalanceEnquiry retrieves all accounts for authenticated user
// @Summary Get user accounts and balances
// @Description Retrieve all accounts and cards with balances for the authenticated user
// @Tags Accounts
// @Produce json
// @Success 200 {object} object{accounts=array,dailyLimit=number,singleTransactionLimit=number,dailySpent=number}
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /account/balance-enquiry [get]
func (s *AccountService) AccountBalanceEnquiry(w http.ResponseWriter, r *http.Request) {
	slog.Info("Account.Balance_Enquiry.Start")
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	type accountRow struct {
		accountID   string
		name        string
		dbBalance   int64
		status      string
		isPrimary   bool
		bankName    string
		bankCode    string
		bvn         string
		mandateCode string // NE session ID saved at link time, used as AuthorizationCode
	}

	// 1. Fetch accounts + limits — no external balance join
	rows, err := s.db.QueryContext(r.Context(), `
		WITH user_data AS (
			SELECT
				COALESCE(MAX(ul.daily_limit), 500000) AS daily_limit,
				COALESCE(MAX(ul.single_transaction_limit), 100000) AS single_tx_limit,
				COALESCE(SUM(t.amount) FILTER (
					WHERE t.type = 'DEBIT'
					  AND t.status IN ('PENDING','SUCCESS','COMPLETED')
					  AND t.created_at >= CURRENT_DATE
				), 0) AS daily_spent
			FROM (SELECT $1::integer AS uid) u
			LEFT JOIN user_limits ul ON ul.user_id = u.uid
			LEFT JOIN transactions t  ON t.user_id  = u.uid
			GROUP BY u.uid
		)
		SELECT
			a.account_id, a.account_name, a.balance, a.status,
			COALESCE(a.is_primary, false),
			COALESCE(a.bank_name, ''),
			COALESCE(a.bank_code, ''),
			COALESCE(u.bvn, ''),
			COALESCE(a.mandate_code, ''),
			ud.daily_limit, ud.single_tx_limit, ud.daily_spent
		FROM accounts a
		JOIN users u ON u.id = $1
		CROSS JOIN user_data ud
		WHERE a.user_id = $1 AND a.status = 'ACTIVE'
	`, userID)
	if err != nil {
		slog.Error("Account.Balance_Enquiry.Query_Failed", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, "Failed to fetch accounts", http.StatusFailedDependency, nil)
		return
	}
	defer rows.Close()

	var accts []accountRow
	var dailyLimit, singleTxLimit, dailySpent float64

	for rows.Next() {
		var a accountRow
		var accountID sql.NullString
		if err := rows.Scan(
			&accountID, &a.name, &a.dbBalance, &a.status,
			&a.isPrimary, &a.bankName, &a.bankCode, &a.bvn, &a.mandateCode,
			&dailyLimit, &singleTxLimit, &dailySpent,
		); err != nil {
			slog.Error("Account.Balance_Enquiry.Scan_Error", "user_id", userID, "error", err)
			continue
		}
		a.accountID = accountID.String
		accts = append(accts, a)
	}
	if err := rows.Err(); err != nil {
		slog.Error("Account.Balance_Enquiry.Rows_Error", "user_id", userID, "error", err)
	}

	// 2. Fan out NIP balance enquiries in parallel for external accounts only
	type nipResult struct {
		accountID      string
		balance        string
		balanceSuccess bool
	}
	resultCh := make(chan nipResult, len(accts))
	external := 0

	for _, a := range accts {
		if a.bankCode == "" || a.accountID == "" {
			continue
		}
		external++
		go func(accountID, accountName, bankCode, bvn, mandateCode string) {
			bal, err := s.nibssClient.BalanceEnquiry.GetBalance(r.Context(), accountID, accountName, bankCode, bvn, mandateCode)
			if err != nil {
				slog.Warn("Account.Balance_Enquiry.NIP_Failed", "account_id", sanitizeAcct(accountID), "error", err)
				resultCh <- nipResult{accountID: accountID, balanceSuccess: false}
				return
			}
			resultCh <- nipResult{accountID: accountID, balance: bal.AvailableBalance, balanceSuccess: true}
		}(a.accountID, a.name, a.bankCode, a.bvn, a.mandateCode)
	}

	type nipBalance struct {
		balance string
		success bool
	}
	nipBalances := make(map[string]nipBalance, external)
	for i := 0; i < external; i++ {
		res := <-resultCh
		nipBalances[res.accountID] = nipBalance{balance: res.balance, success: res.balanceSuccess}
	}

	// 3. Build response — wallet accounts use DB balance, external accounts use NIP balance
	accounts := make([]map[string]any, 0, len(accts))
	for _, a := range accts {
		entry := map[string]any{
			"accountId":   a.accountID,
			"accountName": a.name,
			"status":      a.status,
			"isPrimary":   a.isPrimary,
			"bankName":    a.bankName,
			"bankCode":    a.bankCode,
			"bankLogo":    s.bankService.LoadLogo(a.bankCode),
		}
		if a.bankCode == "" {
			entry["availableBalance"] = a.dbBalance
			entry["balanceAvailable"] = true
		} else if nip, ok := nipBalances[a.accountID]; ok && nip.success {
			entry["availableBalance"] = nip.balance
			entry["balanceAvailable"] = true
		} else {
			entry["availableBalance"] = "Balance Unavailable"
			entry["balanceAvailable"] = false
		}
		accounts = append(accounts, entry)
	}

	slog.Info("Account.Balance_Enquiry.Done", "user_id", userID, "total", len(accounts), "nip_fetched", external)
	utils.SendSuccessResponse(w, "Returning User Accounts", map[string]any{
		"accounts":               accounts,
		"dailyLimit":             dailyLimit,
		"singleTransactionLimit": singleTxLimit,
		"dailySpent":             dailySpent,
	}, http.StatusOK)
}

// GetVirtualAccount retrieves or generates virtual account for merchant payments
// @Summary Get virtual account
// @Description Generate a temporary virtual account tied to merchant from JWT for payment collection
// @Tags Accounts
// @Produce json
// @Param amount query int64 false "Payment amount"
// @Success 200 {object} object{virtualAccount=object,expiresAt=string}
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /account/virtual-account [get]
func (s *AccountService) GetVirtualAccount(w http.ResponseWriter, r *http.Request) {
	slog.Info("Account.GetVirtualAccount.Start")
	_, merchantID := utils.ExtractUserMerchantInfoFromContext(w, r.Context())
	if merchantID == 0 {
		slog.Warn("Account.GetVirtualAccount.Not_A_Merchant", "merchant_id", merchantID)
		utils.SendErrorResponse(w, "Only Merchants Generate Virtual Accounts", http.StatusUnprocessableEntity, nil)
		return
	}

	merchantIDStr := fmt.Sprintf("%v", merchantID)

	var merchantName, status string
	err := s.db.QueryRow("SELECT business_name, status FROM merchants WHERE id = $1", merchantIDStr).Scan(&merchantName, &status)
	if errors.Is(err, sql.ErrNoRows) {
		slog.Warn("Account.VirtualAccount.Merchant_Not_Found", "merchant_id", merchantIDStr)
		utils.SendErrorResponse(w, "Merchant Not Found", http.StatusUnprocessableEntity, nil)
		return
	}
	if err != nil {
		slog.Error("Account.VirtualAccount.DB_Error", "merchant_id", merchantIDStr, "error", err)
		utils.SendErrorResponse(w, utils.InternalServiceError, http.StatusFailedDependency, nil)
		return
	}

	if status != "ACTIVE" {
		slog.Warn("Account.VirtualAccount.Merchant_Inactive", "merchant_id", merchantIDStr, "status", status)
		utils.SendErrorResponse(w, "Merchant Not Active", http.StatusUnprocessableEntity, nil)
		return
	}

	accountNumber := generateVirtualAccountNumber()
	ttl := 3600
	expiresAt := time.Now().Add(time.Duration(ttl) * time.Second)

	vaData := VirtualAccountData{
		AccountNumber: accountNumber,
		AccountName:   merchantName,
		BankName:      "RuralPay Bank",
		// "bankCode":      "999999",
		MerchantID: merchantIDStr,
		ExpiresAt:  expiresAt.Unix(),
	}

	if amountStr := r.URL.Query().Get("amount"); amountStr != "" {
		if amount, err := strconv.ParseInt(amountStr, 10, 64); err == nil {
			vaData.Amount = amount
		}
	}

	if s.redis != nil {
		ctx := context.Background()
		key := fmt.Sprintf("va:%s", accountNumber)
		data, _ := json.Marshal(vaData)
		if err := s.redis.Set(ctx, key, data, time.Duration(ttl)*time.Second).Err(); err != nil {
			slog.Error("Account.VirtualAccount.Redis_Store_Failed", "error", err)
			utils.SendErrorResponse(w, "Failed to generate virtual account", http.StatusFailedDependency, nil)
			return
		}
		slog.Info("Account.VirtualAccount.Generated", "merchant_id", merchantIDStr, "account_number", accountNumber, "ttl", ttl)
	} else {
		slog.Warn("Account.VirtualAccount.Redis_Unavailable")
	}

	slog.Info("Account.GetVirtualAccount.Success", "merchant_id", merchantIDStr)
	utils.SendSuccessResponse(w, "VA Generated", map[string]any{
		"virtualAccount": VirtualAccountData{
			AccountNumber: accountNumber,
			AccountName:   merchantName,
			BankName:      "RuralPay Bank",
		},
		"expiresAt": expiresAt.Format(time.RFC3339),
	}, http.StatusOK)
}

func generateVirtualAccountNumber() string {
	const digits = "0123456789"
	b := make([]byte, 10)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		b[i] = digits[n.Int64()]
	}
	return string(b)
}

// UpdateUserLimits updates user transaction limits
// @Summary Update user limits
// @Description Update daily and single transaction limits for authenticated user
// @Tags Accounts
// @Accept json
// @Produce json
// @Param request body object{dailyLimit=int64,singleTransactionLimit=int64} true "Limit update request"
// @Success 200 {object} object{dailyLimit=int64,singleTransactionLimit=int64}
// @Failure 400 {object} utils.APIErrorResponse
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /account/limits [put]
func (s *AccountService) UpdateUserLimits(w http.ResponseWriter, r *http.Request) {
	slog.Info("account.UpdateUserLimits.start")
	reqCtx := r.Context()
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, reqCtx)

	// userID is already an int, no need to convert

	var req struct {
		DailyLimit             int64 `json:"dailyLimit"`
		SingleTransactionLimit int64 `json:"singleTransactionLimit"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		slog.Error("account.UpdateUserLimits.decode_error", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		slog.Warn("account.UpdateUserLimits.multiple_json_objects")
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	if req.DailyLimit <= 0 || req.SingleTransactionLimit <= 0 {
		slog.Warn("account.UpdateUserLimits.invalid_limits", "daily_limit", req.DailyLimit, "single_transaction_limit", req.SingleTransactionLimit)
		utils.SendErrorResponse(w, "Limits Must Be Positive", http.StatusBadRequest, nil)
		return
	}

	if req.SingleTransactionLimit > req.DailyLimit {
		slog.Warn("account.UpdateUserLimits.single_limit_exceeds_daily_limit", "daily_limit", req.DailyLimit, "single_transaction_limit", req.SingleTransactionLimit)
		utils.SendErrorResponse(w, utils.SingleLimitError, http.StatusBadRequest, nil)
		return
	}

	_, err := s.db.ExecContext(reqCtx, `
		INSERT INTO user_limits (user_id, daily_limit, single_transaction_limit, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (user_id) DO UPDATE
		SET daily_limit = $2, single_transaction_limit = $3, updated_at = NOW()
	`, userID, req.DailyLimit, req.SingleTransactionLimit)

	if err != nil {
		slog.Error("account.update_limits.failed", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, "Failed to Update Limits", http.StatusFailedDependency, nil)
		return
	}

	slog.Info("account.UpdateUserLimits.success", "user_id", userID)
	utils.SendSuccessResponse(w, "Updated Successfully", map[string]any{
		"dailyLimit":             req.DailyLimit,
		"singleTransactionLimit": req.SingleTransactionLimit,
	}, http.StatusOK)
}

// GenerateUserOTP generates the OTP for General Purpose validation
// @Summary Generate OTP
// @Description Generates OTP for general purpose validation
// @Tags Accounts
// @Accept json
// @Produce json
// @Param request body object{action=string,channel=string} true "OTP generation request"
// @Success 200 {object} utils.APISuccessResponse "OTP generated successfully"
// @Failure 400 {string} string "Invalid request"
// @Failure 401 {string} string "Unauthorized"
// @Security BearerAuth
// @Router /account/send-otp [post]
func (s *AccountService) GenerateUserOTP(w http.ResponseWriter, r *http.Request) {
	slog.Info("account.GenerateUserOTP.start")

	reqCtx := r.Context()

	var req struct {
		Action string `json:"action" validate:"required"`
		//Channel models.Channel `json:"channel" validate:"required,oneof=OTP BYPASS FACIAL_RECOGNITION"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("account.GenerateUserOTP.decode_error", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		slog.Error("account.GenerateUserOTP.validation_error", "error", err)
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	userId, _ := utils.ExtractUserMerchantInfoFromContext(w, reqCtx)

	otp := utils.GenerateOTP()
	key := fmt.Sprintf("%s:USER_2FA:%d", req.Action, userId)

	if s.redis != nil {
		ctx := context.Background()
		if err := s.redis.Set(ctx, key, otp, 10*time.Minute).Err(); err != nil {
			slog.Error("user.acct.generate_otp.store_failed", "error", err)
			utils.SendErrorResponse(w, utils.OTPGenerationError, http.StatusFailedDependency, nil)
			return
		}
	}

	// Send OTP via notification
	if s.notificationSVC != nil {
		user := s.fetchUserForNotification(reqCtx, userId)

		slog.Info("user.acct.generate_otp.fetch_user_otp", "user_id", userId, "action", req.Action)

		go s.notificationSVC.SendOTPEmail(user.Email, otp, "10 minutes", models.TransactionOTP)
		go s.notificationSVC.SendOTPSmS(user.PhoneNumber, otp, "10 minutes", models.TransactionOTP)
		slog.Info("user.acct.otp_sent", "user_id", userId)
	}

	slog.Info("user.acct.generate_otp.success", "user_id", userId)
	utils.SendSuccessResponse(w, "OTP Generated Successfully", nil, http.StatusOK)
}

// ValidateUser2FA validates an OTP
// @Summary Validate OTP
// @Description Validates an action against the OTP provided
func (s *AccountService) ValidateUser2FA(ctx context.Context, userId, userOTP, Action string) bool {
	slog.Info("account.validate.user.2FA.start", "action", Action)

	if Action == "BYPASS" {
		slog.Warn("account.validate.user.2FA.bypass", "user_id", userId)
		return true
	} else if Action == "2FA-CODE" {
		key := fmt.Sprintf("%s:USER_2FA:%s", Action, userId)

		if s.redis != nil {
			storedOTP, err := s.redis.Get(ctx, key).Result()
			if err != nil {
				slog.Error("user.acct.validate_2FA.retrieve.stored.failed", "error", err)
				return false
			}

			if storedOTP != userOTP {
				slog.Warn("account.verify_otp.invalid")
				return false
			}

			s.redis.Del(ctx, key)
			slog.Info("user.acct.validate_otp_successful", "action", Action)
			return true
		}
	} else if Action == "FACIAL_RECOGNITION" {
		slog.Info("account.validate.user.2FA.facial_recognition", "user_id", userId)

		// Placeholder for facial recognition logic
		// In a real implementation, this would involve calling a facial recognition service
		// and comparing the result against stored facial data for the user.
		return true
	}

	slog.Warn("account.validate.user.2FA.invalid_action", "action", Action)

	return false
}

// GenerateQRCode generates a QR Code
// @Summary Generate QR Code
// @Description Generate a QR code for payment
// @Tags QR
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body object{amount=int64} true "QR generation request"
// @Success 200 {object} utils.APISuccessResponse{qrCode=string,qrImage=string}
// @Failure 400 {object} utils.APIErrorResponse
// @Failure 401 {object} utils.APIErrorResponse
// @Router /account/qr [post]
func (s *AccountService) GenerateQRCode(w http.ResponseWriter, r *http.Request) {
	slog.Info("account.generate.qr.start")
	userID, merchantID := utils.ExtractUserMerchantInfoFromContext(w, r.Context())
	if merchantID == 0 {
		slog.Warn("account.generate.qr.invalid")
		utils.SendErrorResponse(w, "Invalid Merchant", http.StatusUnauthorized, nil)
		return
	}

	var req struct {
		Size int `json:"size"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	json.NewDecoder(r.Body).Decode(&req)

	qrCode, qrImage, err := s.qrService.GenerateQRCode(r.Context(), strconv.Itoa(userID), strconv.Itoa(merchantID), req.Size)
	if err != nil {
		slog.Error("account.GenerateQR.generate_error", "error", err)
		utils.SendErrorResponse(w, utils.ResponseMessage(err.Error()), http.StatusFailedDependency, nil)
		return
	}

	slog.Info("account.GenerateQR.success", "user_id", userID, "merchant_id", merchantID)
	utils.SendSuccessResponse(w, "QR Generated Successfully", map[string]any{
		"qrCode":  qrCode,
		"qrImage": qrImage,
	}, http.StatusOK)
}

// ProcessQRCode processes a scanned QR code
// @Summary Process QR Code
// @Description Process a scanned QR code data
// @Tags QR
// @Produce json
// @Security BearerAuth
// @Param token query string true "QR token or EMVCo QR string"
// @Success 200 {object} object{userId=string,amount=int64}
// @Failure 400 {object} utils.APIErrorResponse
// @Router /account/qr [get]
func (s *AccountService) ProcessQRCode(w http.ResponseWriter, r *http.Request) {
	slog.Info("account.ProcessQR.start")
	qrData := r.URL.Query().Get("token")
	if qrData == "" {
		slog.Warn("account.ProcessQR.missing_token")
		utils.SendErrorResponse(w, "token parameter is required", http.StatusBadRequest, nil)
		return
	}

	result, err := s.qrService.ProcessQRCode(r.Context(), qrData)
	if err != nil {
		slog.Error("account.ProcessQR.process_error", "error", err)
		utils.SendErrorResponse(w, utils.ResponseMessage(err.Error()), http.StatusBadRequest, nil)
		return
	}

	slog.Info("account.ProcessQR.success")
	utils.SendSuccessResponse(w, "QR Process Successfully", result, http.StatusOK)
}

// GenerateUSSDCode generates a USSD code for send (push) or receive (pull) payment
// @Summary Generate USSD Code
// @Description Generate a cryptographically secure USSD code based on type
// @Tags USSD
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body object{type=string,amount=int64,currency=string} true "USSD code request"
// @Success 200 {object} object{ussdCode=string,expiresIn=int}
// @Failure 400 {object} utils.APIErrorResponse
// @Failure 401 {object} utils.APIErrorResponse
// @Router /account/ussd [post]
func (s *AccountService) GenerateUSSDCode(w http.ResponseWriter, r *http.Request) {
	slog.Info("account.GenerateUSSDCode.start")
	userID, ok := r.Context().Value("userID").(string)
	if !ok || userID == "" {
		slog.Warn("account.generate_code.unauthorized")
		utils.SendErrorResponse(w, utils.UnauthorizedError, http.StatusUnauthorized, nil)
		return
	}

	var req struct {
		Type     string `json:"type" validate:"required,oneof=Send Receive"`
		Amount   int64  `json:"amount" validate:"required,gt=0"`
		Currency string `json:"currency,omitempty"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		slog.Error("account.generate_code.decode_failed", "error", err)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusBadRequest, nil)
		return
	}

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		slog.Warn("account.generate_code.multiple_json_objects")
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	slog.Info("account.generate_code.request", "type", req.Type, "amount", req.Amount)

	// if err := s.validator.ValidateStruct(&req); err != nil {
	// 	slog.Error("[USSD] GenerateCode - Validation error: %v", "error", err)
	// 	utils.SendErrorResponse(w, string(utils.ValidationError), http.StatusBadRequest, err)
	// 	return
	// }

	var code string
	var err error
	if req.Type == "Send" {
		code, err = s.ussdService.GeneratePushCode(r.Context(), userID, req.Amount)
	} else {
		code, err = s.ussdService.GeneratePullCode(r.Context(), userID, req.Amount)
	}

	if err != nil {
		slog.Error("account.generate_code.service_error", "error", err)
		utils.SendErrorResponse(w, utils.ResponseMessage(err.Error()), http.StatusFailedDependency, nil)
		return
	}

	expiresIn := int(s.ussdService.GetCodeTimeout().Seconds())
	formattedCode := s.ussdService.FormatDialCode(code)
	slog.Info("account.generate_code.success", "expires_in", expiresIn)

	utils.SendSuccessResponse(w, "USSD Code Generated", map[string]any{
		"ussdCode":  formattedCode,
		"expiresIn": expiresIn,
	}, http.StatusOK)
}

// ValidateUSSDCode validates and consumes a USSD code
// @Summary Validate USSD Code
// @Description Validate and consume a single-use USSD code
// @Tags USSD
// @Accept json
// @Produce json
// @Param request body object{code=string,mobileNo=string} true "Code validation request"
// @Success 200 {object} USSDCode
// @Failure 400 {object} utils.APIErrorResponse
// @Router /ussd/validate [post]
func (s *AccountService) ValidateUSSDCode(w http.ResponseWriter, r *http.Request) {
	slog.Info("account.ValidateUSSDCode.start")
	var req struct {
		Code     string `json:"code" validate:"required"`
		MobileNo string `json:"mobileNo" validate:"required"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		slog.Error("account.ValidateUSSDCode.decode_error", "error", err)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusBadRequest, nil)
		return
	}

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		slog.Warn("account.ValidateUSSDCode.multiple_json_objects")
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	//if err := s.validator.ValidateStruct(&req); err != nil {
	//	utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
	//	return
	//}

	codeType := models.PullPayment
	if len(req.Code) > 0 && req.Code[0] >= '0' && req.Code[0] <= '9' {
		// Numeric codes need type detection logic
		// For now, default to PullPayment unless service provides detection
	}

	ussdCode, err := s.ussdService.ValidateAndConsume(r.Context(), req.Code, codeType)
	if err != nil {
		slog.Error("account.ValidateUSSDCode.validation_error", "error", err)
		utils.SendErrorResponse(w, utils.ResponseMessage(err.Error()), http.StatusBadRequest, nil)
		return
	}

	slog.Info("account.ValidateUSSDCode.success")
	utils.SendSuccessResponse(w, "Code Validated Successfully", ussdCode, http.StatusOK)
}

// GetUserCodes retrieves all generated codes for the authenticated user
// @Summary Get User USSD Codes
// @Description Get all USSD codes generated by the authenticated user
// @Tags USSD
// @Produce json
// @Security BearerAuth
// @Success 200 {array} USSDCode
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /account/ussd [get]
func (s *AccountService) GetUserCodes(w http.ResponseWriter, r *http.Request) {
	slog.Info("account.GetUserCodes.start")
	userID, ok := r.Context().Value("userID").(string)
	if !ok || userID == "" {
		slog.Warn("account.GetUserCodes.unauthorized")
		utils.SendErrorResponse(w, utils.UnauthorizedError, http.StatusUnauthorized, nil)
		return
	}

	codes, err := s.ussdService.GetUserCodes(r.Context(), userID)
	if err != nil {
		slog.Error("account.GetUserCodes.get_codes_error", "error", err)
		utils.SendErrorResponse(w, utils.ResponseMessage(err.Error()), http.StatusFailedDependency, nil)
		return
	}

	slog.Info("account.GetUserCodes.success", "user_id", userID)
	utils.SendSuccessResponse(w, "Success", codes, http.StatusOK)
}

func (s *AccountService) fetchUserForNotification(ctx context.Context, id int) *models.User {
	slog.Info("account.fetchUserForNotification.start", "user_id", id)
	user := &models.User{ID: id}

	if s.redis != nil {
		key := fmt.Sprintf("user:notif:%d", id)
		if cached, err := s.redis.Get(context.Background(), key).Bytes(); err == nil {
			slog.Info("account.fetchUserForNotification.cache_hit", "user_id", id)
			dec := json.NewDecoder(strings.NewReader(string(cached)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(user); err != nil {
				slog.Warn("account.fetchUserForNotification.cache_decode_failed", "user_id", id, "error", err)
			}
			return user
		}
		slog.Info("account.fetchUserForNotification.cache_miss", "user_id", id)
	}

	var pushToken sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT email, phone_number, first_name
		FROM users WHERE id = $1
	`, id).Scan(&user.Email, &user.PhoneNumber, &user.FirstName)
	if err != nil {
		slog.Error("otp.notification.fetch_user_failed", "user_id", id, "error", err)
		return user
	}
	user.ExpoPushToken = pushToken.String

	if s.redis != nil {
		key := fmt.Sprintf("user:notif:%d", id)
		if data, err := json.Marshal(user); err == nil {
			slog.Info("account.fetchUserForNotification.caching_result", "user_id", id)
			s.redis.Set(context.Background(), key, data, 30*time.Minute)
		}
	}

	slog.Info("account.fetchUserForNotification.success", "user_id", id)
	return user
}

// ValidateFacialIdentity Validates the user's facial identity
// @Summary Validate Facial Identity
// @Description Validates the user's facial identity
// @Tags Accounts
// @Accept json
// @Produce json
// @Param request body object{bvn=string,userSelfie=string} true "OTP verification request"
// @Success 200 {object} utils.APISuccessResponse "Validated Successfully"
// @Failure 400 {string} string "Invalid Request"
// @Failure 403 {string} string "User's Face Does Not Match Our Records"
// @Router /account/validate-identity [post]
func (s *AccountService) ValidateFacialIdentity(w http.ResponseWriter, r *http.Request) {
	slog.Info("account.face.identity.verification.start")

	var req struct {
		BVN              string `json:"BVN" validate:"required"`
		UserSelfieBase64 string `json:"userSelfie" validate:"required"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		slog.Error("account.face.identity.verification.decode_error", "error", err)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusBadRequest, nil)
		return
	}

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		slog.Warn("account.face.identity.verification.multiple_json_objects")
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	key := fmt.Sprintf("BVN_FACE:%s", req.BVN)
	identityToken := utils.GenerateImageIdentityToken()

	if s.redis != nil {
		ctx := context.Background()
		if err := s.redis.Set(ctx, key, identityToken, 5*time.Minute).Err(); err != nil {
			slog.Error("account.face.generate_identityToken.store_failed", "error", err)
			utils.SendErrorResponse(w, utils.OTPGenerationError, http.StatusFailedDependency, nil)
			return
		}
	}

	slog.Info("account.face.identity.verification.success", "req", req)

	utils.SendSuccessResponse(w, "Validated Successfully", map[string]string{
		"identityToken": identityToken,
	}, http.StatusOK)
}

// DebitAccountInfo holds the fields needed to populate the debit side of a NIP transfer.
type DebitAccountInfo struct {
	AccountName string
	BVN         string
	MandateCode string
	BankCode    string
	KYCLevel    string
}

// GetDebitAccountInfo fetches account_name, bvn, mandate_code and bank_code for the
// given account number from the accounts + users tables.
func (s *AccountService) GetDebitAccountInfo(ctx context.Context, accountNumber string) (*DebitAccountInfo, error) {
	var info DebitAccountInfo
	err := s.db.QueryRowContext(ctx, `
		SELECT a.account_name, COALESCE(u.bvn, ''), COALESCE(a.mandate_code, ''), COALESCE(a.bank_code, ''),
		       COALESCE(ul.kyc_level::text, '1')
		FROM accounts a
		JOIN users u ON u.id = a.user_id
		LEFT JOIN user_limits ul ON ul.user_id = a.user_id
		WHERE a.account_id = $1 AND a.status = 'ACTIVE'
		LIMIT 1
	`, accountNumber).Scan(&info.AccountName, &info.BVN, &info.MandateCode, &info.BankCode, &info.KYCLevel)
	if err != nil {
		return nil, fmt.Errorf("GetDebitAccountInfo: %w", err)
	}
	return &info, nil
}
