package services

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"time"

	"github.com/sony/gobreaker"
	"github.com/spf13/viper"
)

type NIBSSClient struct {
	mandateBaseURL    string
	settlementBaseURL string
	apiKey            string
	httpClient        *http.Client
	circuitBreaker    *gobreaker.CircuitBreaker
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

type FundsTransferSettlementResponse struct {
	XMLName         xml.Name `xml:"FundsTransferSettlementResponse" json:"-"`
	Status          string   `json:"status" xml:"Status"`
	Message         string   `json:"message" xml:"Message"`
	TransactionId   string   `json:"transactionId" xml:"TransactionId"`
	TransactionDate string   `json:"transactionDate" xml:"TransactionDate"`
}

type CardSettlementResponse struct {
	XMLName xml.Name `xml:"CardSettlementResponse" json:"-"`
	Status  string   `json:"status" xml:"Status"`
	Message string   `json:"message" xml:"Message"`
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

	cbSettings := gobreaker.Settings{
		Name:        "NIBSS-Settlement",
		MaxRequests: 1,
		Interval:    60 * time.Second,
		Timeout:     30 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return counts.Requests >= 5 && failureRatio >= 0.6
		},
	}

	return &NIBSSClient{
		mandateBaseURL:    mandateURL,
		settlementBaseURL: settlementURL,
		apiKey:            viper.GetString("nibss.api_key"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		circuitBreaker: gobreaker.NewCircuitBreaker(cbSettings),
	}
}

func (c *NIBSSClient) GetAccountMandate(bankCode, accountNumber string) (*MandateResponse, error) {
	reqBody := MandateRequest{
		BankCode:      bankCode,
		AccountNumber: accountNumber,
	}

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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("NIBSS API returned status %d", resp.StatusCode)
	}

	var mandateResp MandateResponse
	if err := json.NewDecoder(resp.Body).Decode(&mandateResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &mandateResp, nil
}

func (c *NIBSSClient) ProcessFundsTransferSettlement(xmlData []byte) (*FundsTransferSettlementResponse, error) {
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

		var settlementResp FundsTransferSettlementResponse
		if err := xml.NewDecoder(resp.Body).Decode(&settlementResp); err != nil {
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}
		return &settlementResp, nil
	})

	if err != nil {
		return nil, err
	}

	return body.(*FundsTransferSettlementResponse), nil
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
