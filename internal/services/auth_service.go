package services

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/go-redis/redis/v8"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
	"golang.org/x/crypto/argon2"
)

type AuthService struct {
	db        *sql.DB
	redis     *redis.Client
	validator *validator.Validate
}

// LoginRequest represents the login request payload
// @Description Login request structure
type LoginRequest struct {
	Identifier string `json:"identifier" validate:"required" example:"+2348012345678"` // User phone number, email, or username
	Password   string `json:"password" validate:"required,min=6" example:"password123"`  // User password
}

// RegisterRequest represents the registration request payload
// @Description Registration request structure
type RegisterRequest struct {
	Email       string `json:"Email" validate:"required,email" example:"user@example.com"` // User email address
	Username    string `json:"Username" validate:"required,min=3" example:"johndoe"`       // Username
	Password    string `json:"Password" validate:"required,min=6" example:"password123"`   // User password
	FirstName   string `json:"FirstName" validate:"required,min=2" example:"John"`         // User first name
	LastName    string `json:"LastName" validate:"required,min=2" example:"Doe"`           // User last name
	BVN         string `json:"BVN" validate:"required,len=11" example:"12345678901"`       // Bank Verification Number
	PhoneNumber string `json:"PhoneNumber" validate:"required" example:"+2348012345678"`   // Phone number
}

// AuthResponse represents the authentication response
// @Description Authentication response structure
type AuthResponse struct {
	Token string `json:"token" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."` // JWT token
	User  User   `json:"user"`                                                    // User information
}

// User represents user information
// @Description User structure
type User struct {
	ID          int    `json:"id" example:"1"`                       // User ID
	Email       string `json:"email" example:"user@example.com"`     // User email
	FirstName   string `json:"FirstName" example:"John"`             // User first name
	LastName    string `json:"LastName" example:"Doe"`               // User last name
	AccountId   string `json:"AccountId" example:"1234567890"`       // User account ID`
	PhoneNumber string `json:"PhoneNumber" example:"+2348012345678"` // User phone number
	BVN         string `json:"BVN" example:"12345678901"`            // User BVN
	DeviceID    string `json:"device_id"`
}

func NewAuthService(db *sql.DB, redisClient *redis.Client) *AuthService {
	return &AuthService{
		db:        db,
		redis:     redisClient,
		validator: validator.New(),
	}
}

func (s *AuthService) sendErrorResponse(w http.ResponseWriter, message string, statusCode int, validationErr error) {
	SendErrorResponse(w, message, statusCode, validationErr)
}

