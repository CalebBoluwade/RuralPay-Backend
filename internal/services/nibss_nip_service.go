package services

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
	"github.com/spf13/viper"
)

// NipConfig holds all NIP SOAP configuration
type NipConfig struct {
	BankCode          string
	PaymentPrefix     string
	CryptoURL         string
	CoreURL           string
	EncryptionBaseURL string
	BaseURL           string
	TsqBaseURL        string
	TimeoutSeconds    int
}

func LoadNipConfig() NipConfig {
	return NipConfig{
		BankCode:          viper.GetString("nip.bank_code"),
		PaymentPrefix:     viper.GetString("nip.payment_prefix"),
		CryptoURL:         viper.GetString("nip.crypto_url"),
		CoreURL:           viper.GetString("nip.core_url"),
		EncryptionBaseURL: viper.GetString("nip.encryption_base_url"),
		BaseURL:           viper.GetString("nip.base_url"),
		TsqBaseURL:        viper.GetString("nip.tsq_base_url"),
		TimeoutSeconds:    viper.GetInt("nip.timeout_seconds"),
	}
}

func (c NipConfig) nameEnquiryURL() string {
	return c.BaseURL + "/NIPWS/NIPInterface/nameenquirysingleitem"
}
func (c NipConfig) balanceEnquiryURL() string {
	return c.BaseURL + "/NIPWS/NIPInterface/balanceenquiry"
}
func (c NipConfig) mandateAdviceURL() string {
	return c.BaseURL + "/NIPWS/NIPInterface/mandateadvice"
}
func (c NipConfig) ftDebitURL() string {
	return c.BaseURL + "/NIPWS/NIPInterface/fundtransfersingleitem_dd"
}
func (c NipConfig) ftCreditURL() string {
	return c.BaseURL + "/NIPWS/NIPInterface/fundtransfersingleitem_dc"
}
func (c NipConfig) tsqURL() string {
	return c.TsqBaseURL + "/NIPWS/NIPTSQInterface/txnstatusquerysingleitem"
}
func (c NipConfig) encryptURL() string {
	return c.EncryptionBaseURL + "/nip/crypto/encrypt"
}
func (c NipConfig) decryptURL() string {
	return c.EncryptionBaseURL + "/nip/crypto/decrypt"
}

// nipSoapClient handles the low-level SOAP encrypt→post→decrypt cycle
type nipSoapClient struct {
	config     NipConfig
	httpClient *http.Client
}

func newNipSoapClient(cfg NipConfig) *nipSoapClient {
	return &nipSoapClient{
		config: cfg,
		httpClient: &http.Client{
			Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second,
		},
	}
}

func (s *nipSoapClient) buildEncryptEnvelope(xmlPayload string) string {
	return fmt.Sprintf(
		`<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:ws="%s">`+
			`<soapenv:Header/><soapenv:Body><ws:Encrypt>`+
			`<BankCode>%s</BankCode>`+
			`<EncryptValue><![CDATA[%s]]></EncryptValue>`+
			`</ws:Encrypt></soapenv:Body></soapenv:Envelope>`,
		s.config.CryptoURL, s.config.BankCode, xmlPayload,
	)
}

func (s *nipSoapClient) buildDecryptEnvelope(ciphertext string) string {
	return fmt.Sprintf(
		`<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:ws="%s">`+
			`<soapenv:Header/><soapenv:Body><ws:Decrypt>`+
			`<BankCode>%s</BankCode>`+
			`<DecryptValue>%s</DecryptValue>`+
			`</ws:Decrypt></soapenv:Body></soapenv:Envelope>`,
		s.config.CryptoURL, s.config.BankCode, ciphertext,
	)
}

func (s *nipSoapClient) buildNipEnvelope(ciphertext, parentElement string) string {
	closingTag := strings.Replace(parentElement, "<", "</", 1)
	return fmt.Sprintf(
		`<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:core="%s">`+
			`<soapenv:Header/><soapenv:Body>`+
			`%s<request>%s</request>%s`+
			`</soapenv:Body></soapenv:Envelope>`,
		s.config.CoreURL, parentElement, ciphertext, closingTag,
	)
}

