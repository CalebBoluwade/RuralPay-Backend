package services

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/go-redis/redis/v8"
	"github.com/golang-jwt/jwt/v5"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
	"github.com/spf13/viper"
	"golang.org/x/crypto/argon2"
)

type AuthService struct {
	db           *sql.DB
	redis        *redis.Client
	validator    *validator.Validate
	notification *NotificationService
}

func NewAuthService(db *sql.DB, redisClient *redis.Client, notificationService *NotificationService) *AuthService {
	return &AuthService{
		db:           db,
		redis:        redisClient,
		validator:    validator.New(),
		notification: notificationService,
	}
}

// Register handles user registration
// @Summary Register a new user
// @Description Register a new user with email, password, and name
// @Tags auth
// @Accept json
// @Produce json
// @Param request body RegisterRequest true "Registration request"
// @Success 200 {object} AuthResponse "Registration successful"
// @Failure 400 {string} string utils.InvalidRequest
// @Failure 409 {string} string "Email already exists"
// @Failure 500 {string} string utils.InternalServiceError
// @Router /auth/register [post]
func (s *AuthService) Register(w http.ResponseWriter, r *http.Request) {
	slog.Info("auth.register.attempt", "remote_addr", r.RemoteAddr)

	maxBytes := 1_048_576 // 1 MB
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxBytes))

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req models.RegisterRequest
	if err := dec.Decode(&req); err != nil {
		slog.Error("auth.register.decode_failed", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		slog.Warn("auth.register.multiple_json_objects")
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		slog.Warn("auth.register.validation_failed", "error", err)
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	slog.Info("auth.register.request")

	hashedPassword, err := hashPassword(req.Password)
	if err != nil {
		slog.Error("auth.register.hash_failed", "error", err)
		utils.SendErrorResponse(w, "An Internal Error Occurred", http.StatusFailedDependency, nil)
		return
	}

	accountID := req.PhoneNumber[1:]
	slog.Info("auth.register.account_id_generated", "account_id", accountID)

	// Start transaction
	tx, err := s.db.Begin()
	if err != nil {
		slog.Error("auth.register.tx_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to create user", http.StatusFailedDependency, nil)
		return
	}
	defer tx.Rollback()

	// Insert user, and limits in a single CTE query
	var userID int
	err = tx.QueryRow(`
		WITH new_user AS (
			INSERT INTO users (email, username, password, first_name, last_name, account_id, bvn, phone_number, push_token)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING id
		),
		new_limits AS (
			INSERT INTO user_limits (user_id, daily_limit, single_transaction_limit, updated_at)
			SELECT id, $10, $11, NOW() FROM new_user
			RETURNING user_id
		)
		SELECT id FROM new_user
	`, strings.ToLower(req.Email), req.Username, hashedPassword, req.FirstName, req.LastName, accountID, req.BVN, req.PhoneNumber, req.ExpoPushToken, viper.GetInt64("user.default_daily_limit"), viper.GetInt64("user.default_single_tx_limit")).Scan(&userID)
	if err != nil {
		slog.Error("auth.register.user_creation_failed", "error", err)
		utils.SendErrorResponse(w, "Email or Username Already Exists", http.StatusConflict, nil)
		return
	}

	if err = tx.Commit(); err != nil {
		slog.Error("auth.register.commit_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to Create User", http.StatusFailedDependency, nil)
		return
	}

	slog.Info("auth.register.success", "user_id", userID)

	utils.SendSuccessResponse(w, "Registration Successful", map[string]any{"userId": userID}, http.StatusOK)
}

// Login handles user authentication
// @Summary Login user
// @Description Authenticate user with email and password
// @Tags auth
// @Accept json
// @Produce json
// @Param request body LoginRequest true "Login request"
// @Success 200 {object} AuthResponse "Login successful"
// @Failure 400 {string} string string(utils.InvalidRequest)
// @Failure 401 {string} string "Invalid Credentials"
// @Failure 500 {string} string "Internal server error"
// @Router /auth/login [post]
func (s *AuthService) Login(w http.ResponseWriter, r *http.Request) {
	maxBytes := 1_048_576 // 1 MB
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxBytes))

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req models.LoginRequest
	if err := dec.Decode(&req); err != nil {
		slog.Error("auth.login.decode_failed", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		slog.Warn("auth.login.multiple_json_objects")
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		slog.Warn("auth.login.validation_failed", "error", err)
		utils.SendErrorResponse(w, "Login Validation Failed", http.StatusBadRequest, err)
		return
	}

	slog.Info("auth.login.request")

	var user models.User
	var hashedPassword string

	// Variables to hold nullable merchant data
	var (
		mID              sql.NullInt64
		mBusinessName    sql.NullString
		mBusinessType    sql.NullString
		mTaxID           sql.NullString
		mStatus          sql.NullString
		mCommissionRate  sql.NullFloat64
		mSettlementCycle sql.NullString
		mCreatedAt       sql.NullTime
		mUpdatedAt       sql.NullTime
	)

	query := `
		SELECT 
			u.id, u.email, u.first_name, u.last_name, u.phone_number, u.bvn, u.username, password, u.account_id,
			m.id, m.business_name, m.business_type, m.tax_id, m.status, 
			m.commission_rate, m.settlement_cycle, m.created_at, m.updated_at
		FROM users u
		LEFT JOIN merchants m ON u.id = m.user_id
		WHERE u.phone_number = $1 OR LOWER(u.email) = LOWER($1) OR LOWER(u.username) = LOWER($1)`

	err := s.db.QueryRow(query, req.Identifier).Scan(
		&user.ID, &user.Email, &user.FirstName, &user.LastName, &user.PhoneNumber, &user.BVN, &user.Username, &hashedPassword, &user.AccountId,
		&mID, &mBusinessName, &mBusinessType, &mTaxID, &mStatus,
		&mCommissionRate, &mSettlementCycle, &mCreatedAt, &mUpdatedAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			slog.Warn("auth.login.user_not_found")
			utils.SendErrorResponse(w, "Invalid Credentials", http.StatusUnauthorized, nil)
		} else {
			slog.Error("auth.login.db_error", "error", err)
			utils.SendErrorResponse(w, utils.InternalServiceError, http.StatusFailedDependency, nil)
		}
		return
	}

	if !verifyPassword(req.Password, hashedPassword) {
		slog.Warn("auth.login.invalid_password")
		utils.SendErrorResponse(w, "Invalid Credentials", http.StatusUnauthorized, nil)
		return
	}

	slog.Info("auth.login.password_verified", "user_id", user.ID)

	user.Role = "consumer"

	if mID.Valid {
		merchant := models.Merchant{
			ID:              int(mID.Int64),
			UserID:          user.ID,
			BusinessName:    mBusinessName.String,
			BusinessType:    mBusinessType.String,
			TaxID:           mTaxID.String,
			Status:          mStatus.String,
			CommissionRate:  mCommissionRate.Float64,
			SettlementCycle: mSettlementCycle.String,
			CreatedAt:       mCreatedAt.Time,
			UpdatedAt:       mUpdatedAt.Time,
		}

		user.Role = "merchant"
		user.Merchant = &merchant

		slog.Info("auth.login.merchant_found", "user_id", user.ID, "merchant_id", int(mID.Int64), "status", mStatus.String)
	}

	var merchant models.Merchant
	if user.Merchant != nil {
		merchant = *user.Merchant
	}

	sessionID := fmt.Sprintf("%d_%d_%d", user.ID, time.Now().Unix(), rand.Int63())
	deviceID := fmt.Sprintf("%s_%s_%s", req.DeviceInfo.Platform, req.DeviceInfo.Model, req.DeviceInfo.OSVersion)

	token, err := generateJWTWithSession(user.ID, merchant, sessionID, deviceID)
	if err != nil {
		slog.Error("auth.login.jwt_failed", "user_id", user.ID, "error", err)
		utils.SendErrorResponse(w, "Failed to generate token", http.StatusFailedDependency, nil)
		return
	}

	refreshToken, err := generateRefreshToken(user.ID, sessionID)
	if err != nil {
		slog.Error("auth.login.refresh_token_failed", "user_id", user.ID, "error", err)
		utils.SendErrorResponse(w, "Failed to generate token", http.StatusFailedDependency, nil)
		return
	}

	if s.redis != nil {
		session := map[string]any{
			"user_id":    fmt.Sprintf("%d", user.ID),
			"device_id":  deviceID,
			"expires_at": fmt.Sprintf("%d", time.Now().Add(time.Duration(viper.GetInt("jwt.expiry_minutes"))*time.Minute).Unix()),
		}
		sessionData, _ := json.Marshal(session)
		ctx := context.Background()
		err := s.redis.Set(ctx, utils.SessionKeyPrefix+sessionID, sessionData, time.Duration(viper.GetInt("jwt.expiry_minutes"))*time.Minute).Err()
		if err != nil {
			slog.Error("auth.login.session_store_failed", "error", err)
		} else {
			slog.Info("auth.login.session_created", "session_id", sessionID)
		}
	}

	response := models.AuthResponse{
		Token:        token,
		RefreshToken: refreshToken,
		User:         user,
	}

	slog.Info("auth.login.success", "user_id", user.ID)

	utils.SendSuccessResponse(w, "Login Successful", response, http.StatusOK)
}