// Register handles user registration
// @Summary Register a new user
// @Description Register a new user with email, password, and name
// @Tags auth
// @Accept json
// @Produce json
// @Param request body RegisterRequest true "Registration request"
// @Success 200 {object} AuthResponse "Registration successful"
// @Failure 400 {string} string "Invalid request"
// @Failure 409 {string} string "Email already exists"
// @Failure 500 {string} string "Internal server error"
// @Router /auth/register [post]
func (s *AuthService) Register(w http.ResponseWriter, r *http.Request) {
	log.Printf("[AUTH] Registration attempt from IP: %s", r.RemoteAddr)

	maxBytes := 1_048_576 // 1 MB
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxBytes))

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req RegisterRequest
	if err := dec.Decode(&req); err != nil {
		log.Printf("[AUTH] Registration failed - invalid request: %v", err)
		s.sendErrorResponse(w, "Invalid request", http.StatusBadRequest, nil)
		return
	}

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		log.Printf("[AUTH] Multiple JSON objects detected")
		s.sendErrorResponse(w, "Request body must only contain a single JSON object", http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		log.Printf("[AUTH] Registration validation failed: %v", err)
		s.sendErrorResponse(w, "Validation failed", http.StatusBadRequest, err)
		return
	}

	log.Printf("[AUTH] Registration request for email: %s", req.Email)

	hashedPassword, err := hashPassword(req.Password)
	if err != nil {
		log.Printf("[AUTH] Password hashing failed for %s: %v", req.Email, err)
		s.sendErrorResponse(w, "An Internal Error Occurred", http.StatusInternalServerError, nil)
		return
	}

	// Generate 10-digit account ID
	accountID := generateAccountID()
	accountName := fmt.Sprintf("%s %s", req.FirstName, req.LastName)

	// Start transaction
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("[AUTH] Transaction start failed for %s: %v", req.Email, err)
		s.sendErrorResponse(w, "Failed to create user", http.StatusInternalServerError, nil)
		return
	}
	defer tx.Rollback()

	// Insert user, account, and limits in a single CTE query
	var userID int
	err = tx.QueryRow(`
		WITH new_user AS (
			INSERT INTO users (email, username, password, first_name, last_name, account_id, bvn, phone_number)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING id
		),
		new_account AS (
			INSERT INTO accounts (account_name, account_id, balance, version, user_id, updated_at)
			SELECT $9, $6, 0, 1, id, NOW() FROM new_user
			RETURNING user_id
		),
		new_limits AS (
			INSERT INTO user_limits (user_id, daily_limit, single_transaction_limit, updated_at)
			SELECT id, $10, $11, NOW() FROM new_user
			RETURNING user_id
		)
		SELECT id FROM new_user
	`, strings.ToLower(req.Email), req.Username, hashedPassword, req.FirstName, req.LastName, accountID, req.BVN, req.PhoneNumber, accountName, viper.GetInt64("user.default_daily_limit"), viper.GetInt64("user.default_single_tx_limit")).Scan(&userID)
	if err != nil {
		log.Printf("[AUTH] User creation failed for %s: %v", req.Email, err)
		s.sendErrorResponse(w, "Email or Username Already Exists", http.StatusConflict, nil)
		return
	}

	// Commit transaction
	if err = tx.Commit(); err != nil {
		log.Printf("[AUTH] Transaction commit failed for %s: %v", req.Email, err)
		s.sendErrorResponse(w, "Failed to create user", http.StatusInternalServerError, nil)
		return
	}

	log.Printf("[AUTH] User created successfully - ID: %d, Email: %s", userID, req.Email)

	token, err := generateJWT(userID, nil, nil)
	if err != nil {
		log.Printf("[AUTH] JWT generation failed for user %d: %v", userID, err)
		s.sendErrorResponse(w, "Failed to generate token", http.StatusInternalServerError, nil)
		return
	}

	response := AuthResponse{
		Token: token,
		User:  User{ID: userID, Email: req.Email, FirstName: req.FirstName, LastName: req.LastName, AccountId: accountID},
	}

	log.Printf("[AUTH] Registration successful for user %d", userID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// Login handles user authentication
// @Summary Login user
// @Description Authenticate user with email and password
// @Tags auth
// @Accept json
// @Produce json
// @Param request body LoginRequest true "Login request"
// @Success 200 {object} AuthResponse "Login successful"
// @Failure 400 {string} string "Invalid request"
// @Failure 401 {string} string "Invalid credentials"
// @Failure 500 {string} string "Internal server error"
// @Router /auth/login [post]
func (s *AuthService) Login(w http.ResponseWriter, r *http.Request) {
	log.Printf("[AUTH] Login attempt from IP: %s", r.RemoteAddr)

	maxBytes := 1_048_576 // 1 MB
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxBytes))

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req LoginRequest
	if err := dec.Decode(&req); err != nil {
		log.Printf("[AUTH] Login failed - invalid request: %v", err)
		s.sendErrorResponse(w, "Invalid request", http.StatusBadRequest, nil)
		return
	}

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		log.Printf("[AUTH] Multiple JSON objects detected")
		s.sendErrorResponse(w, "Request body must only contain a single JSON object", http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		log.Printf("[AUTH] Login validation failed: %v", err)
		s.sendErrorResponse(w, "Validation failed", http.StatusBadRequest, err)
		return
	}

	log.Printf("[AUTH] Login request for identifier: %s", req.Identifier)

	var user User
	var hashedPassword string
	err := s.db.QueryRow(`SELECT id, email, first_name, last_name, password, account_id 
		FROM users 
		WHERE phone_number = $1 OR LOWER(email) = LOWER($1) OR username = $1`,
		req.Identifier).Scan(&user.ID, &user.Email, &user.FirstName, &user.LastName, &hashedPassword, &user.AccountId)
	if err != nil {
		log.Printf("[AUTH] User not found for identifier: %s", req.Identifier)
		s.sendErrorResponse(w, "Invalid credentials", http.StatusUnauthorized, nil)
		return
	}

	if !verifyPassword(req.Password, hashedPassword) {
		log.Printf("[AUTH] Invalid password for identifier: %s", req.Identifier)
		s.sendErrorResponse(w, "Invalid credentials", http.StatusUnauthorized, nil)
		return
	}

	log.Printf("[AUTH] Password verified for user ID: %d", user.ID)

	var merchantID *int
	var merchantStatus *string
	var mID int
	var mStatus string
	err = s.db.QueryRow("SELECT id, status FROM merchants WHERE user_id = $1", user.ID).Scan(&mID, &mStatus)
	if err == nil {
		merchantID = &mID
		merchantStatus = &mStatus
		log.Printf("[AUTH] Merchant found for user %d - Merchant ID: %d, Status: %s", user.ID, mID, mStatus)
	}

	token, err := generateJWT(user.ID, merchantID, merchantStatus)
	if err != nil {
		log.Printf("[AUTH] JWT generation failed for user %d: %v", user.ID, err)
		s.sendErrorResponse(w, "Failed to generate token", http.StatusInternalServerError, nil)
		return
	}

	response := AuthResponse{
		Token: token,
		User:  user,
	}

	log.Printf("[AUTH] Login successful for user %d", user.ID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
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
			key := fmt.Sprintf("blacklist:%s", token)
			// Blacklist token until its expiration
			expiry := time.Duration(viper.GetInt("jwt.expiry_hours")) * time.Hour
			if err := s.redis.Set(ctx, key, "1", expiry).Err(); err != nil {
				log.Printf("[AUTH] Failed to blacklist token: %v", err)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "Logout successful"})
}

