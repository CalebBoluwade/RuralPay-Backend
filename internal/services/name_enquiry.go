package services

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/constants"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
)

const acmt023ErrorMsg = "Acmt023 Name Enquiry Failed: %w"

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
func NewNameEnquiryService(useNIBSSISOzNIPSwitch bool, redis *redis.Client) NameEnquiryService {
	switch useNIBSSISOzNIPSwitch {
	case true:
		return newNIBSSISO20022NameEnquiry(redis)
	case false:
		return newNIPNameEnquiry(redis)
	default:
		return newNIPNameEnquiry(redis)
	}

}

type NIPNameEnquiry struct {
	Redis      *redis.Client
	NIPService *NIBSSNIPService
}

func newNIPNameEnquiry(redis *redis.Client) NameEnquiryService {
	return &NIPNameEnquiry{Redis: redis, NIPService: NewNIBSSNIPService()}
}

func (s *NIPNameEnquiry) EnquireName(ctx context.Context, accountNumber, bankCode string) (*NameEnquiryResult, error) {
	resp, err := s.NIPService.ExecuteNameEnquiry(ctx, &models.NESingleRequest{
		SessionID:                  utils.GenerateNIPSessionId(s.NIPService.GetNIPBankCode()),
		DestinationInstitutionCode: bankCode,
		ChannelCode:                "1",
		AccountNumber:              accountNumber,
	})
	if err != nil {
		return nil, fmt.Errorf("NIP Name Enquiry Failed For Account -> [%s] And Bank Code -> [%s]: %w", accountNumber, bankCode, err)
	}

	if resp.ResponseCode != "00" {
		slog.Error("NIP.NameEnquiry.Failed", "account", accountNumber, "bank_code", bankCode, "response_code", resp.ResponseCode)
		return nil, utils.NewNIPError(utils.NIPResponseCode(resp.ResponseCode))
	}

	if s.Redis != nil {
		err := s.Redis.HSet(ctx, fmt.Sprintf(constants.NameEnquiryMetadataKeyPrefix, resp.SessionID), map[string]any{
			"AccountName":   resp.AccountName,
			"AccountNumber": resp.AccountNumber,
			"BankCode":      resp.DestinationInstitutionCode,
			"BVN":           resp.BankVerificationNumber,
			"KYCLevel":      resp.KYCLevel,
		}).Err()
		if err != nil {
			slog.Error("NIP.NameEnquiry.RedisCacheFailed", "account", accountNumber, "bank_code", bankCode, "error", err)
		}
	}

	return &NameEnquiryResult{
		NameEnquirySessionId: resp.SessionID,
		AccountName:          resp.AccountName,
		AccountNumber:        resp.AccountNumber,
		BankCode:             bankCode,
	}, nil
}

type ISO20022NameEnquiry struct {
	acmtTimeout time.Duration
	Redis       *redis.Client
	ISO20022    *ISO20022Service
}

func newNIBSSISO20022NameEnquiry(redis *redis.Client) NameEnquiryService {
	ISO20022Service := NewISO20022Service(redis)

	return &ISO20022NameEnquiry{
		ISO20022:    ISO20022Service,
		Redis:       redis,
		acmtTimeout: utils.GetTimeout("nibss.acmt_timeout", 20),
	}
}

func (s *ISO20022NameEnquiry) EnquireName(ctx context.Context, accountNumber, bankCode string) (*NameEnquiryResult, error) {
	slog.Info("Initiating ISO20022 ACMT.023 Name Enquiry", "account", accountNumber, "bank_code", bankCode)
	acmt023, err := s.ISO20022.CreateAcmt023(accountNumber, bankCode)
	if err != nil {
		return nil, fmt.Errorf(acmt023ErrorMsg, err)
	}

	slog.Debug("ACMT.023 Request Created", "account", accountNumber, "bank_code", bankCode, "request", acmt023)

	xmlData, err := s.ISO20022.ConvertToXML(acmt023)
	if err != nil {
		return nil, fmt.Errorf(acmt023ErrorMsg, err)
	}

	slog.Debug("ACMT.023 Request Converted to XML", "account", accountNumber, "bank_code", bankCode, "xml", string(xmlData))

	idResp, err := s.VerifyAccountIdentification(ctx, []byte(xmlData))
	if err != nil {
		return nil, fmt.Errorf(acmt023ErrorMsg, err)
	}

	slog.Info("ISO20022 Name Enquiry Successful", "account", accountNumber, "bank_code", bankCode, "response", idResp)

	// Cache the result in Redis for quick retrieval during payment processing
	if s.Redis != nil {
		err := s.Redis.HSet(ctx, fmt.Sprintf(constants.NameEnquiryMetadataKeyPrefix, idResp.AccountName), map[string]any{
			"AccountName":   idResp.AccountName,
			"AccountNumber": accountNumber,
			"BankCode":      bankCode,
			// "BVN":           idResp.BankVerificationNumber, // Not sure if this is returned in ISO 20022 flow, need to check
			// "KYCLevel":      idResp.KYCLevel, // Not sure if this is returned in ISO 20022 flow, need to check
		}).Err()
		if err != nil {
			slog.Error("ISO20022.NameEnquiry.RedisCacheFailed", "account", accountNumber, "bank_code", bankCode, "error", err)
		}
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
	opCtx, cancel := context.WithTimeout(ctx, s.acmtTimeout)
	defer cancel()

	body, err := s.ISO20022.ISO20022Breaker.Execute(func() (interface{}, error) {
		req, err := http.NewRequestWithContext(opCtx, "POST", s.ISO20022.GetACMTBaseURL(), bytes.NewBuffer(xmlData))
		if err != nil {
			return nil, fmt.Errorf("failed to create acmt.023 request: %w", err)
		}
		req.Header.Set(constants.ContentType, constants.XMLContentType)
		// req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.ISO20022.apiKey))

		resp, err := s.ISO20022.httpClient.Do(req)
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