// Logout handles user logout
// @Summary Logout user
// @Description Logout user and blacklist token
// @Tags auth
// @Produce json
// @Success 200 {object} map[string]string "Logout successful"
// @Router /auth/logout [post]
func (s *AuthService) Logout(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")
	if token != "" && len(token) > 7 {
		token = token[7:] // Remove "Bearer " prefix

		if s.redis != nil {
			ctx := context.Background()

			// Parse token to get session ID
			claims := jwt.MapClaims{}
			jwt.ParseWithClaims(token, claims, func(token *jwt.Token) (any, error) {
				return []byte(viper.GetString("jwt.secret_key")), nil
			})

			if sid, ok := claims["sid"].(string); ok {
				s.redis.Del(ctx, utils.SessionKeyPrefix+sid)
			}

			// Blacklist token
			key := fmt.Sprintf("BLACKLISTED_TOKEN:%s", token)
			expiry := time.Duration(viper.GetInt("jwt.expiry_minutes")) * time.Minute
			if err := s.redis.Set(ctx, key, "1", expiry).Err(); err != nil {
				slog.Error("auth.logout.blacklist_failed", "error", err)
			}
		}
	}

	utils.SendSuccessResponse(w, "Logout Successful", nil, http.StatusOK)
}

// GetUserAccount retrieves user account details from auth token
// @Summary Get user account details
// @Description Get authenticated user's account information
// @Tags auth
// @Produce json
// @Success 200 {object} User "User account details"
// @Failure 401 {string} string string(utils.UnauthorizedError)
// @Failure 500 {string} string "Internal server error"
// @Router /auth/account [get]
func (s *AuthService) GetUserAccount(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("userID")
	if userID == nil {
		slog.Warn("auth.account.unauthorized")
		http.Error(w, string(utils.UnauthorizedError), http.StatusUnauthorized)
		return
	}

	var user models.User
	err := s.db.QueryRow("SELECT users.id, email, first_name, last_name, phone_number, users.account_id FROM users LEFT JOIN accounts ON users.id = accounts.user_id WHERE users.id = $1",
		userID).Scan(&user.ID, &user.Email, &user.FirstName, &user.LastName, &user.PhoneNumber, &user.AccountId)
	if err != nil {
		if err == sql.ErrNoRows {
			slog.Warn("auth.account.not_found", "user_id", userID)
			http.Error(w, "User not found", http.StatusNotFound)
		} else {
			slog.Error("auth.account.db_error", "user_id", userID, "error", err)
			http.Error(w, "Failed to fetch user details", http.StatusFailedDependency)
		}
		return
	}

	utils.SendSuccessResponse(w, "", user, http.StatusOK)
}

