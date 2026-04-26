package services

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/go-redis/redis/v8"
	"github.com/golang-jwt/jwt/v5"
	"github.com/ruralpay/backend/internal/constants"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
	"github.com/spf13/viper"
	"golang.org/x/crypto/argon2"
)

type UserService struct {
	db                   *sql.DB
	redis                *redis.Client
	validator            *validator.Validate
	useEncryptedPassword bool
	notificationSvc      *NotificationService
	hsm                  hsm.HSMInterface
}

func NewUserService(db *sql.DB, redisClient *redis.Client, hsmInstance hsm.HSMInterface, notificationService *NotificationService) *UserService {
	return &UserService{
		db:                   db,
		redis:                redisClient,
		hsm:                  hsmInstance,
		validator:            validator.New(),
		useEncryptedPassword: viper.GetBool("auth.use_encrypted_password"),
		notificationSvc:      notificationService,
	}
}

// RegisterNewUser handles user registration
// @Summary Register a new user
// @Description Register a new user with email, password, and name
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body models.RegisterRequest true "Registration request"
// @Success 200 {object} models.AuthResponse "Registration successful"
// @Failure 400 {string} string "Invalid request"
// @Failure 409 {string} string "Email already exists"
// @Failure 500 {string} string "Internal server error"
// @Router /auth [post]
func (s *UserService) RegisterNewUser(w http.ResponseWriter, r *http.Request) {
	slog.Info("auth.register.attempt", "remote_addr", utils.RealIP(r))

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

	slog.Debug("auth.register.request", "req", req)

	hashedPassword, err := hashPassword(req.Password)
	if err != nil {
		slog.Error("auth.register.hash_failed", "error", err)
		utils.SendErrorResponse(w, utils.InternalServiceError, http.StatusFailedDependency, nil)
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
			INSERT INTO users (email, username, password, first_name, last_name, account_id, bvn, phone_number, push_token, identityToken)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			RETURNING id
		),
		new_limits AS (
			INSERT INTO user_limits (user_id, daily_limit, single_transaction_limit, updated_at)
			SELECT id, $11, $12, NOW() FROM new_user
			RETURNING user_id
		),
		new_notifications AS (
			INSERT INTO notifications (user_id, use_device_push, use_sms, use_email, updated_at)
			SELECT id, TRUE, FALSE, FALSE, NOW() FROM new_user
		)
	`, strings.ToLower(req.Email), req.Username, hashedPassword, req.FirstName, req.LastName, accountID, req.BVN, req.PhoneNumber, req.ExpoPushToken, req.IdentityToken, viper.GetInt64("user.default_daily_limit"), viper.GetInt64("user.default_single_tx_limit")).Scan(&userID)
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

	if s.notificationSvc != nil {
		go s.notificationSvc.SendRegisterEmail(&models.User{
			ID:        userID,
			FirstName: req.FirstName,
			LastName:  req.LastName,
			Email:     req.Email,
		})
	}

	utils.SendSuccessResponse(w, "Registration Successful", map[string]any{"userId": userID}, http.StatusOK)
}

// UserLogin handles user authentication
// @Summary Login user
// @Description Authenticate user with email and password
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body models.LoginRequest true "Login request"
// @Success 200 {object} models.AuthResponse "Login successful"
// @Failure 400 {string} string "Invalid request"
// @Failure 401 {string} string "Invalid credentials"
// @Failure 500 {string} string "Internal server error"
// @Router /auth/login [post]
func (s *UserService) UserLogin(w http.ResponseWriter, r *http.Request) {
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

	slog.Debug("auth.login.request", "LoginRequest", req)

	reqCtx := r.Context()

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
			m.commission_rate, m.settlement_cycle, m.created_at, m.updated_at,
			ul.daily_limit, ul.single_transaction_limit,
			un.use_device_push, un.use_sms, un.use_email
		FROM users u
		LEFT JOIN merchants m ON u.id = m.user_id
		LEFT JOIN user_limits ul ON u.id = ul.user_id
		LEFT JOIN notifications un ON u.id = un.user_id
		WHERE (u.phone_number = $1 OR LOWER(u.email) = LOWER($1) OR LOWER(u.username) = LOWER($1))
		  AND u.deleted_at IS NULL`

	var (
		lDailyLimit    sql.NullFloat64
		lSingleTxLimit sql.NullFloat64
		nDevicePush    sql.NullBool
		nSMS           sql.NullBool
		nEmail         sql.NullBool
	)
	err := s.db.QueryRowContext(reqCtx, query, req.Identifier).Scan(
		&user.ID, &user.Email, &user.FirstName, &user.LastName, &user.PhoneNumber, &user.BVN, &user.Username, &hashedPassword, &user.AccountId,
		&mID, &mBusinessName, &mBusinessType, &mTaxID, &mStatus,
		&mCommissionRate, &mSettlementCycle, &mCreatedAt, &mUpdatedAt,
		&lDailyLimit, &lSingleTxLimit,
		&nDevicePush, &nSMS, &nEmail,
	)
	user.TransactionLimits.DailyLimit = lDailyLimit.Float64
	user.TransactionLimits.SingleTransactionLimit = lSingleTxLimit.Float64
	user.Notifications.DevicePush = nDevicePush.Bool
	user.Notifications.SMS = nSMS.Bool
	user.Notifications.Email = nEmail.Bool

	user.Notifications.UserID = user.ID

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			slog.Warn("auth.login.user_not_found")
			utils.SendErrorResponse(w, utils.InvalidCreds, http.StatusUnauthorized, nil)
		} else {
			slog.Error("auth.login.db_error", "error", err)
			utils.SendErrorResponse(w, utils.InternalServiceError, http.StatusFailedDependency, nil)
		}
		return
	}

	var password = req.Password

	if s.useEncryptedPassword {
		decryptedPassword, err := s.hsm.DecryptPII(password)
		if err != nil {
			slog.Error("auth.login.decrypt_failed", "error", err)
			utils.SendErrorResponse(w, utils.InvalidCreds, http.StatusUnauthorized, nil)
			return
		}

		password = decryptedPassword
	}

	if !verifyPassword(password, hashedPassword) {
		slog.Warn("auth.login.invalid_password")
		utils.SendErrorResponse(w, utils.InvalidCreds, http.StatusUnauthorized, nil)
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

	sessionID, err := generateSessionID(user.ID)
	if err != nil {
		slog.Error("auth.login.session_id_failed", "user_id", user.ID, "error", err)
		utils.SendErrorResponse(w, utils.GenerateTokenError, http.StatusInternalServerError, nil)
		return
	}
	deviceID := fmt.Sprintf("%s_%s_%s", req.DeviceInfo.Platform, req.DeviceInfo.Model, req.DeviceInfo.OSVersion)

	token, err := s.generateJWTWithSession(reqCtx, user.ID, merchant, sessionID, deviceID)
	if err != nil {
		slog.Error("auth.login.jwt_failed", "user_id", user.ID, "error", err)
		utils.SendErrorResponse(w, utils.GenerateTokenError, http.StatusFailedDependency, nil)
		return
	}

	refreshToken, err := generateRefreshToken(user.ID, sessionID)
	if err != nil {
		slog.Error("auth.login.refresh_token_failed", "user_id", user.ID, "error", err)
		utils.SendErrorResponse(w, utils.GenerateTokenError, http.StatusFailedDependency, nil)
		return
	}

	if s.redis != nil {
		session := map[string]any{
			"user_id":                fmt.Sprintf("%d", user.ID),
			"device_id":              deviceID,
			"expires_at":             fmt.Sprintf("%d", time.Now().Add(refreshTokenExpiry).Unix()),
			"notificationPreference": user.Notifications,
		}
		sessionData, _ := json.Marshal(session)
		ctx := context.Background()
		err := s.redis.Set(ctx, constants.SessionKeyPrefix+sessionID, sessionData, refreshTokenExpiry).Err()
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

	if s.notificationSvc != nil {
		go s.notificationSvc.SendLoginEmail(&user, deviceID)
	}

	utils.SendSuccessResponse(w, "Login Successful", response, http.StatusOK)
}

// LogoutUser handles user logout
// @Summary Logout user
// @Description Logout user and blacklist token
// @Tags Auth
// @Produce json
// @Success 200 {object} map[string]string "Logout successful"
// @Security BearerAuth
// @Router /auth/logout [post]
func (s *UserService) LogoutUser(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if len(authHeader) > 7 && s.redis != nil {
		accessToken := authHeader[7:]
		ctx := context.Background()

		claims := jwt.MapClaims{}
		jwt.ParseWithClaims(accessToken, claims, func(token *jwt.Token) (any, error) {
			return []byte(viper.GetString("jwt.secret_key")), nil
		}, jwt.WithoutClaimsValidation())

		if sid, ok := claims["sid"].(string); ok {
			s.redis.Del(ctx, constants.SessionKeyPrefix+sid)
		}

		if exp, ok := claims["exp"].(float64); ok {
			if ttl := time.Until(time.Unix(int64(exp), 0)); ttl > 0 {
				s.blacklistToken(ctx, accessToken, ttl)
			}
		}
	}

	utils.SendSuccessResponse(w, "Logout Successful", nil, http.StatusOK)
}

// blacklistToken adds a token to the revocation list with a TTL matching its
// remaining validity window. Uses the same key prefix as checkTokenBlacklist
// in auth middleware.
func (s *UserService) blacklistToken(ctx context.Context, token string, ttl time.Duration) {
	key := constants.BlacklistKeyPrefix + token
	if err := s.redis.Set(ctx, key, "1", ttl).Err(); err != nil {
		slog.Error("auth.blacklist_token.failed", "error", err)
	}
}

// GetUserAccount retrieves user account details from auth token
// @Summary Get user account details
// @Description Get authenticated user's account information
// @Tags Auth
// @Produce json
// @Success 200 {object} models.User "User account details"
// @Failure 401 {string} string "Unauthorized"
// @Failure 500 {string} string "Internal server error"
// @Security BearerAuth
// @Router /auth/account [get]
func (s *UserService) GetUserAccount(w http.ResponseWriter, r *http.Request) {
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	// Notifications, limits

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

	utils.SendSuccessResponse(w, "User Account Details", user, http.StatusOK)
}

// EditUserProfile Edit user account details
// @Summary Edit user account
// @Description Update authenticated user's account information
// @Tags Auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Router /auth [put]
func (s *UserService) EditUserProfile(w http.ResponseWriter, r *http.Request) {}

// DeleteUserProfile deletes user account details from auth token
// @Summary Delete user account
// @Description Soft-delete the authenticated user's account
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body object{password=string} true "Password confirmation"
// @Success 200 {string} string "Account Deleted Successfully"
// @Failure 401 {string} string "Unauthorized"
// @Failure 409 {string} string "Account has non-zero balance or pending transactions"
// @Failure 500 {string} string "Internal server error"
// @Security BearerAuth
// @Router /auth [delete]
func (s *UserService) DeleteUserProfile(w http.ResponseWriter, r *http.Request) {
	reqCtx := r.Context()
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, reqCtx)

	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	var req struct {
		Password string `json:"password" validate:"required,min=6,max=72"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}
	if err := s.validator.Struct(&req); err != nil {
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	// Verify password and confirm account is not already deleted.
	var hashedPassword string
	var deletedAt sql.NullTime
	err := s.db.QueryRowContext(reqCtx,
		`SELECT password, deleted_at FROM users WHERE id = $1`, userID,
	).Scan(&hashedPassword, &deletedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			utils.SendErrorResponse(w, utils.UserNotFoundError, http.StatusNotFound, nil)
		} else {
			slog.Error("auth.delete.fetch_failed", "user_id", userID, "error", err)
			utils.SendErrorResponse(w, utils.InternalServiceError, http.StatusInternalServerError, nil)
		}
		return
	}
	if deletedAt.Valid {
		utils.SendErrorResponse(w, "Account Already Deleted", http.StatusGone, nil)
		return
	}
	if !verifyPassword(req.Password, hashedPassword) {
		utils.SendErrorResponse(w, "Invalid Credentials", http.StatusUnauthorized, nil)
		return
	}

	// Block if any account holds a non-zero balance.
	var nonZeroBalances int
	if err := s.db.QueryRowContext(reqCtx,
		`SELECT COUNT(*) FROM accounts WHERE user_id = $1 AND balance <> 0`, userID,
	).Scan(&nonZeroBalances); err != nil {
		slog.Error("auth.delete.balance_check_failed", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, utils.InternalServiceError, http.StatusInternalServerError, nil)
		return
	}
	if nonZeroBalances > 0 {
		utils.SendErrorResponse(w, "Account balance must be zero before deletion", http.StatusConflict, nil)
		return
	}

	// Block if any pending or processing transactions exist.
	var pendingTx int
	if err := s.db.QueryRowContext(reqCtx,
		`SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND status IN ('PENDING', 'PROCESSING')`, userID,
	).Scan(&pendingTx); err != nil {
		slog.Error("auth.delete.pending_tx_check_failed", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, utils.InternalServiceError, http.StatusInternalServerError, nil)
		return
	}
	if pendingTx > 0 {
		utils.SendErrorResponse(w, "Cannot delete account with pending transactions", http.StatusConflict, nil)
		return
	}

	// Soft delete: stamp deleted_at, set status to inactive, and anonymise PII
	// so the row satisfies unique constraints for email/phone on future
	// registrations (handled by the partial unique indexes in migration 032).
	_, err = s.db.ExecContext(reqCtx, `
		UPDATE users
		SET
			status           = 'inactive',
			deleted_at       = NOW(),
			deletion_reason  = 'USER_REQUEST',
			email            = 'deleted_' || id || '@ruralpay.invalid',
			phone_number     = NULL,
			password         = '',
			updated_at       = NOW()
		WHERE id = $1
	`, userID)
	if err != nil {
		slog.Error("auth.delete.soft_delete_failed", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, utils.InternalServiceError, http.StatusInternalServerError, nil)
		return
	}

	// Revoke all active sessions and blacklist the current access token.
	if s.redis != nil {
		ctx := context.Background()

		if authHeader := r.Header.Get("Authorization"); len(authHeader) > 7 {
			s.blacklistToken(ctx, authHeader[7:], accessTokenExpiry())
		}

		var cursor uint64
		for {
			keys, next, err := s.redis.Scan(
				ctx, cursor,
				fmt.Sprintf("%s%d_*", constants.SessionKeyPrefix, userID),
				100,
			).Result()
			if err != nil {
				slog.Error("auth.delete.redis_scan_failed", "user_id", userID, "error", err)
				break
			}
			if len(keys) > 0 {
				s.redis.Del(ctx, keys...)
			}
			if next == 0 {
				break
			}
			cursor = next
		}
	}

	slog.Info("auth.delete.success", "user_id", userID)

	if s.notificationSvc != nil {
		var deletedUser models.User
		s.db.QueryRow(`SELECT first_name, email FROM users WHERE id = $1`, userID).Scan(&deletedUser.FirstName, &deletedUser.Email)
		deletedUser.ID = userID
		go s.notificationSvc.SendDeleteAccountEmail(&deletedUser)
	}

	utils.SendSuccessResponse(w, "Account Deleted Successfully", nil, http.StatusOK)
}

const (
	minAccessTokenExpiry = 5 * time.Minute
	defaultAccessExpiry  = 15 * time.Minute
	refreshTokenExpiry   = 7 * 24 * time.Hour
)

func accessTokenExpiry() time.Duration {
	d := time.Duration(viper.GetInt("jwt.expiry_minutes")) * time.Minute
	if d < minAccessTokenExpiry {
		d = defaultAccessExpiry
	}
	return d
}

func (s *UserService) generateJWTWithSession(ctx context.Context, userID int, merchant models.Merchant, sessionID, deviceID string) (string, error) {
	expiry := accessTokenExpiry()
	now := time.Now()

	isActive, isAdmin, err := s.CheckUserStatusAndPrivileges(ctx, strconv.Itoa(userID))

	if err != nil {
		slog.Error("auth.check_user_status_failed", "user_id", userID, "error", err)
		return "", fmt.Errorf("failed to check user status")
	}

	claims := jwt.MapClaims{
		"user_id":  userID,
		"sub":      userID,
		"sid":      sessionID,
		"isActive": isActive,
		"isAdmin":  isAdmin,
		"did":      deviceID,
		"iat":      now.Unix(),
		"exp":      now.Add(expiry).Unix(),
		"iss":      viper.GetString("jwt.issuer"),
		"aud":      viper.GetString("jwt.audience"),
	}

	slog.Info("auth.generate_jwt_session", "expiry", expiry)

	if merchant.ID != 0 {
		claims["merchant_id"] = merchant.ID
	}
	if merchant.Status != "" {
		claims["merchant_status"] = merchant.Status
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(viper.GetString("jwt.secret_key")))
}

func generateSessionID(userID int) (string, error) {
	b := make([]byte, 16)
	if _, err := cryptorand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d_%x", userID, b), nil
}

func generateRefreshToken(userID int, sessionID string) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"user_id": userID,
		"sid":     sessionID,
		"iat":     now.Unix(),
		"exp":     now.Add(refreshTokenExpiry).Unix(),
		"type":    "refresh",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(viper.GetString("jwt.secret_key")))
}

// RefreshToken handles token refresh
// @Summary Refresh access token
// @Description Get new access token using refresh token
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body object{refreshToken=string} true "Refresh token"
// @Success 200 {object} models.AuthResponse "Token refreshed"
// @Failure 401 {string} string "Invalid refresh token"
// @Router /auth/refresh [post]
func (s *UserService) RefreshToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refreshToken" validate:"required"`
	}

	reqCtx := r.Context()

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("refresh_token.decode_failed", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(req.RefreshToken, claims, func(token *jwt.Token) (any, error) {
		return []byte(viper.GetString("jwt.secret_key")), nil
	})

	if err != nil || !token.Valid || claims["type"] != "refresh" {
		slog.Error("refresh_token.validate_failed", "error", err)
		utils.SendErrorResponse(w, "Invalid refresh token", http.StatusUnauthorized, nil)
		return
	}

	userID := int(claims["user_id"].(float64))
	sessionID := claims["sid"].(string)

	// Verify session exists
	if s.redis != nil {
		ctx := context.Background()
		exists, _ := s.redis.Exists(ctx, constants.SessionKeyPrefix+sessionID).Result()
		if exists == 0 {
			slog.Error("auth.delete.redis_scan_failed", "user_id", userID, "session_id", sessionID)
			utils.SendErrorResponse(w, "Session Expired", http.StatusUnauthorized, nil)
			return
		}
	}

	// Get user and merchant info
	var user models.User
	var mID sql.NullInt64
	err = s.db.QueryRowContext(reqCtx, `
		SELECT u.id, u.email, u.first_name, u.last_name, u.phone_number, u.username, u.account_id, m.id
		FROM users u LEFT JOIN merchants m ON u.id = m.user_id WHERE u.id = $1
	`, userID).Scan(&user.ID, &user.Email, &user.FirstName, &user.LastName, &user.PhoneNumber, &user.Username, &user.AccountId, &mID)

	if err != nil {
		slog.Error("auth.refresh_failed", "user_id", userID, "error", err)
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
		data, _ := s.redis.Get(ctx, constants.SessionKeyPrefix+sessionID).Result()
		json.Unmarshal([]byte(data), &sessionData)
	}
	deviceID := sessionData["device_id"]

	// Rotate: blacklist the consumed refresh token for its remaining lifetime
	// so it cannot be reused even if intercepted.
	if s.redis != nil {
		ctx := context.Background()
		var remainingTTL time.Duration
		if exp, ok := claims["exp"].(float64); ok {
			remainingTTL = time.Until(time.Unix(int64(exp), 0))
		}
		if remainingTTL > 0 {
			s.blacklistToken(ctx, req.RefreshToken, remainingTTL)
		}
	}

	newAccessToken, err := s.generateJWTWithSession(reqCtx, userID, merchant, sessionID, deviceID)
	if err != nil {
		slog.Error("auth.refresh.access_token_failed", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, utils.GenerateTokenError, http.StatusInternalServerError, nil)
		return
	}
	newRefreshToken, err := generateRefreshToken(userID, sessionID)
	if err != nil {
		slog.Error("auth.refresh.refresh_token_failed", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, utils.GenerateTokenError, http.StatusInternalServerError, nil)
		return
	}

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
// @Description Send password reset OTP to user's email or phone
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body object{identifier=string} true "Phone, email, or username"
// @Success 200 {string} string "Password Reset Code Sent"
// @Failure 400 {string} string "Invalid request"
// @Failure 404 {string} string "User not found"
// @Router /auth/forgot-password [post]
func (s *UserService) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"identifier" validate:"required,max=254" example:"+2348012345678"`
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
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusFailedDependency, nil)
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
				utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusFailedDependency, nil)
				return
			}
			slog.Info("auth.forgot_password.otp_stored", "user_id", userID)
		}
	}

	// Send OTP via notification
	if s.notificationSvc != nil {
		go s.notificationSvc.SendOTPEmail(user.Email, otp, "10 minutes", models.ForgotPassword)
		slog.Info("auth.forgot_password.otp_sent", "user_id", userID)
	}

	slog.Info("auth.forgot_password.success", "user_id", userID)

	utils.SendSuccessResponse(w, "Password Reset Code Sent", nil, http.StatusOK)
}

// ResetPassword handles password reset with token
// @Summary Reset password
// @Description Reset user password using OTP reset token
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body object{token=string,password=string} true "Reset token and new password"
// @Success 200 {object} map[string]string "Password reset successful"
// @Failure 400 {string} string "Invalid request or token"
// @Router /auth/reset-password [post]
func (s *UserService) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token" validate:"required,max=20"`
		Password string `json:"password" validate:"required,min=6,max=72"`
	}

	reqCtx := r.Context()

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

	_, err = tx.ExecContext(reqCtx, "UPDATE users SET password = $1 WHERE id = $2", hashedPassword, userID)
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
	utils.SendSuccessResponse(w, "Password Reset Successful", nil, http.StatusOK)
}

