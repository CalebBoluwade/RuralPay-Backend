package utils

import (
	"context"
	"log"
	"net/http"
	"strconv"
)

var SessionKeyPrefix = "SESSION:"

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
