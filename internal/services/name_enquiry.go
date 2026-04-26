package services

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/constants"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
)

const acmt023ErrorMsg = "Acmt023 name enquiry failed: %w"

// NameEnquiryResult is the normalised output of any name enquiry call.
type NameEnquiryResult struct {
	AccountName          string
	AccountNumber        string
	BankCode             string
	BankName             string
	NameEnquirySessionId string
}

// NameEnquiryService abstracts account name lookup.
// Callers never know or care whether NIP or ISO 20022 is used underneath.
type NameEnquiryService interface {
	EnquireName(ctx context.Context, accountNumber, bankCode string) (*NameEnquiryResult, error)
}

// NewNameEnquiryService returns the default NameEnquiryService.
// Swap the returned implementation here without touching any caller.
func NewNameEnquiryService(redis *redis.Client) NameEnquiryService {
	return newNipNameEnquiry(redis)
}

// --- NIP implementation (internal) ---

type NIPNameEnquiry struct {
	Redis      *redis.Client
	NIPService *NIBSSNIPService
}

func newNipNameEnquiry(redis *redis.Client) NameEnquiryService {
	return &NIPNameEnquiry{Redis: redis, NIPService: NewNIBSSNIPService()}
}

func (s *NIPNameEnquiry) EnquireName(ctx context.Context, accountNumber, bankCode string) (*NameEnquiryResult, error) {
	resp, err := s.NIPService.ExecuteNameEnquiry(ctx, &models.NESingleRequest{
		SessionID:                  utils.GenerateNipSessionId(s.NIPService.GetNIPBankCode()),
		DestinationInstitutionCode: bankCode,
		ChannelCode:                "1",
		AccountNumber:              accountNumber,
	})

	if err != nil {
		return nil, fmt.Errorf("NIP name enquiry failed: %w", err)
	}

	if resp.ResponseCode != "00" {
		slog.Error("nip.name_enquiry.failed", "account", accountNumber, "bank_code", bankCode, "response_code", resp.ResponseCode)
		return nil, fmt.Errorf("NIP name enquiry failed: %s - %s", resp.ResponseCode)
	}

	if s.Redis != nil {
		// Cache Name Enquiry Details For The Transaction
		err := s.Redis.HSet(ctx, fmt.Sprintf(constants.UserTransactionMetadataKeyPrefix, resp.SessionID), map[string]interface{}{
			"AccountName":   resp.AccountName,
			"AccountNumber": resp.AccountNumber,
			"BankCode":      resp.DestinationInstitutionCode,
			"BVN":           resp.BankVerificationNumber,
			"KYCLevel":      resp.KYCLevel,
			// "MandateCode": resp.MandateCode,
		}).Err()
		// Add other relevant fields as needed
		if err != nil {
			slog.Error("name_enquiry.redis_cache_failed", "account", accountNumber, "bank_code", bankCode, "error", err)
			// Proceed without caching if Redis fails
		}
	}

	return &NameEnquiryResult{
		NameEnquirySessionId: resp.SessionID,
		AccountName:          resp.AccountName,
		AccountNumber:        resp.AccountNumber,
		BankCode:             bankCode,
	}, nil
}

// --- ISO 20022 implementation (internal) ---

type ISO20022NameEnquiry struct {
	iso   *ISO20022Service
	nibss *NIBSSClient
}

func newISO20022NameEnquiry() NameEnquiryService {
	return &ISO20022NameEnquiry{
		iso: NewISO20022Service(),
		// nibss: NewNIBSSClient(),
	}
}

func (s *ISO20022NameEnquiry) EnquireName(ctx context.Context, accountNumber, bankCode string) (*NameEnquiryResult, error) {
	acmt023, err := s.iso.CreateAcmt023(accountNumber, bankCode)
	if err != nil {
		return nil, fmt.Errorf(acmt023ErrorMsg, err)
	}
	xmlData, err := s.iso.ConvertToXML(acmt023)
	if err != nil {
		return nil, fmt.Errorf(acmt023ErrorMsg, err)
	}
	idResp, err := s.VerifyAccountIdentification(ctx, []byte(xmlData))
	if err != nil {
		return nil, fmt.Errorf(acmt023ErrorMsg, err)
	}
	if !idResp.Verified {
		return nil, fmt.Errorf(acmt023ErrorMsg, fmt.Errorf("account not verified"))
	}
	return &NameEnquiryResult{
		AccountName:   idResp.AccountName,
		AccountNumber: accountNumber,
		BankCode:      bankCode,
		// NameEnquirySessionId: idResp.SessionID, // Not sure if this is returned in ISO 20022 flow, need to check
	}, nil
}

func (s *ISO20022NameEnquiry) VerifyAccountIdentification(ctx context.Context, xmlData []byte) (*models.IdentificationVerificationResponse, error) {
	// Create a child context with ACMT timeout
	opCtx, cancel := context.WithTimeout(ctx, s.nibss.acmtTimeout)
	defer cancel()

	body, err := s.nibss.iso20022Breaker.Execute(func() (interface{}, error) {
		req, err := http.NewRequestWithContext(opCtx, "POST", s.nibss.acmtURL, bytes.NewBuffer(xmlData))
		if err != nil {
			return nil, fmt.Errorf("failed to create acmt.023 request: %w", err)
		}
		req.Header.Set(constants.ContentType, constants.XMLContentType)
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.nibss.apiKey))

		resp, err := s.nibss.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("acmt.023 request failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("NIBSS acmt.023 API returned status %d", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("NIBSS acmt.023 API returned status %d", resp.StatusCode)
		}

		var idResp models.IdentificationVerificationResponse
		if err := xml.NewDecoder(resp.Body).Decode(&idResp); err != nil {
			return nil, fmt.Errorf("failed to decode acmt.024 response: %w", err)
		}
		return &idResp, nil
	})
	if err != nil {
		return nil, err
	}
	return body.(*models.IdentificationVerificationResponse), nil
}
