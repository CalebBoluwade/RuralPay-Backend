package services

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
)

// MandateAdviceResult is the normalised output of mandate advice creation.
type MandateAdviceResult struct {
	AccountName string
	BankName    string
	MandateCode string
	SessionID   string
}

// MandateAdviceService abstracts mandate creation for linked accounts.
type MandateAdviceService interface {
	// CreateMandate creates a mandate for an account to enable balance enquiry and fund transfers.
	CreateMandate(ctx context.Context, req *models.CreateMandateRequest) (*MandateAdviceResult, error)
}

func NewMandateAdviceService(useNIPSwitch bool, redis *redis.Client) MandateAdviceService {
	return &nipMandateAdvice{nip: NewNIBSSNIPService(), redis: redis}
}

// --- NIP implementation (internal) ---

type nipMandateAdvice struct {
	nip   *NIBSSNIPService
	redis *redis.Client
}

func (s *nipMandateAdvice) CreateMandate(ctx context.Context, req *models.CreateMandateRequest) (*MandateAdviceResult, error) {
	resp, err := s.nip.ExecuteMandateAdvice(ctx, &models.MandateAdviceRequest{
		SessionID:                         utils.GenerateNIPSessionId(s.nip.GetNIPBankCode()),
		DestinationInstitutionCode:        req.DebitBankCode,
		ChannelCode:                       "1",
		MandateReferenceNumber:            utils.GenerateMandateRef(s.nip.GetNIPPaymentPrefix()),
		Amount:                            "100",
		DebitAccountName:                  req.DebitAccountName,
		DebitAccountNumber:                req.DebitAccountNumber,
		DebitBankVerificationNumber:       req.DebitBankVerificationNumber,
		DebitKYCLevel:                     req.DebitKycLevel,
		BeneficiaryAccountName:            req.BeneficiaryAccountName,
		BeneficiaryAccountNumber:          req.BeneficiaryAccountNumber,
		BeneficiaryKYCLevel:               req.BeneficiaryKycLevel,
		BeneficiaryBankVerificationNumber: req.BeneficiaryBankVerificationNumber,
	})
	if err != nil {
		return nil, fmt.Errorf("NIP Mandate Advice Failed: %w", err)
	}

	// if err := svc.checkResponseCode(resp.ResponseCode, "mandate advice"); err != nil {
	// 	slog.Error("nip.mandate_advice.response_code_check_failed", "sessionId", resp.SessionID, "response_code", resp.ResponseCode, "error", err)
	// 	return nil, err
	// }

	if resp.ResponseCode != "00" {
		slog.Error("NIP.MandateAdvice.Failed", "account", req.DebitAccountNumber, "bank_code", req.DebitBankCode, "response_code", resp.ResponseCode)
		return nil, utils.NewNIPError(utils.NIPResponseCode(resp.ResponseCode))
	}

	return &MandateAdviceResult{
		AccountName: resp.DebitAccountName,
		BankName:    req.DebitBankName,
		MandateCode: resp.MandateReferenceNumber,
		SessionID:   resp.SessionID,
	}, nil
}