func (s *nipSoapClient) post(ctx context.Context, url, soapEnvelope string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		bytes.NewBufferString(soapEnvelope))
	if err != nil {
		slog.Error("nip.failed_to_create_request", "url", url, "error", err)
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("Accept", "text/xml")

	slog.Debug("nip.sending_request", "url", url, "soap_envelope", soapEnvelope)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		slog.Error("nip.http_request_failed", "url", url, "error", err, "error_type", fmt.Sprintf("%T", err))
		return "", fmt.Errorf("http request failed to %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("nip.failed_to_read_response_body", "url", url, "error", err)
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	slog.Debug("nip.http_response_received", "url", url, "status_code", resp.StatusCode, "response_body", string(body))

	if resp.StatusCode != http.StatusOK {
		var faultEnv struct {
			Body struct {
				Fault struct {
					FaultString string `xml:"faultstring"`
				} `xml:"Fault"`
			} `xml:"Body"`
		}
		_ = xml.Unmarshal(body, &faultEnv)
		slog.Error("nip.http_response_error", "url", url, "status_code", resp.StatusCode, "fault_string", faultEnv.Body.Fault.FaultString, "response_body", string(body))
		return "", utils.NewNIPErrorMsg(utils.NIPResponseCode("99"),
			faultEnv.Body.Fault.FaultString)
	}

	slog.Debug("nip.post_success", "url", url, "status_code", resp.StatusCode)
	return extractSoapBodyInnerText(body), nil
}

// extractSoapBodyInnerText extracts the text content from the SOAP Body.
// NIBSS responses have the structure:
//
//	<S:Body><ns2:XxxResponse><return>TEXT_OR_XML</return></ns2:XxxResponse></S:Body>
//
// We need the raw inner content of <return> — which may be a hex ciphertext string
// (encrypt/NIP responses) or a decrypted XML string (decrypt response).
func extractSoapBodyInnerText(soapXML []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(soapXML))
	depth := 0
	inBody := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			local := strings.ToLower(t.Name.Local)
			if local == "body" {
				inBody = true
				continue
			}
			if inBody {
				depth++
				// depth==2 is the <return> element inside the response wrapper
				if depth == 2 && local == "return" {
					var inner struct {
						Inner []byte `xml:",innerxml"`
					}
					if err := dec.DecodeElement(&inner, &t); err == nil {
						return html.UnescapeString(strings.TrimSpace(string(inner.Inner)))
					}
				}
			}
		case xml.EndElement:
			if strings.ToLower(t.Name.Local) == "body" {
				return ""
			}
			if inBody && depth > 0 {
				depth--
			}
		}
	}
	return ""
}

func serializeXML(v any) (string, error) {
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("xml serialize failed: %w", err)
	}
	return buf.String(), nil
}

func deserializeXML[T any](xmlText string) (*T, error) {
	var result T
	if err := xml.NewDecoder(strings.NewReader(xmlText)).Decode(&result); err != nil {
		return nil, fmt.Errorf("xml deserialize failed: %w", err)
	}
	return &result, nil
}

