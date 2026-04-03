package services

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/moov-io/iso20022/pkg/pacs_v08"
	"github.com/ruralpay/backend/internal/circuitbreaker"
	"github.com/sony/gobreaker"
	"github.com/spf13/viper"
)

type NIBSSClient struct {
	mandateBaseURL        string
	iso8583BaseURL        string // ISO 8583 card settlement
	bvnBaseURL            string
	pacsURL               string // ISO 20022 pacs (pacs.008, pacs.002, pacs.028)
	acmtURL               string // ISO 20022 acmt (acmt.023, acmt.024)
	painURL               string // ISO 20022 pain
	apiKey                string
	httpClient            *http.Client
	circuitBreaker        *gobreaker.CircuitBreaker
	iso20022Breaker       *gobreaker.CircuitBreaker // ISO 20022 (ACMT/PACS) breaker
	bvnBreaker            *gobreaker.CircuitBreaker
	mandateBreaker        *gobreaker.CircuitBreaker
	defaultTimeout        time.Duration
	bvnTimeout            time.Duration
	mandateTimeout        time.Duration
	pacsTimeout           time.Duration
	acmtTimeout           time.Duration
	cardSettlementTimeout time.Duration
}

type BVNVerifyRequest struct {
	BVN         string `json:"bvn"`
	PhoneNumber string `json:"phoneNumber"`
}

type BVNVerifyResponse struct {
	BVN          string `json:"bvn"`
	FirstName    string `json:"firstName"`
	LastName     string `json:"lastName"`
	PhoneNumber  string `json:"phoneNumber"`
	PhoneMatches bool   `json:"phoneMatches"`
	Status       string `json:"status"`
}

type MandateRequest struct {
	BankCode      string `json:"bankCode"`
	AccountNumber string `json:"accountNumber"`
}

type MandateResponse struct {
	AccountName   string `json:"accountName"`
	AccountNumber string `json:"accountNumber"`
	BankName      string `json:"bankName"`
	BankCode      string `json:"bankCode"`
	Status        string `json:"status"`
}

type SettlementResult struct {
	Status        string
	TransactionID string
	RejectReason  string
}

type CardSettlementResponse struct {
	XMLName xml.Name `xml:"CardSettlementResponse" json:"-"`
	Status  string   `json:"status" xml:"Status"`
	Message string   `json:"message" xml:"Message"`
}

type IdentificationVerificationResponse struct {
	XMLName     xml.Name `xml:"IdVrfctnRpt" json:"-"`
	Verified    bool     `json:"verified" xml:"Rpt>Vrfctn"`
	AccountName string   `json:"accountName" xml:"Rpt>OrgnlPtyAndAcctId>Pty>Nm"`
}

func (c *NIBSSClient) VerifyAccountIdentification(ctx context.Context, xmlData []byte) (*IdentificationVerificationResponse, error) {
	// Create a child context with ACMT timeout
	opCtx, cancel := context.WithTimeout(ctx, c.acmtTimeout)
	defer cancel()

	body, err := c.iso20022Breaker.Execute(func() (interface{}, error) {
		req, err := http.NewRequestWithContext(opCtx, "POST", c.acmtURL, bytes.NewBuffer(xmlData))
		if err != nil {
			return nil, fmt.Errorf("failed to create acmt.023 request: %w", err)
		}
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

		resp, err := c.httpClient.Do(req)
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

		var idResp IdentificationVerificationResponse
		if err := xml.NewDecoder(resp.Body).Decode(&idResp); err != nil {
			return nil, fmt.Errorf("failed to decode acmt.024 response: %w", err)
		}
		return &idResp, nil
	})
	if err != nil {
		return nil, err
	}
	return body.(*IdentificationVerificationResponse), nil
}

func fallback(primary, fallbackURL string) string {
	if primary != "" {
		return primary
	}
	return fallbackURL
}

