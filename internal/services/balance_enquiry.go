package services

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
)

// BalanceEnquiryResult is the normalised output of any balance enquiry call.
type BalanceEnquiryResult struct {
	AccountNumber    string
	AccountName      string
	BankCode         string
	AvailableBalance string
	SessionID        string
}

// BalanceEnquiryService abstracts account balance lookup.
type BalanceEnquiryService interface {
	// GetBalance fetches the live balance for an external account.
	// accountName and bvn come from the DB (stored at Link-Account time).
	// mandateCode is the NE session ID saved at link time, used as AuthorizationCode.
	GetBalance(ctx context.Context, accountNumber, accountName, bankCode, bvn, mandateCode string) (*BalanceEnquiryResult, error)
}

func NewBalanceEnquiryService(useNIPSwitch bool, redis *redis.Client) BalanceEnquiryService {
	return &nipBalanceEnquiry{nip: NewNIBSSNIPService(), redis: redis}
}

// --- NIP implementation (internal) ---

type nipBalanceEnquiry struct {
	nip   *NIBSSNIPService
	redis *redis.Client
}

func (s *nipBalanceEnquiry) GetBalance(ctx context.Context, accountNumber, accountName, bankCode, bvn, mandateCode string) (*BalanceEnquiryResult, error) {
	resp, err := s.nip.ExecuteBalanceEnquiry(ctx, &models.BalanceEnquiryRequest{
		SessionID:                    utils.GenerateNIPSessionId(s.nip.GetNIPBankCode()),
		DestinationInstitutionCode:   bankCode,
		ChannelCode:                  "1",
		AuthorizationCode:            mandateCode,
		TargetAccountNumber:          accountNumber,
		TargetAccountName:            accountName,
		TargetBankVerificationNumber: bvn,
	})
	if err != nil {
		return nil, fmt.Errorf("NIP balance enquiry failed: %w", err)
	}

	if s.nip.checkResponseCode(resp.ResponseCode, "balance enquiry") != nil {
		slog.Error("nip.balance_enquiry.failed", "account", accountNumber, "available_balance", resp.AvailableBalance, "bank_code", bankCode, "response_code", resp.ResponseCode)
		return nil, utils.NewNIPError(utils.NIPResponseCode(resp.ResponseCode))
	}

	// Default to "0" if AvailableBalance is empty
	balance := resp.AvailableBalance
	if balance == "" {
		balance = "0"
	}

	return &BalanceEnquiryResult{
		SessionID:        resp.SessionID,
		AccountNumber:    resp.TargetAccountNumber,
		AccountName:      resp.TargetAccountName,
		BankCode:         bankCode,
		AvailableBalance: balance,
	}, nil
}
