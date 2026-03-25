package services

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"time"

	"github.com/moov-io/iso20022/pkg/pacs_v08"
	"github.com/ruralpay/backend/internal/circuitbreaker"
	"github.com/sony/gobreaker"
	"github.com/spf13/viper"
)

type NIBSSClient struct {
	mandateBaseURL    string
	settlementBaseURL string
	bvnBaseURL        string
	apiKey            string
	httpClient        *http.Client
	circuitBreaker    *gobreaker.CircuitBreaker
	bvnBreaker        *gobreaker.CircuitBreaker
	mandateBreaker    *gobreaker.CircuitBreaker
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

func (c *NIBSSClient) VerifyAccountIdentification(xmlData []byte) (*IdentificationVerificationResponse, error) {
	body, err := c.mandateBreaker.Execute(func() (interface{}, error) {
		req, err := http.NewRequest("POST", fmt.Sprintf("%s/acmt/identification-verification", c.mandateBaseURL), bytes.NewBuffer(xmlData))
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

func NewNIBSSClient() *NIBSSClient {
	baseURL := viper.GetString("nibss.base_url")
	mandateURL := viper.GetString("nibss.mandate_url")
	if mandateURL == "" {
		mandateURL = baseURL
	}
	settlementURL := viper.GetString("nibss.settlement_url")
	if settlementURL == "" {
		settlementURL = baseURL
	}
	bvnURL := viper.GetString("nibss.bvn_url")
	if bvnURL == "" {
		bvnURL = baseURL
	}

	return &NIBSSClient{
		mandateBaseURL:    mandateURL,
		settlementBaseURL: settlementURL,
		bvnBaseURL:        bvnURL,
		apiKey:            viper.GetString("nibss.api_key"),
		httpClient:        &http.Client{Timeout: 30 * time.Second},
		circuitBreaker:    circuitbreaker.Get("NIBSS-Settlement", circuitbreaker.NIBSSSettlementSettings()),
		bvnBreaker:        circuitbreaker.Get("NIBSS-BVN", circuitbreaker.NIBSSBVNSettings()),
		mandateBreaker:    circuitbreaker.Get("NIBSS-Mandate", circuitbreaker.NIBSSMandateSettings()),
	}
}

func (c *NIBSSClient) VerifyBVN(bvn, phoneNumber string) (*BVNVerifyResponse, error) {
	result, err := c.bvnBreaker.Execute(func() (interface{}, error) {
		reqBody := BVNVerifyRequest{BVN: bvn, PhoneNumber: phoneNumber}
		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal BVN request: %w", err)
		}

		req, err := http.NewRequest("POST", fmt.Sprintf("%s/kyc/bvn/verify", c.bvnBaseURL), bytes.NewBuffer(jsonData))
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

func (c *NIBSSClient) GetAccountMandate(bankCode, accountNumber string) (*MandateResponse, error) {
	result, err := c.mandateBreaker.Execute(func() (interface{}, error) {
		reqBody := MandateRequest{BankCode: bankCode, AccountNumber: accountNumber}
		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request: %w", err)
		}

		req, err := http.NewRequest("POST", fmt.Sprintf("%s/mandate/inquiry", c.mandateBaseURL), bytes.NewBuffer(jsonData))
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

func (c *NIBSSClient) ProcessFundsTransferSettlement(xmlData []byte) (*pacs_v08.FIToFIPaymentStatusReportV08, error) {
	body, err := c.circuitBreaker.Execute(func() (interface{}, error) {
		req, err := http.NewRequest("POST", fmt.Sprintf("%s/settlement/funds-transfer", c.settlementBaseURL), bytes.NewBuffer(xmlData))
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

func (c *NIBSSClient) RequestPaymentStatus(xmlData []byte) (*pacs_v08.FIToFIPaymentStatusReportV08, error) {
	body, err := c.circuitBreaker.Execute(func() (interface{}, error) {
		req, err := http.NewRequest("POST", fmt.Sprintf("%s/settlement/payment-status", c.settlementBaseURL), bytes.NewBuffer(xmlData))
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

func (c *NIBSSClient) ProcessCardSettlement(xmlData []byte) (*CardSettlementResponse, error) {
	body, err := c.circuitBreaker.Execute(func() (interface{}, error) {
		req, err := http.NewRequest("POST", fmt.Sprintf("%s/settlement/card", c.settlementBaseURL), bytes.NewBuffer(xmlData))
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