func NewNIBSSClient() *NIBSSClient {
	nibssBase := viper.GetString("nibss.base_url")

	// Load timeout configurations with sensible defaults (in seconds)
	getTimeout := func(key string, defaultSecs int) time.Duration {
		val := viper.GetDuration(key)
		if val == 0 {
			val = time.Duration(viper.GetInt(key)) * time.Second
		}
		if val == 0 {
			val = time.Duration(defaultSecs) * time.Second
		}
		return val
	}

	return &NIBSSClient{
		mandateBaseURL: fallback(viper.GetString("nibss.mandate_url"), nibssBase),
		bvnBaseURL:     fallback(viper.GetString("nibss.bvn_url"), nibssBase),
		iso8583BaseURL: fallback(viper.GetString("iso8583.base_url"), nibssBase),
		pacsURL:        fallback(viper.GetString("nibss.pacs.endpoint.url"), nibssBase),
		acmtURL:        fallback(viper.GetString("nibss.acmt.endpoint.url"), nibssBase),
		painURL:        fallback(viper.GetString("nibss.pain.endpoint.url"), nibssBase),
		apiKey:         viper.GetString("nibss.api_key"),
		httpClient: &http.Client{
			Timeout: getTimeout("nibss.http_timeout", 30),
		},
		circuitBreaker:        circuitbreaker.Get("NIBSS-Settlement", circuitbreaker.NIBSSSettlementSettings()),
		iso20022Breaker:       circuitbreaker.Get("NIBSS-ISO20022", circuitbreaker.NIBSSISO20022Settings()),
		bvnBreaker:            circuitbreaker.Get("NIBSS-BVN", circuitbreaker.NIBSSBVNSettings()),
		mandateBreaker:        circuitbreaker.Get("NIBSS-Mandate", circuitbreaker.NIBSSMandateSettings()),
		defaultTimeout:        getTimeout("nibss.http_timeout", 30),
		bvnTimeout:            getTimeout("nibss.bvn_timeout", 15),
		mandateTimeout:        getTimeout("nibss.mandate_timeout", 10),
		pacsTimeout:           getTimeout("nibss.pacs_timeout", 45),
		acmtTimeout:           getTimeout("nibss.acmt_timeout", 20),
		cardSettlementTimeout: getTimeout("nibss.card_settlement_timeout", 30),
	}
}

