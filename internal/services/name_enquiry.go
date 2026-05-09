package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/constants"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
	"github.com/spf13/viper"
)

const ACMT023ErrorMsg = "ACMT.023 Name Enquiry Failed: %w"

// NameEnquiryResult is the normalized output of any name enquiry call.
type NameEnquiryResult struct {
	AccountName          string
	AccountNumber        string
	BankCode             string
	BankName             string
	NameEnquirySessionId string
}

// ACMT024Notifier is implemented by ISO20022NameEnquiry.
// The callback handler uses it to unblock waiting EnquireName calls.
type ACMT024Notifier interface {
	NotifyAcmt024(msgID string, resp *models.IdentificationVerificationResponse, err error)
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

type acmt024Result struct {
	resp *models.IdentificationVerificationResponse
	err  error
}

type ISO20022NameEnquiry struct {
	ACMTTimeout time.Duration
	Redis       *redis.Client
	ISO20022    *ISO20022Service
	pending     sync.Map // msgID -> chan acmt024Result
}

func newNIBSSISO20022NameEnquiry(redis *redis.Client) NameEnquiryService {
	ISO20022Service := NewISO20022Service(redis)

	return &ISO20022NameEnquiry{
		ISO20022:    ISO20022Service,
		Redis:       redis,
		ACMTTimeout: utils.GetTimeout("nibss.acmt_timeout", 20),
	}
}

// NotifyAcmt024 is called by the callback handler when NIBSS delivers an acmt.024.
// It unblocks the EnquireName call that is waiting on msgID.
func (s *ISO20022NameEnquiry) NotifyAcmt024(msgID string, resp *models.IdentificationVerificationResponse, err error) {
	if ch, ok := s.pending.LoadAndDelete(msgID); ok {
		ch.(chan acmt024Result) <- acmt024Result{resp: resp, err: err}
	}
}

func (s *ISO20022NameEnquiry) EnquireName(ctx context.Context, accountNumber, bankCode string) (*NameEnquiryResult, error) {
	slog.Info("Initiating ISO20022 ACMT.023 Name Enquiry", "account", accountNumber, "bank_code", bankCode)
	senderName := viper.GetString("nibss.institution_name")
	senderBankCode := viper.GetString("nibss.institution_bank_code")
	if senderBankCode == "" {
		senderBankCode = viper.GetString("nip.bank_code")
	}

	ACMT_023, err := s.ISO20022.CreateAcmt023(accountNumber, bankCode, senderName, senderBankCode)
	if err != nil {
		return nil, fmt.Errorf(ACMT023ErrorMsg, err)
	}

	msgID := string(ACMT_023.Assgnmt.MsgId)
	resultCh := make(chan acmt024Result, 1)
	s.pending.Store(msgID, resultCh)
	defer s.pending.Delete(msgID) // clean up if we time out before callback arrives

	slog.Debug("ACMT.023 Request Created", "account", accountNumber, "bank_code", bankCode, "request", ACMT_023)

	xmlData, err := s.ISO20022.ConvertAcmt023ToXML(ACMT_023)
	if err != nil {
		return nil, fmt.Errorf(ACMT023ErrorMsg, err)
	}

	slog.Debug("ACMT.023 Request Converted to XML", "account", accountNumber, "bank_code", bankCode, "xml", xmlData)

	// Send ACMT.023 — NIBSS returns 202 Accepted; the real answer comes via callback.
	if err := s.sendAcmt023(ctx, []byte(xmlData)); err != nil {
		return nil, fmt.Errorf(ACMT023ErrorMsg, err)
	}

	// Block until the acmt.024 callback arrives or we time out.
	timerCtx, cancel := context.WithTimeout(ctx, s.ACMTTimeout)
	defer cancel()

	var idResp *models.IdentificationVerificationResponse
	select {
	case res := <-resultCh:
		if res.err != nil {
			return nil, fmt.Errorf(ACMT023ErrorMsg, res.err)
		}
		idResp = res.resp
	case <-timerCtx.Done():
		return nil, fmt.Errorf(ACMT023ErrorMsg, fmt.Errorf("timed out waiting for acmt.024 callback"))
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
		return nil, fmt.Errorf(ACMT023ErrorMsg, fmt.Errorf("account not verified"))
	}

	resolvedAccount := accountNumber
	if idResp.AccountNumber != "" {
		resolvedAccount = idResp.AccountNumber
	}
	return &NameEnquiryResult{
		AccountName:   idResp.AccountName,
		AccountNumber: resolvedAccount,
		BankCode:      bankCode,
	}, nil
}

// sendAcmt023 fires the ACMT.023 request and expects a 200/202 acceptance from NIBSS.
// The actual result arrives later via the acmt.024 callback.
func (s *ISO20022NameEnquiry) sendAcmt023(ctx context.Context, xmlData []byte) error {
	_, err := s.ISO20022.ISO20022Breaker.Execute(func() (any, error) {
		// Use a short independent timeout just for the HTTP round-trip.
		// The parent ctx must NOT be used here — it carries the full ACMT wait
		// timeout and would cancel the connection while we are still waiting for
		// the acmt.024 callback, causing "socket hang up".
		httpCtx, cancel := context.WithTimeout(context.Background(), utils.GetTimeout("nibss.acmt_send_timeout", 10))
		defer cancel()

		req, err := http.NewRequestWithContext(httpCtx, "POST", s.ISO20022.GetACMTBaseURL(), bytes.NewBuffer(xmlData))
		if err != nil {
			return nil, fmt.Errorf("failed to create ACMT.023 request: %w", err)
		}
		req.Header.Set(constants.ContentType, "text/xml; charset=utf-8")
		req.Header.Set("Accept", "text/xml")
		if apiKey := viper.GetString("nibss.api_key"); apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := s.ISO20022.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("ACMT.023 request failed: %w", err)
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read ACMT.023 response body: %w", err)
		}
		slog.Debug("ACMT.023 response", "status", resp.StatusCode, "body", string(respBody))

		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("NIBSS ACMT.023 API returned status %d: %s", resp.StatusCode, string(respBody))
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("NIBSS ACMT.023 API returned status %d: %s", resp.StatusCode, string(respBody))
		}
		slog.Debug("ACMT.023 accepted", "status", resp.StatusCode)
		return nil, nil
	})
	return err
}