func generateJWTWithSession(userID int, merchant models.Merchant, sessionID, deviceID string) (string, error) {
	claims := jwt.MapClaims{
		"user_id": userID,
		"sub":     userID,
		"sid":     sessionID,
		"did":     deviceID,
		"exp":     time.Now().Add(time.Duration(viper.GetInt("jwt.expiry_minutes")) * time.Minute).Unix(),
		"iss":     viper.GetString("jwt.issuer"),
		"aud":     viper.GetString("jwt.audience"),
	}

	if merchant.ID != 0 {
		claims["merchant_id"] = merchant.ID
	}

	if merchant.Status != "" {
		claims["merchant_status"] = merchant.Status
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(viper.GetString("jwt.secret_key")))
}

func generateRefreshToken(userID int, sessionID string) (string, error) {
	claims := jwt.MapClaims{
		"user_id": userID,
		"sid":     sessionID,
		"exp":     time.Now().Add(7 * 24 * time.Hour).Unix(), // 7 days
		"type":    "refresh",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(viper.GetString("jwt.secret_key")))
}

// RefreshToken handles token refresh
// @Summary Refresh access token
// @Description Get new access token using refresh token
// @Tags auth
// @Accept json
// @Produce json
// @Param request body map[string]string true "Refresh token"
// @Success 200 {object} AuthResponse "Token refreshed"
// @Failure 401 {string} string "Invalid refresh token"
// @Router /auth/refresh [post]
func (s *AuthService) RefreshToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refreshToken" validate:"required"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(req.RefreshToken, claims, func(token *jwt.Token) (any, error) {
		return []byte(viper.GetString("jwt.secret_key")), nil
	})

	if err != nil || !token.Valid || claims["type"] != "refresh" {
		utils.SendErrorResponse(w, "Invalid refresh token", http.StatusUnauthorized, nil)
		return
	}

	userID := int(claims["user_id"].(float64))
	sessionID := claims["sid"].(string)

	// Verify session exists
	if s.redis != nil {
		ctx := context.Background()
		exists, _ := s.redis.Exists(ctx, utils.SessionKeyPrefix+sessionID).Result()
		if exists == 0 {
			utils.SendErrorResponse(w, "Session Expired", http.StatusUnauthorized, nil)
			return
		}
	}

	// Get user and merchant info
	var user models.User
	var mID sql.NullInt64
	err = s.db.QueryRow(`
		SELECT u.id, u.email, u.first_name, u.last_name, u.phone_number, u.username, u.account_id, m.id
		FROM users u LEFT JOIN merchants m ON u.id = m.user_id WHERE u.id = $1
	`, userID).Scan(&user.ID, &user.Email, &user.FirstName, &user.LastName, &user.PhoneNumber, &user.Username, &user.AccountId, &mID)

	if err != nil {
		utils.SendErrorResponse(w, utils.UserNotFoundError, http.StatusUnauthorized, nil)
		return
	}

	var merchant models.Merchant
	if mID.Valid {
		merchant.ID = int(mID.Int64)
	}

	// Get device ID from session
	var sessionData map[string]string
	if s.redis != nil {
		ctx := context.Background()
		data, _ := s.redis.Get(ctx, utils.SessionKeyPrefix+sessionID).Result()
		json.Unmarshal([]byte(data), &sessionData)
	}
	deviceID := sessionData["device_id"]

	// Generate new tokens
	newAccessToken, _ := generateJWTWithSession(userID, merchant, sessionID, deviceID)
	newRefreshToken, _ := generateRefreshToken(userID, sessionID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models.AuthResponse{
		Token:        newAccessToken,
		RefreshToken: newRefreshToken,
		User:         user,
	})
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, viper.GetInt("argon2.salt_length"))
	if _, err := cryptorand.Read(salt); err != nil {
		return "", err
	}

	hash := argon2.IDKey([]byte(password), salt,
		uint32(viper.GetInt("argon2.time")),
		uint32(viper.GetInt("argon2.memory")),
		uint8(viper.GetInt("argon2.threads")),
		uint32(viper.GetInt("argon2.key_length")))
	return fmt.Sprintf("%s$%s", base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(hash)), nil
}

