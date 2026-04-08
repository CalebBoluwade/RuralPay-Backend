package services

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/moov-io/iso20022/pkg/pacs_v08"
	"github.com/moov-io/iso8583"
	"github.com/moov-io/iso8583/encoding"
	"github.com/moov-io/iso8583/field"
	"github.com/moov-io/iso8583/prefix"
	"github.com/ruralpay/backend/internal/circuitbreaker"
	"github.com/ruralpay/backend/internal/constants"
	"github.com/sony/gobreaker"
	"github.com/spf13/viper"
)

type NIBSSClient struct {
	mandateBaseURL string
	iso8583BaseURL string // ISO 8583 card settlement
	bvnBaseURL     string
	pacsURL        string // ISO 20022 pacs (pacs.008, pacs.002, pacs.028)
	acmtURL        string // ISO 20022 acmt (acmt.023, acmt.024)
	painURL        string // ISO 20022 pain
	apiKey         string
	sslCertPath    string
	sslKeyPath     string

	merchantID             string
	terminalID             string
	acquiringInstitutionID string
	httpClient             *http.Client
	circuitBreaker         *gobreaker.CircuitBreaker
	iso20022Breaker        *gobreaker.CircuitBreaker // ISO 20022 (ACMT/PACS) breaker
	bvnBreaker             *gobreaker.CircuitBreaker
	mandateBreaker         *gobreaker.CircuitBreaker
	defaultTimeout         time.Duration
	bvnTimeout             time.Duration
	mandateTimeout         time.Duration
	pacsTimeout            time.Duration
	acmtTimeout            time.Duration
	cardSettlementTimeout  time.Duration
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
		req.Header.Set(constants.ContentType, constants.XMLContentType)
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

	iso20022BaseURL := viper.GetString("nibss.iso20022.base.url")

	return &NIBSSClient{
		mandateBaseURL: fallback(viper.GetString("nibss.mandate_url"), nibssBase),
		bvnBaseURL:     fallback(viper.GetString("nibss.bvn_url"), nibssBase),

		iso8583BaseURL: fallback(viper.GetString("nibss.iso8583.base_url"), nibssBase),
		pacsURL:        fallback(iso20022BaseURL+"/nps/pacs", nibssBase),
		acmtURL:        fallback(iso20022BaseURL+"/nps/acmt", nibssBase),
		painURL:        fallback(iso20022BaseURL+"/nps/pain", nibssBase),

		sslCertPath: viper.GetString("iso8583.ssl_cert_path"),
		sslKeyPath:  viper.GetString("iso8583.ssl_key_path"),

		merchantID:             viper.GetString("iso8583.card_acceptor_id"),
		terminalID:             viper.GetString("iso8583.terminal_id"),
		acquiringInstitutionID: viper.GetString("iso8583.acquiring_institution_id"),

		apiKey: viper.GetString("nibss.api_key"),

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
		req.Header.Set(constants.ContentType, "application/json")
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
		req.Header.Set(constants.ContentType, "application/json")
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
		req.Header.Set(constants.ContentType, constants.XMLContentType)
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
		req.Header.Set(constants.ContentType, constants.XMLContentType)
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

func (c *NIBSSClient) PerformKeyExchange(conn *tls.Conn) (string, error) {
	now := time.Now()
	stan := fmt.Sprintf("%06d", now.Unix()%1000000)

	spec := iso8583.Spec87
	spec.Fields[3] = field.NewString(&field.Spec{
		Length:      6,
		Description: "Processing Code",
		Enc:         encoding.ASCII,
		Pref:        prefix.ASCII.Fixed,
	})
	msg := iso8583.NewMessage(spec)

	// MTI (0800 = network management request)
	msg.MTI("0800")
	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 0, "value", "0800")

	_ = msg.Field(3, "000000")
	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 3, "value", "000000")

	// Transmission Date & Time (MMDDhhmmss)
	_ = msg.Field(7, now.Format("0102150405"))
	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 7, "value", now.Format("0102150405"))

	// STAN
	_ = msg.Field(11, stan)
	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 11, "value", stan)

	// Acquirer ID
	_ = msg.Field(32, c.acquiringInstitutionID)
	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 32, "value", c.acquiringInstitutionID)

	// Terminal ID
	_ = msg.Field(41, c.terminalID)
	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 41, "value", c.terminalID)

	_ = msg.Field(42, c.merchantID)
	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 42, "value", c.merchantID)

	// Network Management Code (001 = sign-on / key exchange)
	_ = msg.Field(70, "001")
	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 70, "value", "001")

	packed, err := msg.Pack()
	if err != nil {
		return "", fmt.Errorf("pack failed: %w", err)
	}

	lengthHeader := make([]byte, 2)
	binary.BigEndian.PutUint16(lengthHeader, uint16(len(packed)))

	fullMsg := append(lengthHeader, packed...)

	// Debug (DO NOT REMOVE while testing)
	slog.Debug(fmt.Sprintf("Request ISO OUT (hex): %x", fullMsg))

	// Set write timeout
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))

	// Send
	if _, err := conn.Write(fullMsg); err != nil {
		return "", fmt.Errorf("write failed: %w", err)
	}

	// --- RECEIVE ---
	_ = conn.SetReadDeadline(time.Now().Add(35 * time.Second))

	// Read 4-byte ASCII length header
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", fmt.Errorf("read header failed: %w", err)
	}

	respLen := binary.BigEndian.Uint16(header)

	// Read ISO message body
	body := make([]byte, respLen)
	if _, err := io.ReadFull(conn, body); err != nil {
		return "", fmt.Errorf("read body failed: %w", err)
	}

	slog.Debug(fmt.Sprintf("Response ISO In (hex): %x", body))

	resp := iso8583.NewMessage(iso8583.Spec87)
	if err := resp.Unpack(body); err != nil {
		return "", fmt.Errorf("unpack failed: %w", err)
	}

	// ✅ Always check response code
	code, _ := resp.GetString(39)
	if code != "00" {
		return "", fmt.Errorf("key exchange failed, response code: %s", code)
	}

	// ⚠️ Field 53 usually contains encrypted key material
	sessionKey, err := resp.GetString(53)
	if err != nil {
		return "", fmt.Errorf("failed to get field 53: %w", err)
	}

	return sessionKey, nil
}

