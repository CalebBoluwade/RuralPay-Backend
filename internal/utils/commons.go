package utils

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"
)

var SessionKeyPrefix = "SESSION:"
var BlacklistKeyPrefix = "BLACKLIST:"

func ExtractUserMerchantInfoFromContext(w http.ResponseWriter, ctx context.Context) (int, int) {
	userIDStr, ok := ctx.Value("userID").(string)
	if !ok || userIDStr == "" {
		log.Printf("Unauthorized: User ID Not Found in Context")
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
