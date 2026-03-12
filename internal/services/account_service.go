package services

import (
	"context"
	"crypto/rand"
	"errors"

	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

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
		notificationSVC: NewNotificationService(),
	}
}

func (s *AccountService) LinkAccount(w http.ResponseWriter, r *http.Request) {
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	var req LinkAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	mandateResp, err := s.nibssClient.GetAccountMandate(req.BankCode, req.AccountNumber)
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
	utils.SendSuccessResponse(w, "Account Linked Successfully", response, http.StatusOK)
}

// AccountNameEnquiry retrieves account name
// @Summary Get account name
// @Description Retrieve account name for a given account ID
// @Tags accounts
// @Produce JSON
// @Param accountId query string true "Account ID"
// @Param bankCode query string false "Bank Code"
// @Success 200 {object} object{responseCode=string,accountId=string,accountName=string,status=string,source=string}
// @Failure 400 {object} services.ErrorResponse
// @Failure 403 {object} services.ErrorResponse
// @Failure 404 {object} services.ErrorResponse
// @Router /account/name-enquiry [get]
// @Security BearerAuth
func (s *AccountService) AccountNameEnquiry(w http.ResponseWriter, r *http.Request) {
	accountId := strings.TrimSpace(r.URL.Query().Get("accountId"))
	bankCode := strings.TrimSpace(r.URL.Query().Get("bankCode"))

	if accountId == "" {
		utils.SendErrorResponse(w, "Account Number Is Required", http.StatusBadRequest, nil)
		return
	}

	if !IsValidAccountId(accountId) {
		utils.SendErrorResponse(w, "invalid Account Number format", http.StatusBadRequest, nil)
		return
	}

	if bankCode != "" && !IsValidBankCode(bankCode) {
		utils.SendErrorResponse(w, "invalid bankCode format", http.StatusBadRequest, nil)
		return
	}

	// Check virtual accounts in Redis first
	if s.redis != nil {
		if vaData, err := ValidateVirtualAccount(s.redis, accountId); err == nil {
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
			utils.SendErrorResponse(w, "Account Not Active", http.StatusForbidden, nil)
			return
		}

		utils.SendSuccessResponse(w, "Account Found", map[string]any{
			"accountId":   accountId,
			"accountName": accountName,
			"source":      "local",
		}, http.StatusOK)

		return
	}

	utils.SendErrorResponse(w, "Account Not Found", http.StatusNotFound, nil)
}

