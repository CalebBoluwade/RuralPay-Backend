package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/config"
	"github.com/ruralpay/backend/internal/models"
)

type USSDCode struct {
	Code          string              `json:"code"`
	TransactionID string              `json:"transactionId"`
	Type          models.USSDCodeType `json:"txType"`
	UserID        string              `json:"userId"`
	Amount        int64               `json:"amount"`
	CreatedAt     time.Time           `json:"createdAt"`
	ExpiresAt     time.Time           `json:"expiresAt"`
	Expired       bool                `json:"expired"`
	Used          bool                `json:"used"`
	Currency      string              `json:"currency"`
}

type USSDService struct {
	db     *sql.DB
	redis  *redis.Client
	config *config.USSDConfig
}

func NewUSSDService(db *sql.DB, redis *redis.Client) *USSDService {
	return &USSDService{
		db:     db,
		redis:  redis,
		config: config.LoadUSSDConfig(),
	}
}

func (s *USSDService) GeneratePushCode(ctx context.Context, userID string, amount int64) (string, error) {
	return s.generateCode(ctx, userID, amount, models.PushPayment)
}

func (s *USSDService) GeneratePullCode(ctx context.Context, userID string, amount int64) (string, error) {
	return s.generateCode(ctx, userID, amount, models.PullPayment)
}

func (s *USSDService) generateCode(ctx context.Context, userID string, amount int64, codeType models.USSDCodeType) (string, error) {
	if err := s.checkRateLimit(ctx, userID); err != nil {
		slog.Warn("ussd.generate_code.rate_limited", "user_id", userID, "error", err)
		return "", err
	}

	code := s.generateSecureCode()
	hashedCode := s.hashCode(code)
	transactionId := s.generateTransactionID()
	expiresAt := time.Now().Add(s.config.CodeTimeout)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ussd_codes (transaction_id, code_hash, code_type, user_id, amount, expires_at, used)
		VALUES ($1, $2, $3, $4, $5, $6, false)
	`, transactionId, hashedCode, string(codeType), userID, amount, expiresAt)

	if err != nil {
		slog.Error("ussd.generate_code.db_failed", "user_id", userID, "error", err)
		return "", fmt.Errorf("failed to store code: %w", err)
	}

	s.incrementRateLimit(ctx, userID)
	return code, nil
}

func (s *USSDService) ValidateAndConsume(ctx context.Context, code string, expectedType models.USSDCodeType) (*USSDCode, error) {
	hashedCode := s.hashCode(code)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var ussdCode USSDCode
	var used bool
	err = tx.QueryRowContext(ctx, `
		SELECT transaction_id, user_id, amount, expires_at, used, code_type
		FROM ussd_codes
		WHERE code_hash = $1 AND code_type = $2
		FOR UPDATE
	`, hashedCode, string(expectedType)).Scan(&ussdCode.TransactionID, &ussdCode.UserID, &ussdCode.Amount, &ussdCode.ExpiresAt, &used, &ussdCode.Type)

	if err == sql.ErrNoRows {
		return nil, errors.New("invalid code")
	}
	if err != nil {
		return nil, err
	}

	if used {
		return nil, errors.New("code already used")
	}

	if time.Now().After(ussdCode.ExpiresAt) {
		return nil, errors.New("code expired")
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE ussd_codes
		SET used = true, used_at = $1
		WHERE code_hash = $2
	`, time.Now(), hashedCode)

	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	ussdCode.Code = code
	return &ussdCode, nil
}

func (s *USSDService) generateSecureCode() string {
	const charset = "0123456789"
	code := make([]byte, s.config.CodeLength)
	charsetLen := big.NewInt(int64(len(charset)))

	for i := range code {
		n, _ := rand.Int(rand.Reader, charsetLen)
		code[i] = charset[n.Int64()]
	}

	return string(code)
}

func (s *USSDService) generateTransactionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("USSD-%X-%d", b, time.Now().Unix())
}

func (s *USSDService) hashCode(code string) string {
	hash := sha256.Sum256([]byte(code))
	for i := 1; i < s.config.HashIterations; i++ {
		hash = sha256.Sum256(hash[:])
	}
	return hex.EncodeToString(hash[:])
}

func (s *USSDService) checkRateLimit(ctx context.Context, userID string) error {
	key := fmt.Sprintf("ussd:ratelimit:%s", userID)
	count, err := s.redis.Get(ctx, key).Int()
	if err != nil && err != redis.Nil {
		return err
	}

	if count >= s.config.MaxGenerationPerUser {
		return errors.New("rate limit exceeded")
	}

	return nil
}

func (s *USSDService) incrementRateLimit(ctx context.Context, userID string) {
	key := fmt.Sprintf("ussd:ratelimit:%s", userID)
	pipe := s.redis.Pipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, s.config.RateLimitWindow)
	pipe.Exec(ctx)
}

func (s *USSDService) CleanupExpiredCodes(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM ussd_codes
		WHERE expires_at < $1 OR (used = true AND used_at < $2)
	`, time.Now(), time.Now().Add(-24*time.Hour))
	return err
}

func (s *USSDService) GetCodeTimeout() time.Duration {
	return s.config.CodeTimeout
}

func (s *USSDService) FormatDialCode(code string) string {
	return s.config.DialPrefix + code + s.config.DialSuffix
}

func (s *USSDService) GetUserCodes(ctx context.Context, userID string) ([]USSDCode, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT transaction_id, code_type, user_id, amount, expires_at, created_at, used
		FROM ussd_codes
		WHERE user_id = $1
		ORDER BY expires_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var codes []USSDCode
	for rows.Next() {
		var code USSDCode
		var used bool
		if err := rows.Scan(&code.TransactionID, &code.Type, &code.UserID, &code.Amount, &code.ExpiresAt, &code.CreatedAt, &used); err != nil {
			return nil, err
		}

		code.Expired = time.Now().After(code.ExpiresAt) || used
		code.Used = used
		code.Currency = "NGN"
		code.Code = "***" // Masked for security
		codes = append(codes, code)
	}

	return codes, rows.Err()
}