// VerifyOTP verifies the OTP for BVN validation
// @Summary Verify OTP
// @Description Verify OTP sent for BVN validation
// @Tags accounts
// @Accept json
// @Produce json
// @Param request body map[string]string true "OTP verification request"
// @Success 200 {object} map[string]interface{} "OTP verified successfully"
// @Failure 400 {string} string "Invalid request"
// @Failure 401 {string} string "Invalid or expired OTP"
// @Router /accounts/verify-otp [post]
func (s *AuthService) VerifyOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BVN string `json:"bvn" validate:"required,len=11"`
		OTP string `json:"otp" validate:"required,len=8"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.sendErrorResponse(w, "Invalid request", http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		s.sendErrorResponse(w, "Validation failed", http.StatusBadRequest, err)
		return
	}

	key := fmt.Sprintf("bvn_otp:%s", req.BVN)

	if s.redis != nil {
		ctx := context.Background()
		storedOTP, err := s.redis.Get(ctx, key).Result()
		if err != nil {
			log.Printf("[AUTH] OTP not found or expired for BVN %s", req.BVN)
			s.sendErrorResponse(w, "Invalid or expired OTP", http.StatusUnauthorized, nil)
			return
		}

		if storedOTP != req.OTP {
			log.Printf("[AUTH] Invalid OTP for BVN %s", req.BVN)
			s.sendErrorResponse(w, "Invalid or expired OTP", http.StatusUnauthorized, nil)
			return
		}

		s.redis.Del(ctx, key)
	}

	log.Printf("[AUTH] OTP verified successfully for BVN %s", req.BVN)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"message": "OTP Verified Successfully",
		"valid":   true,
	})
}

// GetUserAccount retrieves user account details from auth token
// @Summary Get user account details
// @Description Get authenticated user's account information
// @Tags auth
// @Produce json
// @Success 200 {object} User "User account details"
// @Failure 401 {string} string "Unauthorized"
// @Failure 500 {string} string "Internal server error"
// @Router /auth/account [get]
func (s *AuthService) GetUserAccount(w http.ResponseWriter, r *http.Request) {
	log.Printf("[AUTH] User account request from IP: %s", r.RemoteAddr)

	userID := r.Context().Value("userID")
	if userID == nil {
		log.Printf("[AUTH] Unauthorized account request - no user ID in context")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	log.Printf("[AUTH] Fetching account details for user ID: %v", userID)
	var user User
	err := s.db.QueryRow("SELECT users.id, email, first_name, last_name, phone_number, users.account_id FROM users LEFT JOIN accounts ON users.id = accounts.user_id WHERE users.id = $1",
		userID).Scan(&user.ID, &user.Email, &user.FirstName, &user.LastName, &user.PhoneNumber, &user.AccountId)
	if err != nil {
		if err == sql.ErrNoRows {
			log.Printf("[AUTH] User not found for ID: %v", userID)
			http.Error(w, "User not found", http.StatusNotFound)
		} else {
			log.Printf("[AUTH] Failed to fetch user details for ID %v: %v", userID, err)
			http.Error(w, "Failed to fetch user details", http.StatusInternalServerError)
		}
		return
	}

	log.Printf("[AUTH] Successfully fetched account details for user: %s (ID: %d)", user.Email, user.ID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

func generateJWT(userID int, merchantID *int, merchantStatus *string) (string, error) {
	claims := jwt.MapClaims{
		"user_id": userID,
		"nameid":  userID,
		"exp":     time.Now().Add(time.Duration(viper.GetInt("jwt.expiry_hours")) * time.Hour).Unix(),
	}
	if merchantID != nil {
		claims["merchant_id"] = *merchantID
	}
	if merchantStatus != nil {
		claims["merchant_status"] = *merchantStatus
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(viper.GetString("jwt.secret_key")))
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

func generateAccountID() string {
	const digits = "0123456789"
	b := make([]byte, 10)
	for i := range b {
		b[i] = digits[rand.Intn(len(digits))]
	}
	return string(b)
}

func generateOTP() string {
	b := make([]byte, 4)
	cryptorand.Read(b)
	return fmt.Sprintf("%08d", (int(b[0])<<24|int(b[1])<<16|int(b[2])<<8|int(b[3]))%100000000)
}

func generateResetToken() (string, error) {
	b := make([]byte, 32)
	if _, err := cryptorand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// ForgotPassword handles password reset request
// @Summary Request password reset
// @Description Send password reset token to user's email
// @Tags auth
// @Accept json
// @Produce json
// @Param request body map[string]string true "Email address"
// @Success 200 {object} map[string]string "Reset token sent"
// @Failure 400 {string} string "Invalid request"
// @Failure 404 {string} string "User not found"
// @Router /auth/forgot-password [post]
func (s *AuthService) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email" validate:"required,email"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.sendErrorResponse(w, "Invalid request", http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		s.sendErrorResponse(w, "Validation failed", http.StatusBadRequest, err)
		return
	}

	var userID int
	err := s.db.QueryRow("SELECT id FROM users WHERE email = $1", strings.ToLower(req.Email)).Scan(&userID)
	if err != nil {
		log.Printf("[AUTH] User not found for email: %s", req.Email)
		s.sendErrorResponse(w, "User not found", http.StatusNotFound, nil)
		return
	}

	token, err := generateResetToken()
	if err != nil {
		log.Printf("[AUTH] Failed to generate reset token: %v", err)
		s.sendErrorResponse(w, "Failed to generate reset token", http.StatusInternalServerError, nil)
		return
	}

	expiresAt := time.Now().Add(1 * time.Hour)
	_, err = s.db.Exec("INSERT INTO password_reset_tokens (user_id, token, expires_at) VALUES ($1, $2, $3)",
		userID, token, expiresAt)
	if err != nil {
		log.Printf("[AUTH] Failed to store reset token: %v", err)
		s.sendErrorResponse(w, "Failed to process request", http.StatusInternalServerError, nil)
		return
	}

	log.Printf("[AUTH] Password reset token generated for user %d: %s", userID, token)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Password reset token sent",
		"token":   token,
	})
}

// ResetPassword handles password reset with token
// @Summary Reset password
// @Description Reset user password using reset token
// @Tags auth
// @Accept json
// @Produce json
// @Param request body map[string]string true "Reset token and new password"
// @Success 200 {object} map[string]string "Password reset successful"
// @Failure 400 {string} string "Invalid request"
// @Failure 401 {string} string "Invalid or expired token"
// @Router /auth/reset-password [post]
func (s *AuthService) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token" validate:"required"`
		Password string `json:"password" validate:"required,min=6"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.sendErrorResponse(w, "Invalid request", http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		s.sendErrorResponse(w, "Validation failed", http.StatusBadRequest, err)
		return
	}

	var userID int
	var expiresAt time.Time
	var used bool
	err := s.db.QueryRow("SELECT user_id, expires_at, used FROM password_reset_tokens WHERE token = $1",
		req.Token).Scan(&userID, &expiresAt, &used)
	if err != nil {
		log.Printf("[AUTH] Invalid reset token: %s", req.Token)
		s.sendErrorResponse(w, "Invalid or expired token", http.StatusUnauthorized, nil)
		return
	}

	if used || time.Now().After(expiresAt) {
		log.Printf("[AUTH] Reset token expired or already used: %s", req.Token)
		s.sendErrorResponse(w, "Invalid or expired token", http.StatusUnauthorized, nil)
		return
	}

	hashedPassword, err := hashPassword(req.Password)
	if err != nil {
		log.Printf("[AUTH] Password hashing failed: %v", err)
		s.sendErrorResponse(w, "Failed to reset password", http.StatusInternalServerError, nil)
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("[AUTH] Transaction start failed: %v", err)
		s.sendErrorResponse(w, "Failed to reset password", http.StatusInternalServerError, nil)
		return
	}
	defer tx.Rollback()

	_, err = tx.Exec("UPDATE users SET password = $1 WHERE id = $2", hashedPassword, userID)
	if err != nil {
		log.Printf("[AUTH] Failed to update password: %v", err)
		s.sendErrorResponse(w, "Failed to reset password", http.StatusInternalServerError, nil)
		return
	}

	_, err = tx.Exec("UPDATE password_reset_tokens SET used = TRUE WHERE token = $1", req.Token)
	if err != nil {
		log.Printf("[AUTH] Failed to mark token as used: %v", err)
		s.sendErrorResponse(w, "Failed to reset password", http.StatusInternalServerError, nil)
		return
	}

	if err = tx.Commit(); err != nil {
		log.Printf("[AUTH] Transaction commit failed: %v", err)
		s.sendErrorResponse(w, "Failed to reset password", http.StatusInternalServerError, nil)
		return
	}

	log.Printf("[AUTH] Password reset successful for user %d", userID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "Password reset successful"})
}
