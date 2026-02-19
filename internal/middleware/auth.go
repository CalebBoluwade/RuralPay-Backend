package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-redis/redis/v8"
	"github.com/golang-jwt/jwt/v5"
	"github.com/ruralpay/backend/internal/services"
	"github.com/spf13/viper"
)

var redisClient *redis.Client
var authService *services.AuthService

func InitAuthMiddleware(redis *redis.Client, auth *services.AuthService) {
	redisClient = redis
	authService = auth
}

func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get token from Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Authorization Header required", http.StatusUnauthorized)
			return
		}

		// Extract token
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			http.Error(w, "Invalid Authorization", http.StatusUnauthorized)
			return
		}

		token := parts[1]

		// Check if token is blacklisted
		if redisClient != nil {
			ctx := context.Background()
			key := fmt.Sprintf("blacklist:%s", token)
			if exists, _ := redisClient.Exists(ctx, key).Result(); exists > 0 {
				http.Error(w, "Token Revoked", http.StatusUnauthorized)
				return
			}
		}

		// Validate token (implement your JWT validation here)
		userID, merchantID, err := validateToken(token)
		if err != nil {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		// Check user status
		active, err := authService.CheckUserStatus(userID)
		if err != nil {
			http.Error(w, "Internal Service Error", http.StatusInternalServerError)
			return
		}

		if !active {
			http.Error(w, "User is not active", http.StatusUnauthorized)
			return
		}

		// Add user ID to context
		ctx := context.WithValue(r.Context(), "userID", userID)
		if merchantID != nil {
			ctx = context.WithValue(ctx, "merchantID", merchantID)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func validateToken(tokenString string) (string, any, error) {
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("Unexpected Signing Method --> %v", token.Header["alg"])
		}
		return []byte(viper.GetString("jwt.secret_key")), nil
	})

	if err != nil || !token.Valid {
		return "", nil, err
	}

	// Validate claims
	issuer := viper.GetString("jwt.issuer")
	audience := viper.GetString("jwt.audience")

	if issuer != "" {
		if iss, ok := claims["iss"].(string); !ok || iss != issuer {
			return "", nil, fmt.Errorf("invalid issuer")
		}
	}

	if audience != "" {
		if aud, ok := claims["aud"].(string); !ok || aud != audience {
			return "", nil, fmt.Errorf("invalid audience")
		}
	}

	userID := claims["user_id"]
	merchantID := claims["merchant_id"]
	fmt.Printf("[AUTH] Token Validated --> [USER ID]: %v, [Merchant ID]: %v \n", userID, merchantID)
	return fmt.Sprintf("%v", userID), merchantID, nil
}
