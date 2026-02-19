package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/go-redis/redis/v8"
)

type AccountService struct {
	db          *sql.DB
	nibssClient *NIBSSClient
	redis       *redis.Client
	validator   *validator.Validate
	bankService *BankService
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
		db:          db,
		redis:       redisClient,
		nibssClient: NewNIBSSClient(),
		validator:   validator.New(),
		bankService: NewBankService(),
	}
}

func (s *AccountService) sendErrorResponse(w http.ResponseWriter, message string, statusCode int, validationErr error) {
	SendErrorResponse(w, message, statusCode, validationErr)
}

func (s *AccountService) LinkAccount(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("userID")
	if userID == nil {
		s.sendErrorResponse(w, "Unauthorized", http.StatusUnauthorized, nil)
		return
	}

	var req LinkAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.sendErrorResponse(w, "Invalid Request", http.StatusBadRequest, nil)
		return
	}

	mandateResp, err := s.nibssClient.GetAccountMandate(req.BankCode, req.AccountNumber)
	if err != nil {
		log.Printf("[ACCOUNT] NIBSS mandate API failed: %v", err)
		s.sendErrorResponse(w, "Failed to Verify Account", http.StatusBadGateway, nil)
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
		log.Printf("[ACCOUNT] Failed to insert account: %v", err)
		s.sendErrorResponse(w, "Failed to link account", http.StatusInternalServerError, nil)
		return
	}

	response := LinkedAccount{
		ID:            accountID,
		UserID:        userID.(int),
		AccountNumber: req.AccountNumber,
		AccountName:   mandateResp.AccountName,
		BankName:      mandateResp.BankName,
		BankCode:      req.BankCode,
		IsPrimary:     req.IsPrimary,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// ValidateBVN validates a BVN number and sends OTP
// @Summary Validate BVN
// @Description Validate a Bank Verification Number and send OTP
// @Tags accounts
// @Accept json
// @Produce json
// @Param request body map[string]string true "BVN validation request"
// @Success 200 {object} map[string]interface{} "OTP sent successfully"
// @Failure 400 {string} string "Invalid request"
// @Router /accounts/validate-bvn [post]
func (s *AccountService) ValidateBVN(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BVN         string `json:"bvn" validate:"required,len=11"`
		PhoneNumber string `json:"phoneNumber" validate:"required"`
		Email       string `json:"email" validate:"required,email"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.sendErrorResponse(w, "Invalid request", http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		s.sendErrorResponse(w, "Validation failed", http.StatusBadRequest, err)
		return
	}

	otp := generateOTP()
	key := fmt.Sprintf("bvn_otp:%s", req.BVN)

	if s.redis != nil {
		ctx := context.Background()
		if err := s.redis.Set(ctx, key, otp, 10*time.Minute).Err(); err != nil {
			log.Printf("[AUTH] Failed to store OTP in Redis: %v", err)
			s.sendErrorResponse(w, "Failed to generate OTP", http.StatusInternalServerError, nil)
			return
		}
	}

	log.Printf("[AUTH] OTP generated for BVN %s: %s (Phone: %s, Email: %s)", req.BVN, otp, req.PhoneNumber, req.Email)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"message": "OTP Sent Successfully",
		"valid":   true,
	})
}

// AccountNameEnquiry retrieves account name
// @Summary Get account name
// @Description Retrieve account name for a given account ID
// @Tags accounts
// @Produce json
// @Param accountId query string true "Account ID"
// @Param bankCode query string false "Bank Code"
// @Success 200 {object} object{responseCode=string,accountId=string,accountName=string,status=string,source=string}
// @Failure 400 {object} services.ErrorResponse
// @Failure 403 {object} services.ErrorResponse
// @Failure 404 {object} services.ErrorResponse
// @Router /accounts/name-enquiry [get]
// @Security BearerAuth
func (s *AccountService) AccountNameEnquiry(w http.ResponseWriter, r *http.Request) {
	accountId := strings.TrimSpace(r.URL.Query().Get("accountId"))
	bankCode := strings.TrimSpace(r.URL.Query().Get("bankCode"))

	if accountId == "" {
		SendErrorResponse(w, "accountId is required", http.StatusBadRequest, nil)
		return
	}

	if !IsValidAccountId(accountId) {
		SendErrorResponse(w, "invalid accountId format", http.StatusBadRequest, nil)
		return
	}

	if bankCode != "" && !IsValidBankCode(bankCode) {
		SendErrorResponse(w, "invalid bankCode format", http.StatusBadRequest, nil)
		return
	}

	// Check virtual accounts in Redis first
	if s.redis != nil {
		if vaData, err := ValidateVirtualAccount(s.redis, accountId); err == nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"success":     true,
				"accountId":   accountId,
				"accountName": vaData.AccountName,
				"status":      "SUCCESS",
				"source":      "virtual",
			})
			return
		}
	}

	var accountName, status string
	err := s.db.QueryRow(`
		SELECT account_name, status FROM accounts 
		WHERE card_id = $1 OR account_id = $1
		LIMIT 1
	`, accountId).Scan(&accountName, &status)

	if err == nil {
		if status != "ACTIVE" {
			SendErrorResponse(w, "Account not active", http.StatusForbidden, nil)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"responseCode": "00",
			"accountId":    accountId,
			"accountName":  accountName,
			"status":       "SUCCESS",
			"source":       "local",
		})
		return
	}

	SendErrorResponse(w, "Account not found", http.StatusNotFound, nil)
}

// AccountBalanceEnquiry retrieves all accounts for authenticated user
// @Summary Get user accounts and balances
// @Description Retrieve all accounts and cards with balances for the authenticated user
// @Tags accounts
// @Produce json
// @Success 200 {object} object{responseCode=string,accounts=array,status=string}
// @Failure 401 {object} services.ErrorResponse
// @Failure 500 {object} services.ErrorResponse
// @Router /accounts/balance-enquiry [get]
// @Security BearerAuth
func (s *AccountService) AccountBalanceEnquiry(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value("userID").(string)
	if !ok || userID == "" {
		log.Printf("[BALANCE_ENQUIRY] Unauthorized: userID not found in context")
		SendErrorResponse(w, "Unauthorized", http.StatusUnauthorized, nil)
		return
	}

	userIDInt, err := strconv.Atoi(userID)
	if err != nil {
		log.Printf("[BALANCE_ENQUIRY] Invalid user ID conversion: %v", err)
		SendErrorResponse(w, "Invalid user ID", http.StatusBadRequest, nil)
		return
	}

	log.Printf("[BALANCE_ENQUIRY] Starting balance enquiry for user %d", userIDInt)

	log.Printf("[BALANCE_ENQUIRY] Executing query for user %d", userIDInt)
	rows, err := s.db.Query(`
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
			a.id, a.account_id, a.card_id, a.account_name, a.balance, a.status,
			COALESCE(a.is_primary, false) as is_primary,
			COALESCE(a.bank_name, '') as bank_name,
			COALESCE(a.bank_code, '') as bank_code,
			ud.daily_limit, ud.single_tx_limit, ud.daily_spent
		FROM accounts a
		CROSS JOIN user_data ud
		WHERE a.user_id = $1
	`, userIDInt)
	if err != nil {
		log.Printf("[BALANCE_ENQUIRY] Query failed for user %d: %v", userIDInt, err)
		SendErrorResponse(w, "Failed to fetch accounts", http.StatusInternalServerError, nil)
		return
	}
	defer rows.Close()

	log.Printf("[BALANCE_ENQUIRY] Query executed successfully for user %d", userIDInt)

	accounts := []map[string]any{}
	var dailyLimit, singleTxLimit, dailySpent float64
	rowCount := 0

	for rows.Next() {
		rowCount++
		var id, accountName, status string
		var accountID, cardID, bankName, bankCode sql.NullString
		var balance int64
		var isPrimary bool
		if err := rows.Scan(&id, &accountID, &cardID, &accountName, &balance, &status, &isPrimary, &bankName, &bankCode, &dailyLimit, &singleTxLimit, &dailySpent); err != nil {
			log.Printf("[BALANCE_ENQUIRY] Row scan error for user %d, row %d: %v", userIDInt, rowCount, err)
			continue
		}
		log.Printf("[BALANCE_ENQUIRY] Scanned Account for user %d: [id]=%s, [Account]=%s, [CardID]=%s, [Name]=%s, [Balance]=%d, [Status]=%s",
			userIDInt, id, accountID.String, cardID.String, accountName, balance, status)
		accounts = append(accounts, map[string]any{
			"id":               id,
			"accountId":        accountID.String,
			"cardId":           cardID.String,
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
		log.Printf("[BALANCE_ENQUIRY] Rows iteration error for user %d: %v", userIDInt, err)
	}

	log.Printf("[BALANCE_ENQUIRY] Processed %d rows, Returning %d Accounts for user %d", rowCount, len(accounts), userIDInt)

	log.Printf("[BALANCE_ENQUIRY] Returning User Accounts. [User] %d: %d Accounts, [Daily Limit]=%d, [Single TxLimit]=%d, [Daily Spent]=%d",
		userIDInt, len(accounts), dailyLimit, singleTxLimit, dailySpent)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"responseCode":           "00",
		"accounts":               accounts,
		"dailyLimit":             dailyLimit,
		"singleTransactionLimit": singleTxLimit,
		"dailySpent":             dailySpent,
		"status":                 "SUCCESS",
	})
}

// GetVirtualAccount retrieves or generates virtual account for merchant payments
// @Summary Get virtual account
// @Description Generate a temporary virtual account tied to merchant from JWT for payment collection
// @Tags accounts
// @Produce json
// @Param amount query int64 false "Payment amount"
// @Success 200 {object} object{responseCode=string,virtualAccount=object,expiresAt=string,status=string}
// @Failure 401 {object} services.ErrorResponse
// @Failure 403 {object} services.ErrorResponse
// @Failure 500 {object} services.ErrorResponse
// @Router /accounts/virtual-account [get]
// @Security BearerAuth
func (s *AccountService) GetVirtualAccount(w http.ResponseWriter, r *http.Request) {
	merchantID := r.Context().Value("merchantID")
	log.Printf("[VA_GEN] Request From [Merchant] --> %v", merchantID)

	if merchantID == nil {
		SendErrorResponse(w, "Only Merchants Generate Virtual Accounts", http.StatusForbidden, nil)
		return
	}

	merchantIDStr := fmt.Sprintf("%v", merchantID)
	log.Printf("[VA_GEN] Querying merchant: id=%s", merchantIDStr)

	var merchantName, status string
	err := s.db.QueryRow("SELECT business_name, status FROM merchants WHERE id = $1", merchantIDStr).Scan(&merchantName, &status)
	if err == sql.ErrNoRows {
		log.Printf("[VA_GEN] Merchant %s not found in database", merchantIDStr)
		SendErrorResponse(w, "Merchant Not Found", http.StatusForbidden, nil)
		return
	}
	if err != nil {
		log.Printf("[VA_GEN] Database Error Querying Merchant %s: %v (type: %T)", merchantIDStr, err, err)
		SendErrorResponse(w, "Database Error", http.StatusInternalServerError, nil)
		return
	}

	log.Printf("[VA_GEN] Merchant Found: [MerchantID] --> %s, [Name] --> %s, [Status] --> %s", merchantIDStr, merchantName, status)

	if status != "ACTIVE" {
		log.Printf("[VA_GEN] Merchant %s is Not Active (STATUS=%s)", merchantIDStr, status)
		SendErrorResponse(w, "Merchant not active", http.StatusForbidden, nil)
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
			log.Printf("[VA_GEN] Amount Specified: %d", amount)
		}
	}

	if s.redis != nil {
		ctx := context.Background()
		key := fmt.Sprintf("va:%s", accountNumber)
		data, _ := json.Marshal(vaData)
		if err := s.redis.Set(ctx, key, data, time.Duration(ttl)*time.Second).Err(); err != nil {
			log.Printf("[VA_GEN] Failed to store VA in Redis: %v", err)
			SendErrorResponse(w, "Failed to generate virtual account", http.StatusInternalServerError, nil)
			return
		}
		log.Printf("[VA_GEN] Successfully Generated VA. [Merchant] --> `%s`, [AccountNo] --> `%s`, [TTL] -->  (TTL: %ds)", merchantIDStr, accountNumber, ttl)
	} else {
		log.Printf("[VA_GEN] Warning: Redis not available, VA not persisted")
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"virtualAccount": VirtualAccountData{
			AccountNumber: accountNumber,
			AccountName:   merchantName,
			BankName:      "RuralPay Bank",
		},
		"expiresAt": expiresAt.Format(time.RFC3339),
		"status":    true,
	})
}

func generateVirtualAccountNumber() string {
	const digits = "0123456789"
	b := make([]byte, 10)
	for i := range b {
		b[i] = digits[rand.Intn(len(digits))]
	}
	return string(b)
}

// UpdateUserLimits updates user transaction limits
// @Summary Update user limits
// @Description Update daily and single transaction limits for authenticated user
// @Tags accounts
// @Accept json
// @Produce json
// @Param request body object{dailyLimit=int64,singleTransactionLimit=int64} true "Limit update request"
// @Success 200 {object} object{responseCode=string,dailyLimit=int64,singleTransactionLimit=int64,status=string}
// @Failure 401 {object} services.ErrorResponse
// @Failure 400 {object} services.ErrorResponse
// @Failure 500 {object} services.ErrorResponse
// @Router /accounts/limits [put]
// @Security BearerAuth
func (s *AccountService) UpdateUserLimits(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value("userID").(string)
	if !ok || userID == "" {
		SendErrorResponse(w, "Unauthorized", http.StatusUnauthorized, nil)
		return
	}

	userIDInt, err := strconv.Atoi(userID)
	if err != nil {
		SendErrorResponse(w, "Invalid user ID", http.StatusBadRequest, nil)
		return
	}

	var req struct {
		DailyLimit             int64 `json:"dailyLimit"`
		SingleTransactionLimit int64 `json:"singleTransactionLimit"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		SendErrorResponse(w, "Invalid request", http.StatusBadRequest, nil)
		return
	}

	if req.DailyLimit <= 0 || req.SingleTransactionLimit <= 0 {
		SendErrorResponse(w, "Limits Must Be Positive", http.StatusBadRequest, nil)
		return
	}

	if req.SingleTransactionLimit > req.DailyLimit {
		SendErrorResponse(w, "Single Transaction Limit Cannot Exceed Daily Limit", http.StatusBadRequest, nil)
		return
	}

	_, err = s.db.Exec(`
		INSERT INTO user_limits (user_id, daily_limit, single_transaction_limit, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (user_id) DO UPDATE
		SET daily_limit = $2, single_transaction_limit = $3, updated_at = NOW()
	`, userIDInt, req.DailyLimit, req.SingleTransactionLimit)

	if err != nil {
		log.Printf("[UPDATE_LIMITS] Failed To Update Limits. [User] %d: %v", userIDInt, err)
		SendErrorResponse(w, "Failed to Update Limits", http.StatusInternalServerError, nil)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"responseCode":           "00",
		"dailyLimit":             req.DailyLimit,
		"singleTransactionLimit": req.SingleTransactionLimit,
		"status":                 "SUCCESS",
	})
}
