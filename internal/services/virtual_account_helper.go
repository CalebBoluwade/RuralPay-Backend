package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
)

type VirtualAccountData struct {
	AccountNumber string `json:"accountNumber"`
	AccountName   string `json:"accountName"`
	BankName      string `json:"bankName"`
	// BankCode      string `json:"bankCode"`
	MerchantID string `json:"merchantId"`
	Amount     int64  `json:"amount,omitempty"`
	ExpiresAt  int64  `json:"expiresAt"`
}

// ValidateVirtualAccount checks if a virtual account exists and is valid in Redis
func ValidateVirtualAccount(redisClient *redis.Client, accountNumber string) (*VirtualAccountData, error) {
	if redisClient == nil {
		return nil, fmt.Errorf("redis client not available")
	}

	ctx := context.Background()
	key := fmt.Sprintf("va:%s", accountNumber)

	data, err := redisClient.Get(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("virtual account not found or expired")
	}

	var vaData VirtualAccountData
	if err := json.Unmarshal([]byte(data), &vaData); err != nil {
		return nil, fmt.Errorf("invalid virtual account data")
	}

	if time.Now().Unix() > vaData.ExpiresAt {
		return nil, fmt.Errorf("virtual account expired")
	}

	return &vaData, nil
}