func (c *NIBSSClient) VerifyBVN(ctx context.Context, bvn, phoneNumber string) (*BVNVerifyResponse, error) {
	// Create a child context with BVN timeout
	opCtx, cancel := context.WithTimeout(ctx, c.bvnTimeout)
	defer cancel()

	result, err := c.bvnBreaker.Execute(func() (interface{}, error) {
		reqBody := BVNVerifyRequest{BVN: bvn, PhoneNumber: phoneNumber}
		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal BVN request: %w", err)
		}

		req, err := http.NewRequestWithContext(opCtx, "POST", fmt.Sprintf("%s/kyc/bvn/verify", c.bvnBaseURL), bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, fmt.Errorf("failed to create BVN request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("BVN verification request failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("NIBSS BVN API returned status %d", resp.StatusCode)
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("BVN not found")
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("NIBSS BVN API returned status %d", resp.StatusCode)
		}

		var bvnResp BVNVerifyResponse
		if err := json.NewDecoder(resp.Body).Decode(&bvnResp); err != nil {
			return nil, fmt.Errorf("failed to decode BVN response: %w", err)
		}
		return &bvnResp, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*BVNVerifyResponse), nil
}

func (c *NIBSSClient) GetAccountMandate(ctx context.Context, bankCode, accountNumber string) (*MandateResponse, error) {
	// Create a child context with Mandate timeout
	opCtx, cancel := context.WithTimeout(ctx, c.mandateTimeout)
	defer cancel()

	result, err := c.mandateBreaker.Execute(func() (interface{}, error) {
		reqBody := MandateRequest{BankCode: bankCode, AccountNumber: accountNumber}
		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request: %w", err)
		}

		req, err := http.NewRequestWithContext(opCtx, "POST", fmt.Sprintf("%s/mandate/inquiry", c.mandateBaseURL), bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to execute request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("NIBSS mandate API returned status %d", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("NIBSS mandate API returned status %d", resp.StatusCode)
		}

		var mandateResp MandateResponse
		if err := json.NewDecoder(resp.Body).Decode(&mandateResp); err != nil {
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}
		return &mandateResp, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*MandateResponse), nil
}

func (c *NIBSSClient) ProcessFundsTransferSettlement(ctx context.Context, xmlData []byte) (*pacs_v08.FIToFIPaymentStatusReportV08, error) {
	// Create a child context with PACS timeout
	opCtx, cancel := context.WithTimeout(ctx, c.pacsTimeout)
	defer cancel()

	body, err := c.iso20022Breaker.Execute(func() (interface{}, error) {
		req, err := http.NewRequestWithContext(opCtx, "POST", c.pacsURL, bytes.NewBuffer(xmlData))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to execute request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("NIBSS API returned status %d", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("NIBSS API returned status %d", resp.StatusCode)
		}

		var pacs002 pacs_v08.FIToFIPaymentStatusReportV08
		if err := xml.NewDecoder(resp.Body).Decode(&pacs002); err != nil {
			return nil, fmt.Errorf("failed to decode pacs.002 response: %w", err)
		}
		return &pacs002, nil
	})
	if err != nil {
		return nil, err
	}
	return body.(*pacs_v08.FIToFIPaymentStatusReportV08), nil
}

func (c *NIBSSClient) RequestPaymentStatus(ctx context.Context, xmlData []byte) (*pacs_v08.FIToFIPaymentStatusReportV08, error) {
	// Create a child context with PACS timeout
	opCtx, cancel := context.WithTimeout(ctx, c.pacsTimeout)
	defer cancel()

	body, err := c.iso20022Breaker.Execute(func() (interface{}, error) {
		req, err := http.NewRequestWithContext(opCtx, "POST", c.pacsURL, bytes.NewBuffer(xmlData))
		if err != nil {
			return nil, fmt.Errorf("failed to create pacs.028 request: %w", err)
		}
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("pacs.028 request failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("NIBSS pacs.028 API returned status %d", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("NIBSS pacs.028 API returned status %d", resp.StatusCode)
		}

		var pacs002 pacs_v08.FIToFIPaymentStatusReportV08
		if err := xml.NewDecoder(resp.Body).Decode(&pacs002); err != nil {
			return nil, fmt.Errorf("failed to decode pacs.002 status response: %w", err)
		}
		return &pacs002, nil
	})
	if err != nil {
		return nil, err
	}
	return body.(*pacs_v08.FIToFIPaymentStatusReportV08), nil
}

func (c *NIBSSClient) ProcessCardSettlement(ctx context.Context, xmlData []byte) (*CardSettlementResponse, error) {
	// Create a child context with card settlement timeout
	opCtx, cancel := context.WithTimeout(ctx, c.cardSettlementTimeout)
	defer cancel()

	body, err := c.circuitBreaker.Execute(func() (interface{}, error) {
		req, err := http.NewRequestWithContext(opCtx, "POST", fmt.Sprintf("%s/settlement/card", c.iso8583BaseURL), bytes.NewBuffer(xmlData))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to execute request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("NIBSS API returned status %d", resp.StatusCode)
		}

		if resp.StatusCode != http.StatusOK {
			var respBody []byte
			respBody, _ = io.ReadAll(resp.Body)
			slog.Error("NIBSS API Response", "status", resp.StatusCode, "body", string(respBody))
			return nil, fmt.Errorf("NIBSS API returned status %d", resp.StatusCode)
		}

		var settlementResp CardSettlementResponse
		if err := xml.NewDecoder(resp.Body).Decode(&settlementResp); err != nil {
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}
		return &settlementResp, nil
	})

	if err != nil {
		return nil, err
	}

	return body.(*CardSettlementResponse), nil
}