// execute runs the full encrypt→NIP→decrypt cycle for a given request/response pair
func (s *nipSoapClient) execute(ctx context.Context, url, parentElement string, req any) (string, error) {
	xmlMsg, err := serializeXML(req)
	if err != nil {
		slog.Error("nip.xml_serialization_failed", "error", err)
		return "", utils.NewNIPError(utils.NIPResponseCode("99"), err)
	}
	slog.Debug("nip.step_serialize_xml_success", "xml_msg", xmlMsg)

	encryptEnvelope := s.buildEncryptEnvelope(xmlMsg)
	slog.Debug("nip.step_encrypt_start", "encrypt_url", s.config.encryptURL(), "bank_code", s.config.BankCode, "soap_envelope", encryptEnvelope)
	encrypted, err := s.post(ctx, s.config.encryptURL(), encryptEnvelope)
	if err != nil {
		slog.Error("nip.step_encrypt_failed", "error", err)
		return "", utils.NewNIPError(utils.NIPResponseCode("99"), err)
	}
	slog.Debug("nip.step_encrypt_success", "encrypted_result", encrypted)

	nipEnvelope := s.buildNipEnvelope(encrypted, parentElement)
	slog.Debug("nip.step_nip_request_start", "nip_url", url, "soap_envelope", nipEnvelope)
	nipResp, err := s.post(ctx, url, nipEnvelope)
	if err != nil {
		slog.Error("nip.step_nip_request_failed", "nip_url", url, "error", err)
		return "", utils.NewNIPError(utils.NIPResponseCode("99"), err)
	}
	slog.Debug("nip.step_nip_request_success", "nip_url", url, "nip_response", nipResp)

	decryptEnvelope := s.buildDecryptEnvelope(nipResp)
	slog.Debug("nip.step_decrypt_start", "decrypt_url", s.config.decryptURL(), "bank_code", s.config.BankCode, "soap_envelope", decryptEnvelope)
	decrypted, err := s.post(ctx, s.config.decryptURL(), decryptEnvelope)
	if err != nil {
		slog.Error("nip.step_decrypt_failed", "error", err)
		return "", utils.NewNIPError(utils.NIPResponseCode("99"), err)
	}
	slog.Debug("nip.step_decrypt_success", "decrypted_result", decrypted)

	return decrypted, nil
}

// NIBSSNIPService is the high-level NIP service
type NIBSSNIPService struct {
	config NipConfig
	soap   *nipSoapClient
}

func NewNIBSSNIPService() *NIBSSNIPService {
	cfg := LoadNipConfig()
	return &NIBSSNIPService{
		config: cfg,
		soap:   newNipSoapClient(cfg),
	}
}

// NewNIBSSNIPServiceWith allows injecting a custom soap client (for testing)
func NewNIBSSNIPServiceWith(cfg NipConfig, soap *nipSoapClient) *NIBSSNIPService {
	return &NIBSSNIPService{config: cfg, soap: soap}
}

func (svc *NIBSSNIPService) GetNIPBankCode() string      { return svc.config.BankCode }
func (svc *NIBSSNIPService) GetNIPPaymentPrefix() string { return svc.config.PaymentPrefix }

func (svc *NIBSSNIPService) ExecuteNameEnquiry(ctx context.Context, req *models.NESingleRequest) (*models.NESingleResponse, error) {
	slog.Info("nip.name_enquiry.start", "sessionId", req.SessionID, "account", req.AccountNumber, "bankCode", req.DestinationInstitutionCode)
	slog.Debug("nip.name_enquiry.config", "base_url", svc.config.BaseURL, "encryption_url", svc.config.EncryptionBaseURL, "timeout_seconds", svc.config.TimeoutSeconds)

	decrypted, err := svc.soap.execute(ctx, svc.config.nameEnquiryURL(), "<core:nameenquirysingleitem>", req)
	if err != nil {
		slog.Error("nip.name_enquiry.soap_execute_failed", "sessionId", req.SessionID, "error", err)
		return nil, svc.wrapErr(err, "name enquiry")
	}
	slog.Debug("nip.name_enquiry.soap_execute_success", "sessionId", req.SessionID, "decrypted_xml", decrypted)

	resp, err := deserializeXML[models.NESingleResponse](decrypted)
	if err != nil {
		slog.Error("nip.name_enquiry.xml_deserialize_failed", "sessionId", req.SessionID, "error", err, "raw_xml", decrypted)
		return nil, utils.NewNIPError(utils.NIPResponseCode("99"), err)
	}
	return resp, nil
}