// CheckUserStatusAndPrivileges checks both user status and admin privileges in a single query
func (s *UserService) CheckUserStatusAndPrivileges(ctx context.Context, userID string) (isActive, isAdmin bool, err error) {
	slog.Debug("user.check_status_privileges", "user_id", userID)
	var status string
	var adminFlag sql.NullBool

	err = s.db.QueryRowContext(ctx, `
	SELECT 
		u.status, 
		(a.user_id IS NOT NULL) AS is_admin
	FROM users u
	LEFT JOIN admins a 
		ON u.id = a.user_id
	WHERE u.id = $1
	`, userID).Scan(&status, &adminFlag)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, false, nil
		}

		slog.Error("user.check_status_privileges.error", "user_id", userID, "error", err)
		return false, false, err
	}

	isActive = status == "active"
	isAdmin = adminFlag.Valid && adminFlag.Bool

	slog.Debug("user.status_privileges_result", "user_id", userID, "is_active", isActive, "is_admin", isAdmin)
	return isActive, isAdmin, nil
}

// // Deprecated: Use CheckUserStatusAndPrivileges instead
// func (s *UserService) CheckUserStatus(userID string) (bool, error) {
// 	active, _, err := s.CheckUserStatusAndPrivileges(userID)
// 	return active, err
// }

