package utils

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// GetTimeout Load Timeout Configurations with sensible defaults (in seconds)
func GetTimeout(key string, defaultSecs int) time.Duration {
	val := viper.GetDuration(key)
	if val == 0 {
		val = time.Duration(viper.GetInt(key)) * time.Second
	}
	if val == 0 {
		val = time.Duration(defaultSecs) * time.Second
	}
	return val
}

func RealIP(r *http.Request) string {
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return strings.SplitN(ip, ",", 2)[0]
	}
	if ip := r.Header.Get("X-Real-Ip"); ip != "" {
		return ip
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

func ExtractUserMerchantInfoFromContext(w http.ResponseWriter, ctx context.Context) (int, int) {
	userIDStr, ok := ctx.Value("userID").(string)
	if !ok || userIDStr == "" {
		slog.Error("Unauthorized: User ID Not Found in Context")
		SendErrorResponse(w, UnauthorizedError, http.StatusUnauthorized, nil)
	}

	userID, _ := strconv.Atoi(userIDStr)

	merchantIDStr, _ := ctx.Value("merchantID").(string)
	merchantID, _ := strconv.Atoi(merchantIDStr)

	return userID, merchantID
}

func ExtractIsAdminFromContext(w http.ResponseWriter, ctx context.Context) (bool, error) {
	isAdmin, ok := ctx.Value("isAdmin").(bool)
	if !ok || isAdmin == false {
		slog.Error("Unauthorized: isAdmin Not Found in Context", ctx.Value("isAdmin"))
		return false, fmt.Errorf("isAdmin not found in context")
	}

	// isAdmin, err := strconv.ParseBool(isAdminStr)
	// if err != nil {
	// 	slog.Error("Failed to parse isAdmin from context", "value", isAdminStr, "error", err)
	// 	return false, fmt.Errorf("failed to parse isAdmin: %w", err)
	// }

	return isAdmin, nil
}

func FormatTime(t time.Time) string {
	duration := time.Since(t)
	if duration.Minutes() < 60 {
		return strconv.FormatFloat(duration.Minutes(), 'f', 0, 64) + " min ago"
	} else if duration.Hours() < 24 {
		return strconv.FormatFloat(duration.Hours(), 'f', 0, 64) + " hr ago"
	}
	return t.Format("Jan 2, 2006")
}

func GenerateOTP() string {
	b := make([]byte, 4)
	_, _ = cryptorand.Read(b)
	return fmt.Sprintf("%08d", (int(b[0])<<24|int(b[1])<<16|int(b[2])<<8|int(b[3]))%100000000)
}

const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func GenerateImageIdentityToken() string {
	length := 32
	token := make([]byte, length)

	for i := 0; i < length; i++ {
		n, _ := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(len(charset))))
		token[i] = charset[n.Int64()]
	}

	return string(token)
}

// GenerateNIPSessionId Generates a Unique NIP SessionID: "{nipBankCode}{yyMMddHHmmss}{12RandomDigits}"
func GenerateNIPSessionId(nipBankCode string) string {
	return nipBankCode + time.Now().Format("060102150405") + generateRandomNumericString(12)
}

// GenerateMandateRef generates a mandate reference: "{prefix}/{yyMMddHHmmss}/{8RandomDigits}"
func GenerateMandateRef(prefix string) string {
	if prefix == "" {
		prefix = "RYLPAY"
	}
	return prefix + "/" + time.Now().Format("060102150405") + "/" + generateRandomNumericString(8)
}

func generateRandomNumericString(size int) string {
	const digits = "0123456789"
	b := make([]byte, size)
	for i := range b {
		n, _ := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(len(digits))))
		b[i] = digits[n.Int64()]
	}
	return string(b)
}