func (svc *NIBSSNIPService) ExecuteMandateAdvice(ctx context.Context, req *models.MandateAdviceRequest) (*models.MandateAdviceResponse, error) {
	slog.Info("nip.mandate_advice.start", "sessionId", req.SessionID, "mandateRef", req.MandateReferenceNumber)
	decrypted, err := svc.soap.execute(ctx, svc.config.mandateAdviceURL(), "<core:mandateadvice>", req)
	if err != nil {
		slog.Error("nip.mandate_advice.soap_execute_failed", "sessionId", req.SessionID, "error", err)
		return nil, svc.wrapErr(err, "mandate advice")
	}
	slog.Debug("nip.mandate_advice.soap_execute_success", "sessionId", req.SessionID, "decrypted_xml", decrypted)
	resp, err := deserializeXML[models.MandateAdviceResponse](decrypted)
	if err != nil {
		slog.Error("nip.mandate_advice.xml_deserialize_failed", "sessionId", req.SessionID, "error", err, "raw_xml", decrypted)
		return nil, utils.NewNIPError(utils.NIPResponseCode("99"), err)
	}
	if err := svc.checkResponseCode(resp.ResponseCode, "mandate advice"); err != nil {
		slog.Error("nip.mandate_advice.response_code_check_failed", "sessionId", resp.SessionID, "response_code", resp.ResponseCode, "error", err)
		return nil, err
	}
	slog.Info("nip.mandate_advice.success", "sessionId", resp.SessionID, "responseCode", resp.ResponseCode)
	return resp, nil
}

func (svc *NIBSSNIPService) ExecuteBalanceEnquiry(ctx context.Context, req *models.BalanceEnquiryRequest) (*models.BalanceEnquiryResponse, error) {
	slog.Info("nip.balance_enquiry.start", "sessionId", req.SessionID, "account", req.TargetAccountNumber)
	decrypted, err := svc.soap.execute(ctx, svc.config.balanceEnquiryURL(), "<core:balanceenquiry>", req)
	if err != nil {
		slog.Error("nip.balance_enquiry.soap_execute_failed", "sessionId", req.SessionID, "error", err)
		return nil, svc.wrapErr(err, "balance enquiry")
	}
	slog.Debug("nip.balance_enquiry.soap_execute_success", "sessionId", req.SessionID, "decrypted_xml", decrypted)
	resp, err := deserializeXML[models.BalanceEnquiryResponse](decrypted)
	if err != nil {
		slog.Error("nip.balance_enquiry.xml_deserialize_failed", "sessionId", req.SessionID, "error", err, "raw_xml", decrypted)
		return nil, utils.NewNIPError(utils.NIPResponseCode("99"), err)
	}
	if err := svc.checkResponseCode(resp.ResponseCode, "balance enquiry"); err != nil {
		slog.Error("nip.balance_enquiry.response_code_check_failed", "sessionId", resp.SessionID, "response_code", resp.ResponseCode, "error", err)
		return nil, err
	}
	slog.Info("nip.balance_enquiry.success", "sessionId", resp.SessionID, "balance", resp.AvailableBalance)
	return resp, nil
}

func (svc *NIBSSNIPService) ExecuteFundsTransferDebit(ctx context.Context, req *models.FTSingleDebitRequest) (*models.FTSingleDebitResponse, error) {
	slog.Info("nip.ft_debit.start", "sessionId", req.SessionID, "amount", req.Amount, "endpoint", svc.config.ftDebitURL())
	decrypted, err := svc.soap.execute(ctx, svc.config.ftDebitURL(), "<core:fundtransfersingleitem_dd>", req)
	if err != nil {
		slog.Error("nip.ft_debit.soap_execute_failed", "sessionId", req.SessionID, "error", err)
		return nil, svc.wrapErr(err, "funds transfer debit")
	}
	slog.Debug("nip.ft_debit.soap_execute_success", "sessionId", req.SessionID, "decrypted_xml", decrypted)
	resp, err := deserializeXML[models.FTSingleDebitResponse](decrypted)
	if err != nil {
		slog.Error("nip.ft_debit.xml_deserialize_failed", "sessionId", req.SessionID, "error", err, "raw_xml", decrypted)
		return nil, utils.NewNIPError(utils.NIPResponseCode("99"), err)
	}
	if err := svc.checkResponseCode(resp.ResponseCode, "funds transfer debit"); err != nil {
		slog.Error("nip.ft_debit.response_code_check_failed", "sessionId", resp.SessionID, "response_code", resp.ResponseCode, "error", err)
		return nil, err
	}
	slog.Info("nip.ft_debit.success", "sessionId", resp.SessionID, "responseCode", resp.ResponseCode)
	return resp, nil
}