func verifyPassword(password, hashedPassword string) bool {
	parts := strings.Split(hashedPassword, "$")
	if len(parts) != 2 {
		return false
	}

	salt, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}

	hash, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}

	computedHash := argon2.IDKey([]byte(password), salt,
		uint32(viper.GetInt("argon2.time")),
		uint32(viper.GetInt("argon2.memory")),
		uint8(viper.GetInt("argon2.threads")),
		uint32(viper.GetInt("argon2.key_length")))
	return string(hash) == string(computedHash)
}

// func generateAccountID() string {
// 	const digits = "0123456789"
// 	b := make([]byte, 10)
// 	for i := range b {
// 		b[i] = digits[rand.Intn(len(digits))]
// 	}
// 	return string(b)
// }

// ForgotPassword handles password reset request
// @Summary Request password reset
// @Description Send password reset token to user's email
// @Tags auth
// @Accept json
// @Produce json
// @Param request body map[string]string true "Email address"
// @Success 200 {object} map[string]string "Reset token sent"
// @Failure 400 {string} string string(utils.InvalidRequest)
// @Failure 404 {string} string "User not found"
// @Router /auth/forgot-password [post]
func (s *AuthService) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"identifier" validate:"required" example:"+2348012345678"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("auth.forgot_password.decode_failed", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		slog.Warn("auth.forgot_password.validation_failed", "error", err)
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	var userID int
	err := s.db.QueryRow("SELECT id FROM users WHERE phone_number = $1 OR LOWER(email) = LOWER($1) OR LOWER(username) = LOWER($1)", req.Identifier).Scan(&userID)
	if err != nil {
		slog.Warn("auth.forgot_password.user_not_found")
		utils.SendErrorResponse(w, "User not found", http.StatusNotFound, nil)
		return
	}

	var user models.User
	err = s.db.QueryRow("SELECT email, phone_number FROM users WHERE id = $1", userID).Scan(&user.Email, &user.PhoneNumber)
	if err != nil {
		slog.Error("auth.forgot_password.fetch_failed", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, "Failed to process request", http.StatusFailedDependency, nil)
		return
	}

	otp := utils.GenerateOTP()

	if s.redis != nil {
		ctx := context.Background()
		redisKey := fmt.Sprintf("RESET_USER:%d", userID)
		existingOTP, err := s.redis.Get(ctx, redisKey).Result()
		if err == nil {
			slog.Info("auth.forgot_password.otp_resend", "user_id", userID)
			otp = existingOTP
		} else {
			if err = s.redis.Set(ctx, redisKey, otp, 30*time.Minute).Err(); err != nil {
				slog.Error("auth.forgot_password.otp_store_failed", "error", err)
				utils.SendErrorResponse(w, "Failed to process request", http.StatusFailedDependency, nil)
				return
			}
			slog.Info("auth.forgot_password.otp_stored", "user_id", userID)
		}
	}

	// Send OTP via notification
	if s.notification != nil {
		go s.notification.SendOTPEmail(user.Email, otp, "10 minutes", models.ForgotPassword)
		slog.Info("auth.forgot_password.otp_sent", "user_id", userID)
	}

	slog.Info("auth.forgot_password.success", "user_id", userID)

	utils.SendSuccessResponse(w, "Password Reset Code Sent", nil, http.StatusOK)
}

