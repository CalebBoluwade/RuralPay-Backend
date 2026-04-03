package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/golang-jwt/jwt/v5"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/services"
	"github.com/ruralpay/backend/internal/utils"
	"github.com/spf13/viper"
)

var redisClient *redis.Client
var userService *services.UserService
var validator *services.ValidationHelper
var cfg models.SessionConfig

func InitAuthMiddleware(redis *redis.Client, user *services.UserService, config models.SessionConfig) {
	redisClient = redis
	userService = user
	validator = services.NewValidationHelper()
	cfg = config
}

func extractBearerToken(r *http.Request) (string, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", fmt.Errorf("missing authorization header")
	}
	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		return "", fmt.Errorf("invalid authorization header format")
	}
	return parts[1], nil
}

func checkTokenBlacklist(token string) bool {
	if redisClient == nil {
		return false
	}
	key := utils.BlacklistKeyPrefix + token
	exists, _ := redisClient.Exists(context.Background(), key).Result()
	return exists > 0
}

func parseSessionClaims(token string) (sid, deviceID string, err error) {
	claims := jwt.MapClaims{}
	_, err = jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return []byte(viper.GetString("jwt.secret_key")), nil
	})

	if err != nil {
		return "", "", fmt.Errorf("failed to Parse JWToken: %w", err)
	}

	var hasSid, hasDeviceID bool
	sid, hasSid = claims["sid"].(string)
	deviceID, hasDeviceID = claims["did"].(string)
	if !hasSid || !hasDeviceID {
		return "", "", fmt.Errorf("missing Session Claims")
	}
	return sid, deviceID, nil
}

func validateSession(ctx context.Context, sessionKey, deviceID string) error {
	if redisClient == nil {
		return nil
	}
	sessionData, err := redisClient.Get(ctx, sessionKey).Result()
	if err != nil {
		slog.Warn("auth.session.not_found", "session_key", sessionKey, "error", err)
		return utils.ErrSessionNotFound
	}
	var data map[string]string
	if err := json.Unmarshal([]byte(sessionData), &data); err != nil {
		slog.Error("auth.session.unmarshal_failed", "error", err)
		return utils.ErrInvalidSession
	}
	expiresAt, _ := strconv.ParseInt(data["expires_at"], 10, 64)
	if time.Now().Unix() > expiresAt {
		redisClient.Del(ctx, sessionKey)
		return utils.ErrInvalidSession
	}
	if data["device_id"] != deviceID {
		redisClient.Del(ctx, sessionKey)
		return utils.ErrDeviceMismatch
	}
	redisClient.Expire(ctx, sessionKey, cfg.InactivityTTL)
	return nil
}

func AuthSessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := extractBearerToken(r)
		if err != nil {
			validator.SendErrorResponse(w, "Invalid Authorization Header", http.StatusUnauthorized, nil)
			return
		}

		if checkTokenBlacklist(token) {
			validator.SendErrorResponse(w, "Token Revoked", http.StatusUnauthorized, nil)
			return
		}

		userID, merchantID, err := validateToken(token)
		if err != nil {
			validator.SendErrorResponse(w, "Invalid User Token", http.StatusUnauthorized, nil)
			return
		}

		sid, deviceID, err := parseSessionClaims(token)
		if err != nil {
			validator.SendErrorResponse(w, "Invalid Token: Missing Session Claims", http.StatusUnauthorized, nil)
			return
		}

		ctx := r.Context()
		if err := validateSession(ctx, utils.SessionKeyPrefix+sid, deviceID); err != nil {
			if errors.Is(err, utils.ErrSessionNotFound) || errors.Is(err, utils.ErrInvalidSession) {
				validator.SendErrorResponse(w, "Session Locked - User Presence Required", http.StatusLocked, nil)
				return
			}

			validator.SendErrorResponse(w, "Invalid Security Token", http.StatusUnauthorized, nil)
			return
		}

		active, err := userService.CheckUserStatus(userID)
		if err != nil {
			validator.SendErrorResponse(w, "Invalid User Status", http.StatusUnauthorized, nil)
			return
		}
		if !active {
			validator.SendErrorResponse(w, "User Is Not Active", http.StatusUnauthorized, nil)
			return
		}

		ctx = context.WithValue(ctx, "userID", userID)
		if merchantID != nil {
			ctx = context.WithValue(ctx, "merchantID", fmt.Sprintf("%v", merchantID))
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func CreateSession(ctx context.Context, rdb *redis.Client, sessionID string, s models.Session) error {
	data, _ := json.Marshal(s)

	return rdb.Set(ctx,
		utils.SessionKeyPrefix+sessionID,
		data,
		cfg.InactivityTTL, // inactivity TTL
	).Err()
}

func RotateRefreshToken(
	ctx context.Context,
	rdb *redis.Client,
	sid string,
	newRefreshHash string,
	cfg models.SessionConfig,
) error {

	key := utils.SessionKeyPrefix + sid

	pipe := rdb.TxPipeline()

	pipe.HSet(ctx, key, "refresh_hash", newRefreshHash)
	pipe.Expire(ctx, key, cfg.InactivityTTL)

	_, err := pipe.Exec(ctx)
	return err
}

func validateToken(tokenString string) (string, any, error) {
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected Signing Method --> %v", token.Header["alg"])
		}
		return []byte(viper.GetString("jwt.secret_key")), nil
	})

	if err != nil || !token.Valid {
		slog.Warn("auth.token.invalid", "error", err)
		return "", nil, err
	}

	issuer := viper.GetString("jwt.issuer")
	audience := viper.GetString("jwt.audience")

	if issuer != "" {
		if iss, ok := claims["iss"].(string); !ok || iss != issuer {
			return "", nil, fmt.Errorf("Invalid issuer")
		}
	}

	if audience != "" {
		if aud, ok := claims["aud"].(string); !ok || aud != audience {
			return "", nil, fmt.Errorf("Invalid Audience")
		}
	}

	return fmt.Sprintf("%v", claims["user_id"]), claims["merchant_id"], nil
}