// // Deprecated: Use CheckUserStatusAndPrivileges instead
// func (s *UserService) CheckUserIsAdmin(userID string) (bool, error) {
// 	_, isAdmin, err := s.CheckUserStatusAndPrivileges(userID)
// 	return isAdmin, err
// }

func (s *UserService) UserFeedback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email            string `json:"email" validate:"required,email,max=254"`
		MostLovedFeature string `json:"mostLovedFeature" validate:"required,max=500"`
		MostHatedFeature string `json:"mostHatedFeature" validate:"required,max=500"`
		NiceHaveFeature  string `json:"niceHaveFeature" validate:"required,max=500"`
		GeneralFeedback  string `json:"generalFeedback" validate:"required,max=1000"`
	}

	reqCtx := r.Context()

	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("feedback.decode_failed", "error", err)
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		slog.Warn("feedback.validation_failed", "error", err)
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	_, err := s.db.ExecContext(reqCtx, `
		INSERT INTO user_feedback (email, most_loved_feature, most_hated_feature, nice_have_feature, created_at)
		VALUES ($1, $2, $3, $4, NOW())
	`, req.Email, req.MostLovedFeature, req.MostHatedFeature, req.NiceHaveFeature)

	if err != nil {
		slog.Error("feedback.recording.failed", "error", err)
		utils.SendErrorResponse(w, "Failed Recording Feedback", http.StatusFailedDependency, nil)
		return
	}

	// Send Feedback Mail
	go s.notificationSvc.SendFeedbackReceivedEmail(req.Email)

	utils.SendSuccessResponse(w, "Feedback Taken Successfully", nil, http.StatusOK)
}
