package utils

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"time"
)

var SessionKeyPrefix = "SESSION:"
var BlacklistKeyPrefix = "BLACKLIST:"

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