func (svc *NIBSSNIPService) ExecuteFundsTransferCredit(ctx context.Context, req *models.FTSingleCreditRequest) (*models.FTSingleCreditResponse, error) {
	slog.Info("nip.ft_credit.start", "sessionId", req.SessionID, "amount", req.Amount, "endpoint", svc.config.ftCreditURL())
	decrypted, err := svc.soap.execute(ctx, svc.config.ftCreditURL(), "<core:fundtransfersingleitem_dc>", req)
	if err != nil {
		slog.Error("nip.ft_credit.soap_execute_failed", "sessionId", req.SessionID, "error", err)
		return nil, svc.wrapErr(err, "funds transfer credit")
	}
	slog.Debug("nip.ft_credit.soap_execute_success", "sessionId", req.SessionID, "decrypted_xml", decrypted)
	resp, err := deserializeXML[models.FTSingleCreditResponse](decrypted)
	if err != nil {
		slog.Error("nip.ft_credit.xml_deserialize_failed", "sessionId", req.SessionID, "error", err, "raw_xml", decrypted)
		return nil, utils.NewNIPError(utils.NIPResponseCode("99"), err)
	}
	if err := svc.checkResponseCode(resp.ResponseCode, "funds transfer credit"); err != nil {
		slog.Error("nip.ft_credit.response_code_check_failed", "sessionId", resp.SessionID, "response_code", resp.ResponseCode, "error", err)
		return nil, err
	}
	slog.Info("nip.ft_credit.success", "sessionId", resp.SessionID, "responseCode", resp.ResponseCode)
	return resp, nil
}

func (svc *NIBSSNIPService) ExecuteTransactionStatusQuery(ctx context.Context, req *models.TSQuerySingleRequest) (*models.TSQuerySingleResponse, error) {
	slog.Info("nip.tsq.start", "sessionId", req.SessionID, "endpoint", svc.config.tsqURL())
	decrypted, err := svc.soap.execute(ctx, svc.config.tsqURL(), "<core:txnstatusquerysingleitem>", req)
	if err != nil {
		slog.Error("nip.tsq.soap_execute_failed", "sessionId", req.SessionID, "error", err)
		return nil, svc.wrapErr(err, "transaction status query")
	}
	slog.Debug("nip.tsq.soap_execute_success", "sessionId", req.SessionID, "decrypted_xml", decrypted)
	resp, err := deserializeXML[models.TSQuerySingleResponse](decrypted)
	if err != nil {
		slog.Error("nip.tsq.xml_deserialize_failed", "sessionId", req.SessionID, "error", err, "raw_xml", decrypted)
		return nil, utils.NewNIPError(utils.NIPResponseCode("99"), err)
	}
	if err := svc.checkResponseCode(resp.ResponseCode, "transaction status query"); err != nil {
		slog.Error("nip.tsq.response_code_check_failed", "sessionId", resp.SessionID, "response_code", resp.ResponseCode, "error", err)
		return nil, err
	}
	slog.Info("nip.tsq.success", "sessionId", resp.SessionID, "responseCode", resp.ResponseCode)
	return resp, nil
}

func (svc *NIBSSNIPService) checkResponseCode(code, action string) error {
	if code == string(utils.NIPResponseCode("00")) {
		return nil
	}
	if code == "" {
		return utils.NewNIPErrorMsg(utils.NIPResponseCode("99"), action+" returned empty response code")
	}
	return utils.NewNIPError(utils.NIPResponseCode(code))
}

func (svc *NIBSSNIPService) wrapErr(err error, action string) error {
	if _, ok := err.(*utils.NIPError); ok {
		return err
	}
	slog.Error("NIP unexpected error", "action", action, "error", err)
	return utils.NewNIPError(utils.NIPResponseCode("99"), err)
}
