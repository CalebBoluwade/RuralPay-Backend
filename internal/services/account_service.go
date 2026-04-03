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
	isoService      *ISO20022Service
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

func NewAccountService(db *sql.DB, redisClient *redis.Client) *AccountService {
	return &AccountService{
		db:              db,
		redis:           redisClient,
		nibssClient:     NewNIBSSClient(),
		validator:       validator.New(),
		bankService:     NewBankService(),
		qrService:       NewQRService(db, redisClient),
		notificationSVC: NewNotificationService(db),
		isoService:      NewISO20022Service(),
	}
}

func (s *AccountService) LinkAccount(w http.ResponseWriter, r *http.Request) {
	slog.Info("account.LinkAccount.start")
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	var req LinkAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("account.LinkAccount.decode_error", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	mandateResp, err := s.nibssClient.GetAccountMandate(r.Context(), req.BankCode, req.AccountNumber)
	if err != nil {
		slog.Error("account.link.nibss_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to Verify Account", http.StatusBadGateway, nil)
		return
	}

	var accountID int
	err = s.db.QueryRow(`
		WITH updated AS (
			UPDATE accounts SET is_primary = false 
			WHERE user_id = $1 AND $2 = true
			RETURNING 1
		)
		INSERT INTO accounts (account_name, account_id, bank_name, bank_code, user_id, is_primary, balance, version, updated_at)
		VALUES ($3, $4, $5, $6, $1, $2, 0, 1, NOW())
		RETURNING id`,
		userID, req.IsPrimary, mandateResp.AccountName, req.AccountNumber, mandateResp.BankName, req.BankCode,
	).Scan(&accountID)
	if err != nil {
		slog.Error("account.link.insert_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to Link Account", http.StatusFailedDependency, nil)
		return
	}

	var response = LinkedAccount{
		ID:            accountID,
		UserID:        userID,
		AccountNumber: req.AccountNumber,
		AccountName:   mandateResp.AccountName,
		BankName:      mandateResp.BankName,
		BankCode:      req.BankCode,
		IsPrimary:     req.IsPrimary,
	}
	slog.Info("account.LinkAccount.success", "account_id", accountID)
	utils.SendSuccessResponse(w, "Account Linked Successfully", response, http.StatusOK)
}

func (s *AccountService) UnlinkAccount(w http.ResponseWriter, r *http.Request) {
	slog.Info("unlink.account.start")
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())
	accountId := chi.URLParam(r, "accountNumber")

	if accountId == "" {
		slog.Warn("unlink.account.missing_account_id")
		utils.SendErrorResponse(w, "Account Number Is Required", http.StatusBadRequest, nil)
		return
	}

	rows, err := s.db.QueryContext(r.Context(), `UPDATE accounts SET status = 0 WHERE user_id = $1 AND account_id = $2 RETURNING id`, userID, accountId)
	if err != nil {
		slog.Error("account.link.insert_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to Link Account", http.StatusFailedDependency, nil)
		return
	}
	defer rows.Close()

	if !rows.Next() {
		slog.Warn("unlink.account.not_found")
		utils.SendErrorResponse(w, "Account Not Found", http.StatusNotFound, nil)
		return
	}

	var id int64
	if err := rows.Scan(&id); err != nil {
		slog.Error("account.unlink.scan_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to Unlink Account", http.StatusFailedDependency, nil)
		return
	}

	slog.Info("unlink.account.success", "account_id", accountId)
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
	slog.Info("account.name.enquiry.start")
	accountId := strings.TrimSpace(r.URL.Query().Get("accountId"))
	bankCode := strings.TrimSpace(r.URL.Query().Get("bankCode"))

	if accountId == "" {
		slog.Warn("account.name.enquiry.missing_account_id")
		utils.SendErrorResponse(w, "Account Number Is Required", http.StatusBadRequest, nil)
		return
	}

	if !IsValidAccountId(accountId) {
		slog.Warn("account.name.enquiry.invalid_account_id", "account_id", accountId)
		utils.SendErrorResponse(w, "invalid Account Number format", http.StatusBadRequest, nil)
		return
	}

	if bankCode != "" && !IsValidBankCode(bankCode) {
		slog.Warn("account.name.enquiry.invalid_bank_code", "bank_code", bankCode)
		utils.SendErrorResponse(w, "invalid bankCode format", http.StatusBadRequest, nil)
		return
	}

	// Check virtual accounts in Redis first
	if s.redis != nil {
		if vaData, err := ValidateVirtualAccount(s.redis, accountId); err == nil {
			slog.Info("account.name.enquiry.virtual_account_found", "account_id", accountId)
			utils.SendSuccessResponse(w, "Account Found", map[string]any{
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
			slog.Warn("account.name.enquiry.account_not_active", "account_id", accountId, "status", status)
			utils.SendErrorResponse(w, "Account Not Active", http.StatusUnprocessableEntity, nil)
			return
		}

		slog.Info("account.name.enquiry.local_account_found", "account_id", accountId)
		utils.SendSuccessResponse(w, "Account Found", map[string]any{
			"accountId":   accountId,
			"accountName": accountName,
			"source":      "local",
		}, http.StatusOK)

		return
	}

	slog.Info("account.name.enquiry.account_not_found_locally", "account_id", accountId, "bank_code", bankCode)

	if bankCode == "" {
		slog.Warn("account.name.enquiry.no_bank_code_for_switch_lookup", "account_id", accountId)
		utils.SendErrorResponse(w, utils.AccountNotFoundError, http.StatusNotFound, nil)
		return
	}

	acmt023, err := s.isoService.CreateAcmt023(accountId, bankCode)
	if err != nil {
		slog.Error("account.name.enquiry.acmt023_build_failed", "error", err)
		utils.SendErrorResponse(w, utils.AccountNotFoundError, http.StatusNotFound, nil)
		return
	}

	xmlData, err := s.isoService.ConvertToXML(acmt023)
	if err != nil {
		slog.Error("account.name.enquiry.acmt023_xml_failed", "error", err)
		utils.SendErrorResponse(w, utils.AccountNotFoundError, http.StatusNotFound, nil)
		return
	}

	slog.Debug("account.name.enquiry.xml", "xml", xmlData)

	idResp, err := s.nibssClient.VerifyAccountIdentification(r.Context(), []byte(xmlData))
	if err != nil {
		slog.Error("account.name.enquiry.acmt023_nibss_failed", "error", err)
		utils.SendErrorResponse(w, utils.AccountNotFoundError, http.StatusNotFound, nil)
		return
	}

	if !idResp.Verified {
		slog.Warn("account.name.enquiry.acmt023_not_verified", "account_id", accountId)
		utils.SendErrorResponse(w, utils.AccountNotFoundError, http.StatusNotFound, nil)
		return
	}

	slog.Info("account.name.enquiry.acmt023_found", "account_id", accountId)
	utils.SendSuccessResponse(w, "Account Found", map[string]any{
		"accountId":   accountId,
		"accountName": idResp.AccountName,
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
	slog.Info("account.balance.enquiry.start")
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	// userID is already an int, no need to convert
	rows, err := s.db.QueryContext(r.Context(), `
		WITH user_data AS (
			SELECT 
				COALESCE(MAX(ul.daily_limit), 500000) as daily_limit,
				COALESCE(MAX(ul.single_transaction_limit), 100000) as single_tx_limit,
				COALESCE(SUM(t.amount) FILTER (WHERE t.type = 'DEBIT' AND t.status IN ('PENDING', 'SUCCESS', 'COMPLETED') AND t.created_at >= CURRENT_DATE), 0) as daily_spent
			FROM (SELECT $1::integer AS uid) u
			LEFT JOIN user_limits ul ON ul.user_id = u.uid
			LEFT JOIN transactions t ON t.user_id = u.uid
			GROUP BY u.uid
		)
		SELECT 
			a.id, a.account_id, a.account_name, a.balance, a.status,
			COALESCE(a.is_primary, false) as is_primary,
			COALESCE(a.bank_name, '') as bank_name,
			COALESCE(a.bank_code, '') as bank_code,
			ud.daily_limit, ud.single_tx_limit, ud.daily_spent
		FROM accounts a
		CROSS JOIN user_data ud
		WHERE a.user_id = $1
	`, userID)
	if err != nil {
		slog.Error("account.balance_enquiry.query_failed", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, "Failed to fetch accounts", http.StatusFailedDependency, nil)
		return
	}
	defer rows.Close()

	accounts := []map[string]any{}
	var dailyLimit, singleTxLimit, dailySpent float64
	rowCount := 0

	for rows.Next() {
		rowCount++
		var id, accountName, status string
		var accountID, bankName, bankCode sql.NullString
		var balance int64
		var isPrimary bool
		if err := rows.Scan(&id, &accountID, &accountName, &balance, &status, &isPrimary, &bankName, &bankCode, &dailyLimit, &singleTxLimit, &dailySpent); err != nil {
			slog.Error("account.balance_enquiry.scan_error", "user_id", userID, "error", err)
			continue
		}
		accounts = append(accounts, map[string]any{
			"id":               id,
			"accountId":        accountID.String,
			"accountName":      accountName,
			"availableBalance": balance,
			"status":           status,
			"isPrimary":        isPrimary,
			"bankName":         bankName.String,
			"bankCode":         bankCode.String,
			"bankLogo":         s.bankService.LoadLogo(bankCode.String),
		})
	}

	if err := rows.Err(); err != nil {
		slog.Error("account.balance_enquiry.rows_error", "user_id", userID, "error", err)
	}

	slog.Info("account.balance_enquiry.done", "user_id", userID, "account_count", len(accounts))

	utils.SendSuccessResponse(w, "Returning User Accounts", map[string]any{
		// "responseCode":           "00",
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
	slog.Info("account.GetVirtualAccount.start")
	_, merchantID := utils.ExtractUserMerchantInfoFromContext(w, r.Context())
	if merchantID == 0 {
		slog.Warn("account.GetVirtualAccount.not_a_merchant")
		utils.SendErrorResponse(w, "Only Merchants Generate Virtual Accounts", http.StatusUnprocessableEntity, nil)
		return
	}

	merchantIDStr := fmt.Sprintf("%v", merchantID)

	var merchantName, status string
	err := s.db.QueryRow("SELECT business_name, status FROM merchants WHERE id = $1", merchantIDStr).Scan(&merchantName, &status)
	if errors.Is(err, sql.ErrNoRows) {
		slog.Warn("account.virtual_account.merchant_not_found", "merchant_id", merchantIDStr)
		utils.SendErrorResponse(w, "Merchant Not Found", http.StatusUnprocessableEntity, nil)
		return
	}
	if err != nil {
		slog.Error("account.virtual_account.db_error", "merchant_id", merchantIDStr, "error", err)
		utils.SendErrorResponse(w, utils.InternalServiceError, http.StatusFailedDependency, nil)
		return
	}

	if status != "ACTIVE" {
		slog.Warn("account.virtual_account.merchant_inactive", "merchant_id", merchantIDStr, "status", status)
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
			slog.Error("account.virtual_account.redis_store_failed", "error", err)
			utils.SendErrorResponse(w, "Failed to generate virtual account", http.StatusFailedDependency, nil)
			return
		}
		slog.Info("account.virtual_account.generated", "merchant_id", merchantIDStr, "account_number", accountNumber, "ttl", ttl)
	} else {
		slog.Warn("account.virtual_account.redis_unavailable")
	}

	slog.Info("account.GetVirtualAccount.success", "merchant_id", merchantIDStr)
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
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	// userID is already an int, no need to convert

	var req struct {
		DailyLimit             int64 `json:"dailyLimit"`
		SingleTransactionLimit int64 `json:"singleTransactionLimit"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("account.UpdateUserLimits.decode_error", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
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

	_, err := s.db.Exec(`
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
// @Success 200 {object} map[string]interface{} "OTP generated successfully"
// @Failure 400 {string} string "Invalid request"
// @Failure 401 {string} string "Unauthorized"
// @Security BearerAuth
// @Router /account/send-otp [post]
func (s *AccountService) GenerateUserOTP(w http.ResponseWriter, r *http.Request) {
	slog.Info("account.GenerateUserOTP.start")
	var req struct {
		Action  string `json:"action" validate:"required"`
		Channel string `json:"channel" validate:"required,oneof=SMS EMAIL"`
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

	userId, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	otp := utils.GenerateOTP()
	key := fmt.Sprintf("%s:USER_OTP:%d", req.Action, userId)

	if s.redis != nil {
		ctx := context.Background()
		if err := s.redis.Set(ctx, key, otp, 10*time.Minute).Err(); err != nil {
			slog.Error("user.acct.generate_otp.store_failed", "error", err)
			utils.SendErrorResponse(w, utils.OTPGenerationError, http.StatusFailedDependency, nil)
			return
		}
	}

	//OTPType models.NotificationType :=
	//switch req.Action {
	//case "2FA-CODE":
	//	OT
	//}

	// Send OTP via notification
	if s.notificationSVC != nil {
		user := s.fetchUserForNotification(userId)

		slog.Info("user.acct.generate_otp.fetch_user_otp", "user_id", userId, "action", req.Action, "channel", req.Channel)

		go s.notificationSVC.SendOTPEmail(user.Email, otp, "10 minutes", models.TransactionOTP)
		go s.notificationSVC.SendOTPSmS(user.PhoneNumber, otp, "10 minutes", models.TransactionOTP)
		slog.Info("user.acct.otp_sent", "user_id", userId)
	}

	slog.Info("user.acct.generate_otp.success", "user_id", userId)
	utils.SendSuccessResponse(w, "OTP Generated Successfully", nil, http.StatusOK)
}

// ValidateUserOTP validates an OTP
// @Summary Validate OTP
// @Description Validates an action against the OTP provided
func (s *AccountService) ValidateUserOTP(userId, userOTP, Action string) bool {
	slog.Info("account.ValidateUserOTP.start", "action", Action)
	key := fmt.Sprintf("%s:USER_OTP:%s", Action, userId)

	if s.redis != nil {
		ctx := context.Background()
		storedOTP, err := s.redis.Get(ctx, key).Result()
		if err != nil {
			slog.Error("user.acct.validate_otp.retrieve.stored.failed", "error", err)
			return false
		}

		if storedOTP != userOTP {
			slog.Warn("account.verify_otp.invalid")
			return false
		}

		s.redis.Del(ctx, key)
	}

	slog.Info("user.acct.validate_otp_successful", "action", Action)
	return true
}

// GenerateBVNOTP generates the OTP for BVN validation
// @Summary Generate BVN OTP
// @Description Generates OTP for BVN validation
// @Tags Accounts
// @Accept json
// @Produce json
// @Param request body object{bvn=string,phoneNumber=string,email=string} true "BVN OTP request"
// @Success 200 {object} map[string]interface{} "OTP generated successfully"
// @Failure 400 {string} string "Invalid request"
// @Failure 401 {string} string "Unauthorized"
// @Router /account/send-bvn-otp [post]
func (s *AccountService) GenerateBVNOTP(w http.ResponseWriter, r *http.Request) {
	slog.Info("account.GenerateBVNOTP.start")
	var req struct {
		BVN         string `json:"bvn" validate:"required,len=11"`
		PhoneNumber string `json:"phoneNumber" validate:"required"`
		Email       string `json:"email" validate:"required,email"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("account.GenerateBVNOTP.decode_error", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		slog.Error("account.GenerateBVNOTP.validation_error", "error", err)
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	bvnData, err := s.nibssClient.VerifyBVN(r.Context(), req.BVN, req.PhoneNumber)
	if err != nil {
		slog.Error("account.generate_bvn_otp.nibss_failed", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, "BVN verification failed", http.StatusBadGateway, nil)
		return
	}
	if !bvnData.PhoneMatches {
		slog.Warn("account.generate_bvn_otp.phone_mismatch", "user_id", userID)
		utils.SendErrorResponse(w, "Phone number does not match BVN records", http.StatusUnauthorized, nil)
		return
	}

	key := fmt.Sprintf("%s:BVN_OTP:%d", req.BVN, userID)
	otp := utils.GenerateOTP()

	if s.redis != nil {
		ctx := context.Background()
		if err := s.redis.Set(ctx, key, otp, 10*time.Minute).Err(); err != nil {
			slog.Error("account.generate_otp.store_failed", "error", err)
			utils.SendErrorResponse(w, utils.OTPGenerationError, http.StatusFailedDependency, nil)
			return
		}
	}

	// Send OTP via notification
	if s.notificationSVC != nil {
		go s.notificationSVC.SendOTPEmail(req.Email, otp, "10 minutes", models.ValidateAccount)
		go s.notificationSVC.SendOTPSmS(req.PhoneNumber, otp, "10 minutes", models.ValidateAccount)
		slog.Info("auth.forgot_password.otp_sent", "user_id", userID)
	}

	slog.Info("account.generate_otp.success", "user_id", userID)
	utils.SendSuccessResponse(w, "", "OTP Generated", http.StatusOK)
}

// ValidateBVNOTP verifies the OTP for BVN validation
// @Summary Validate BVN OTP
// @Description Verify OTP sent for BVN validation
// @Tags Accounts
// @Accept json
// @Produce json
// @Param request body object{bvn=string,otp=string} true "OTP verification request"
// @Success 200 {object} map[string]interface{} "OTP verified successfully"
// @Failure 400 {string} string "Invalid request"
// @Failure 401 {string} string "Invalid or expired OTP"
// @Router /account/validate-bvn-otp [post]
func (s *AccountService) ValidateBVNOTP(w http.ResponseWriter, r *http.Request) {
	slog.Info("account.ValidateBVNOTP.start")
	var req struct {
		BVN string `json:"bvn" validate:"required,len=11"`
		OTP string `json:"otp" validate:"required,len=8"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("account.ValidateBVNOTP.decode_error", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		slog.Error("account.ValidateBVNOTP.validation_error", "error", err)
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())
	key := fmt.Sprintf("%s:BVN_OTP:%d", req.BVN, userID)

	if s.redis != nil {
		ctx := context.Background()
		storedOTP, err := s.redis.Get(ctx, key).Result()
		if err != nil {
			slog.Warn("account.verify.bvn_otp.not_found_or_expired")
			utils.SendErrorResponse(w, utils.OTPError, http.StatusUnauthorized, nil)
			return
		}

		if storedOTP != req.OTP {
			slog.Warn("account.verify.bvn_otp.invalid")
			utils.SendErrorResponse(w, utils.OTPError, http.StatusUnauthorized, nil)
			return
		}

		s.redis.Del(ctx, key)
	}

	if _, err := s.db.Exec(`
		INSERT INTO user_limits (user_id, kyc_status, kyc_level, kyc_verified_at, updated_at)
		VALUES ($1, 'VERIFIED', 1, NOW(), NOW())
		ON CONFLICT (user_id) DO UPDATE
		SET kyc_status = 'VERIFIED', kyc_level = 1, kyc_verified_at = NOW(), updated_at = NOW()
	`, userID); err != nil {
		slog.Error("account.verify.bvn_otp.kyc_persist_failed", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, "Failed to update KYC status", http.StatusFailedDependency, nil)
		return
	}

	slog.Info("account.verify.bvn_otp.success", "user_id", userID)
	utils.SendSuccessResponse(w, "", "BVN Verified", http.StatusOK)
}

// GenerateQR generates a QR Code
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
func (s *AccountService) GenerateQR(w http.ResponseWriter, r *http.Request) {
	slog.Info("account.generate.qr.start")
	userID, merchantID := utils.ExtractUserMerchantInfoFromContext(w, r.Context())
	if merchantID == 0 {
		slog.Warn("account.generate.qr.invalid")
		utils.SendErrorResponse(w, "Invalid Merchant", http.StatusUnauthorized, nil)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	// if err := dec.Decode(&req); err != nil {
	// 	services.utils.SendErrorResponse(w, "Unable To Process This Request At This Time", http.StatusBadRequest, nil)
	// 	return
	// }

	// if err := dec.Decode(&struct{}{}); err != io.EOF {
	// 	services.utils.SendErrorResponse(w, string(utils.SingleObjectError), http.StatusBadRequest, nil)
	// 	return
	// }

	// if err := h.validator.ValidateStruct(&req); err != nil {
	// 	services.utils.SendErrorResponse(w, string(utils.ValidationError), http.StatusBadRequest, err)
	// 	return
	// }

	qrCode, qrImage, err := s.qrService.GenerateQRCode(r.Context(), strconv.Itoa(userID), strconv.Itoa(merchantID))
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

// ProcessQR processes a scanned QR code
// @Summary Process QR Code
// @Description Process a scanned QR code data
// @Tags QR
// @Produce json
// @Security BearerAuth
// @Param token query string true "QR token or EMVCo QR string"
// @Success 200 {object} object{userId=string,amount=int64}
// @Failure 400 {object} utils.APIErrorResponse
// @Router /account/qr [get]
func (s *AccountService) ProcessQR(w http.ResponseWriter, r *http.Request) {
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
		utils.SendErrorResponse(w, "Unable To Process This Request At This Time", http.StatusBadRequest, nil)
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
		utils.SendErrorResponse(w, "Unable To Process This Request At This Time", http.StatusBadRequest, nil)
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

func (s *AccountService) fetchUserForNotification(id int) *models.User {
	slog.Info("account.fetchUserForNotification.start", "user_id", id)
	user := &models.User{ID: id}

	if s.redis != nil {
		key := fmt.Sprintf("user:notif:%d", id)
		if cached, err := s.redis.Get(context.Background(), key).Bytes(); err == nil {
			slog.Info("account.fetchUserForNotification.cache_hit", "user_id", id)
			json.Unmarshal(cached, user)
			return user
		}
		slog.Info("account.fetchUserForNotification.cache_miss", "user_id", id)
	}

	var pushToken sql.NullString
	err := s.db.QueryRow(`
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
		utils.SendErrorResponse(w, "Unable To Process This Request At This Time", http.StatusBadRequest, nil)
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