// AccountBalanceEnquiry retrieves all accounts for authenticated user
// @Summary Get user accounts and balances
// @Description Retrieve all accounts and cards with balances for the authenticated user
// @Tags accounts
// @Produce JSON
// @Success 200 {object} object{responseCode=string,accounts=array,status=string}
// @Failure 401 {object} services.ErrorResponse
// @Failure 500 {object} services.ErrorResponse
// @Router /account/balance-enquiry [get]
// @Security BearerAuth
func (s *AccountService) AccountBalanceEnquiry(w http.ResponseWriter, r *http.Request) {
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
// @Tags accounts
// @Produce JSON
// @Param amount query int64 false "Payment amount"
// @Success 200 {object} object{responseCode=string,virtualAccount=object,expiresAt=string,status=string}
// @Failure 401 {object} services.ErrorResponse
// @Failure 403 {object} services.ErrorResponse
// @Failure 500 {object} services.ErrorResponse
// @Router /account/virtual-account [get]
// @Security BearerAuth
func (s *AccountService) GetVirtualAccount(w http.ResponseWriter, r *http.Request) {
	_, merchantID := utils.ExtractUserMerchantInfoFromContext(w, r.Context())
	if merchantID == 0 {
		utils.SendErrorResponse(w, "Only Merchants Generate Virtual Accounts", http.StatusForbidden, nil)
		return
	}

	merchantIDStr := fmt.Sprintf("%v", merchantID)

	var merchantName, status string
	err := s.db.QueryRow("SELECT business_name, status FROM merchants WHERE id = $1", merchantIDStr).Scan(&merchantName, &status)
	if errors.Is(err, sql.ErrNoRows) {
		slog.Warn("account.virtual_account.merchant_not_found", "merchant_id", merchantIDStr)
		utils.SendErrorResponse(w, "Merchant Not Found", http.StatusForbidden, nil)
		return
	}
	if err != nil {
		slog.Error("account.virtual_account.db_error", "merchant_id", merchantIDStr, "error", err)
		utils.SendErrorResponse(w, utils.InternalServiceError, http.StatusFailedDependency, nil)
		return
	}

	if status != "ACTIVE" {
		slog.Warn("account.virtual_account.merchant_inactive", "merchant_id", merchantIDStr, "status", status)
		utils.SendErrorResponse(w, "Merchant not active", http.StatusForbidden, nil)
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
// @Tags accounts
// @Accept JSON
// @Produce JSON
// @Param request body object{dailyLimit=int64,singleTransactionLimit=int64} true "Limit update request"
// @Success 200 {object} object{responseCode=string,dailyLimit=int64,singleTransactionLimit=int64,status=string}
// @Failure 401 {object} services.ErrorResponse
// @Failure 400 {object} services.ErrorResponse
// @Failure 500 {object} services.ErrorResponse
// @Router /account/limits [put]
// @Security BearerAuth
func (s *AccountService) UpdateUserLimits(w http.ResponseWriter, r *http.Request) {
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	// userID is already an int, no need to convert

	var req struct {
		DailyLimit             int64 `json:"dailyLimit"`
		SingleTransactionLimit int64 `json:"singleTransactionLimit"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if req.DailyLimit <= 0 || req.SingleTransactionLimit <= 0 {
		utils.SendErrorResponse(w, "Limits Must Be Positive", http.StatusBadRequest, nil)
		return
	}

	if req.SingleTransactionLimit > req.DailyLimit {
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

	utils.SendSuccessResponse(w, "Updated Successfully", map[string]any{
		"dailyLimit":             req.DailyLimit,
		"singleTransactionLimit": req.SingleTransactionLimit,
	}, http.StatusOK)
}

// GenerateUserOTP generates the OTP for General Purpose validation
// @Summary Generate OTP
// @Description Generates OTP for BVN validation
// @Tags accounts
// @Accept JSON
// @Produce JSON
// @Param request body map[string]string true "OTP verification request"
// @Success 200 {object} map[string]interface{} "OTP generated successfully"
// @Failure 400 {string} string(utils.InvalidRequest)
// @Failure 401 {string} string utils.OTPError
// @Router /account/send-otp [post]
func (s *AccountService) GenerateUserOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action  string `json:"action" validate:"required"`
		Channel string `json:"channel" validate:"required,oneof=SMS EMAIL"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
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
// @Description Validates an Action against the OTP Provided
// @Tags accounts
// @Accept JSON
// @Produce JSON
// @Param request body map[string]string true "BVN validation request"
// @Success 200 {object} map[string]interface{} "OTP sent successfully"
// @Failure 400 {string} string(utils.InvalidRequest)
// @Router /account/validate-otp [post]
func (s *AccountService) ValidateUserOTP(userId, userOTP, Action string) bool {
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
// @Summary Generate OTP
// @Description Generates OTP for BVN validation
// @Tags accounts
// @Accept JSON
// @Produce JSON
// @Param request body map[string]string true "OTP verification request"
// @Success 200 {object} map[string]interface{} "OTP generated successfully"
// @Failure 400 {string} string(utils.InvalidRequest)
// @Failure 401 {string} string utils.OTPError
// @Router /account/send-bvn-otp [post]
func (s *AccountService) GenerateBVNOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BVN         string `json:"bvn" validate:"required,len=11"`
		PhoneNumber string `json:"phoneNumber" validate:"required"`
		Email       string `json:"email" validate:"required,email"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	bvnData, err := s.nibssClient.VerifyBVN(req.BVN, req.PhoneNumber)
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
// @Summary Validate OTP
// @Description Verify OTP sent for BVN validation
// @Tags accounts
// @Accept JSON
// @Produce JSON
// @Param request body map[string]string true "OTP verification request"
// @Success 200 {object} map[string]interface{} "OTP verified successfully"
// @Failure 400 {string} string(utils.InvalidRequest)
// @Failure 401 {string} string utils.OTPError
// @Router /account/validate-bvn-otp [post]
func (s *AccountService) ValidateBVNOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BVN string `json:"bvn" validate:"required,len=11"`
		OTP string `json:"otp" validate:"required,len=8"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
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
// @Accept JSON
// @Produce JSON
// @Security BearerAuth
// @Param request body object{amount=int64} true "QR generation request"
// @Success 200 {object} object{qrCode=string}
// @Failure 400 {object} services.ErrorResponse
// @Failure 401 {object} services.ErrorResponse
// @Router /account/qr [post]
func (s *AccountService) GenerateQR(w http.ResponseWriter, r *http.Request) {
	userID, merchantID := utils.ExtractUserMerchantInfoFromContext(w, r.Context())
	if merchantID == 0 {
		slog.Warn("account.generate_qr.invalid")
		utils.SendErrorResponse(w, "Invalid Merchant", http.StatusUnauthorized, nil)
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
		utils.SendErrorResponse(w, utils.ResponseMessage(err.Error()), http.StatusFailedDependency, nil)
		return
	}

	utils.SendSuccessResponse(w, "QR Generated Successfully", map[string]any{
		"qrCode":  qrCode,
		"qrImage": qrImage,
	}, http.StatusOK)
}

// ProcessQR processes a scanned QR code
// @Summary Process QR Code
// @Description Process a scanned QR code data
// @Tags QR
// @Accept JSON
// @Produce JSON
// @Security BearerAuth
// @Param token query string true "QR token or EMVCo QR string"
// @Success 200 {object} object{userId=string,amount=int64}
// @Failure 400 {object} services.ErrorResponse
// @Router /account/qr [get]
func (s *AccountService) ProcessQR(w http.ResponseWriter, r *http.Request) {
	qrData := r.URL.Query().Get("token")
	if qrData == "" {
		utils.SendErrorResponse(w, "token parameter is required", http.StatusBadRequest, nil)
		return
	}

	result, err := s.qrService.ProcessQRCode(r.Context(), qrData)
	if err != nil {
		utils.SendErrorResponse(w, utils.ResponseMessage(err.Error()), http.StatusBadRequest, nil)
		return
	}

	utils.SendSuccessResponse(w, "QR Process Successfully", result, http.StatusOK)
}

// GenerateUSSDCode generates a USSD code for send (push) or receive (pull) payment
// @Summary Generate USSD Code
// @Description Generate a cryptographically secure USSD code based on type
// @Tags USSD
// @Accept JSON
// @Produce JSON
// @Security BearerAuth
// @Param request body object{type=string,amount=int64,currency=string} true "USSD code request"
// @Success 200 {object} object{code=string}
// @Failure 400 {object} services.ErrorResponse
// @Failure 401 {object} services.ErrorResponse
// @Failure 500 {object} services.ErrorResponse
// @Router /account/ussd [post]
func (s *AccountService) GenerateUSSDCode(w http.ResponseWriter, r *http.Request) {
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
	// 	log.Printf("[USSD] GenerateCode - Validation error: %v", err)
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
// @Accept JSON
// @Produce JSON
// @Param request body object{code=string,mobileNo=string} true "Code validation request"
// @Success 200 {object} services.USSDCode
// @Failure 400 {object} services.ErrorResponse
// @Router /ussd/validate [post]
func (s *AccountService) ValidateUSSDCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string `json:"code" validate:"required"`
		MobileNo string `json:"mobileNo" validate:"required"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		utils.SendErrorResponse(w, "Unable To Process This Request At This Time", http.StatusBadRequest, nil)
		return
	}

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	// if err := h.validator.ValidateStruct(&req); err != nil {
	// 	utils.SendErrorResponse(w, string(utils.ValidationError), http.StatusBadRequest, err)
	// 	return
	// }

	codeType := models.PullPayment
	if len(req.Code) > 0 && req.Code[0] >= '0' && req.Code[0] <= '9' {
		// Numeric codes need type detection logic
		// For now, default to PullPayment unless service provides detection
	}

	ussdCode, err := s.ussdService.ValidateAndConsume(r.Context(), req.Code, codeType)
	if err != nil {
		utils.SendErrorResponse(w, utils.ResponseMessage(err.Error()), http.StatusBadRequest, nil)
		return
	}

	utils.SendSuccessResponse(w, "Code Validated Successfully", ussdCode, http.StatusOK)
}

// GetUserCodes retrieves all generated codes for the authenticated user
// @Summary Get User USSD Codes
// @Description Get all USSD codes generated by the authenticated user
// @Tags USSD
// @Produce JSON
// @Security BearerAuth
// @Success 200 {array} services.USSDCode
// @Failure 401 {object} services.ErrorResponse
// @Failure 500 {object} services.ErrorResponse
// @Router /account/ussd [get]
func (s *AccountService) GetUserCodes(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value("userID").(string)
	if !ok || userID == "" {
		utils.SendErrorResponse(w, utils.UnauthorizedError, http.StatusUnauthorized, nil)
		return
	}

	codes, err := s.ussdService.GetUserCodes(r.Context(), userID)
	if err != nil {
		utils.SendErrorResponse(w, utils.ResponseMessage(err.Error()), http.StatusFailedDependency, nil)
		return
	}

	utils.SendSuccessResponse(w, "Success", codes, http.StatusOK)
}

// GetBeneficiaries retrieves saved beneficiaries for the authenticated user
// @Summary Get beneficiaries
// @Description Retrieve all saved beneficiaries for the authenticated user
// @Tags accounts
// @Produce JSON
// @Success 200 {object} object{beneficiaries=array}
// @Failure 401 {object} services.ErrorResponse
// @Failure 500 {object} services.ErrorResponse
// @Router /account/beneficiaries [get]
// @Security BearerAuth
func (s *AccountService) GetBeneficiaries(w http.ResponseWriter, r *http.Request) {
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())
	ctx := r.Context()
	cacheKey := fmt.Sprintf("beneficiaries:%d", userID)

	if s.redis != nil {
		if cached, err := s.redis.Get(ctx, cacheKey).Bytes(); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Write(cached)
			return
		}
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, account_number, account_name, bank_name, bank_code
		FROM beneficiaries
		WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		slog.Error("account.get_beneficiaries.query_failed", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, "Failed to fetch beneficiaries", http.StatusFailedDependency, nil)
		return
	}
	defer rows.Close()

	beneficiaries := []map[string]any{}
	for rows.Next() {
		var id int
		var accountNumber, accountName, bankName, bankCode string
		if err := rows.Scan(&id, &accountNumber, &accountName, &bankName, &bankCode); err != nil {
			slog.Error("account.get_beneficiaries.scan_error", "user_id", userID, "error", err)
			continue
		}
		beneficiaries = append(beneficiaries, map[string]any{
			"id":            id,
			"accountNumber": accountNumber,
			"accountName":   accountName,
			"bankName":      bankName,
			"bankCode":      bankCode,
			"bankLogo":      s.bankService.LoadLogo(bankCode),
		})
	}

	slog.Info("account.get_beneficiaries.done", "user_id", userID, "count", len(beneficiaries))

	payload, _ := json.Marshal(map[string]any{"beneficiaries": beneficiaries})
	if s.redis != nil {
		s.redis.Set(ctx, cacheKey, payload, 10*time.Minute)
	}

	utils.SendSuccessResponse(w, "Returning Beneficiaries", beneficiaries, http.StatusOK)
}

func (s *AccountService) fetchUserForNotification(id int) *models.User {
	user := &models.User{ID: id}

	if s.redis != nil {
		key := fmt.Sprintf("user:notif:%d", id)
		if cached, err := s.redis.Get(context.Background(), key).Bytes(); err == nil {
			json.Unmarshal(cached, user)
			return user
		}
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
			s.redis.Set(context.Background(), key, data, 30*time.Minute)
		}
	}

	return user
}