func (c *NIBSSClient) DecryptSessionKey(encryptedKey string) ([]byte, error) {
	// Load private key
	keyBytes, err := os.ReadFile(c.sslKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key: %w", err)
	}
	privateKey, err := x509.ParsePKCS1PrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	// Decode hex-encoded encrypted key
	decodedKey, err := hex.DecodeString(encryptedKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decode session key: %w", err)
	}

	// Decrypt with RSA-OAEP
	decryptedKey, err := rsa.DecryptOAEP(sha1.New(), rand.Reader, privateKey, decodedKey, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt session key: %w", err)
	}

	return decryptedKey, nil
}

func (c *NIBSSClient) ProcessCardSettlement(ctx context.Context, xmlData []byte) (*CardSettlementResponse, error) {
	body, err := c.circuitBreaker.Execute(func() (interface{}, error) {
		cert, err := tls.LoadX509KeyPair(c.sslCertPath, c.sslKeyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load SSL key pair: %w", err)
		}

		config := &tls.Config{
			MinVersion:         tls.VersionTLS12,
			Certificates:       []tls.Certificate{cert},
			InsecureSkipVerify: true, // Use this only for testing; replace with proper CA verification
		}

		dialer := &net.Dialer{Timeout: c.cardSettlementTimeout}
		conn, err := tls.DialWithDialer(dialer, "tcp", c.iso8583BaseURL, config)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to socket: %w", err)
		}
		defer conn.Close()

		deadline, _ := ctx.Deadline()
		if deadline.IsZero() {
			deadline = time.Now().Add(c.cardSettlementTimeout)
		}
		_ = conn.SetDeadline(deadline)

		// Perform key exchange and get the session key
		encryptedSessionKey, err := c.PerformKeyExchange(conn)
		if err != nil {
			return nil, err
		}

		slog.Debug("encryptedSessionKey >>>", "key", encryptedSessionKey)
		_, err = c.DecryptSessionKey(encryptedSessionKey)
		if err != nil {
			return nil, err
		}

		// Encrypt and send the actual settlement data (not implemented in this example)
		// For now, we'll send the raw XML data as in the original function
		header := make([]byte, 2)
		binary.BigEndian.PutUint16(header, uint16(len(xmlData)))

		if _, err := conn.Write(append(header, xmlData...)); err != nil {
			return nil, fmt.Errorf("failed to write to socket: %w", err)
		}

		respHeader := make([]byte, 2)
		if _, err := io.ReadFull(conn, respHeader); err != nil {
			return nil, fmt.Errorf("failed to read response header: %w", err)
		}

		respLen := binary.BigEndian.Uint16(respHeader)
		respBody := make([]byte, respLen)

		if _, err := io.ReadFull(conn, respBody); err != nil {
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}

		var settlementResp CardSettlementResponse
		if err := xml.Unmarshal(respBody, &settlementResp); err != nil {
			slog.Error("NIBSS Socket Raw Response", "body", string(respBody))
			return nil, fmt.Errorf("failed to decode socket response: %w", err)
		}

		return &settlementResp, nil
	})

	if err != nil {
		return nil, err
	}

	return body.(*CardSettlementResponse), nil
}