// ResetPassword handles password reset with token
// @Summary Reset password
// @Description Reset user password using reset token
// @Tags auth
// @Accept JSON
// @Produce JSON
// @Param request body map[string]string true "Reset token and new password"
// @Success 200 {object} map[string]string "Password reset successful"
// @Failure 400 {string} string utils.InvalidRequest
// @Failure 401 {string} string utils.TokenError
// @Router /auth/reset-password [post]
func (s *AuthService) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token" validate:"required"`
		Password string `json:"password" validate:"required,min=6"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("auth.reset_password.decode_failed", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		slog.Warn("auth.reset_password.validation_failed", "error", err)
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	var userID int
	if s.redis != nil {
		ctx := context.Background()
		// Scan for RESET_USER:* key matching this OTP
		var cursor uint64
		keys, _, err := s.redis.Scan(ctx, cursor, "RESET_USER:*", 100).Result()
		if err != nil {
			slog.Error("auth.reset_password.redis_scan_failed", "error", err)
			utils.SendErrorResponse(w, utils.TokenError, http.StatusBadRequest, nil)
			return
		}
		for _, key := range keys {
			val, err := s.redis.Get(ctx, key).Result()
			if err == nil && val == req.Token {
				fmt.Sscanf(key, "RESET_USER:%d", &userID)
				break
			}
		}
		if userID == 0 {
			slog.Warn("auth.reset_password.invalid_token")
			utils.SendErrorResponse(w, utils.TokenError, http.StatusBadRequest, nil)
			return
		}
		slog.Info("auth.reset_password.otp_validated", "user_id", userID)
	}

	hashedPassword, err := hashPassword(req.Password)
	if err != nil {
		slog.Error("auth.reset_password.hash_failed", "error", err)
		utils.SendErrorResponse(w, utils.PasswordResetError, http.StatusFailedDependency, nil)
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		slog.Error("auth.reset_password.tx_failed", "error", err)
		utils.SendErrorResponse(w, utils.PasswordResetError, http.StatusFailedDependency, nil)
		return
	}
	defer tx.Rollback()

	_, err = tx.Exec("UPDATE users SET password = $1 WHERE id = $2", hashedPassword, userID)
	if err != nil {
		slog.Error("auth.reset_password.update_failed", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, utils.PasswordResetError, http.StatusFailedDependency, nil)
		return
	}

	if err = tx.Commit(); err != nil {
		slog.Error("auth.reset_password.commit_failed", "error", err)
		utils.SendErrorResponse(w, utils.PasswordResetError, http.StatusFailedDependency, nil)
		return
	}

	if s.redis != nil {
		ctx := context.Background()
		s.redis.Del(ctx, fmt.Sprintf("RESET_USER:%d", userID))
		slog.Info("auth.reset_password.otp_deleted", "user_id", userID)
	}

	slog.Info("auth.reset_password.success", "user_id", userID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"message": "Password Reset successful", "success": true})
}

func (s *AuthService) CheckUserStatus(userID string) (bool, error) {
	var status string
	err := s.db.QueryRow("SELECT status FROM users WHERE id = $1", userID).Scan(&status)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}

	return status == "active", nil
}
