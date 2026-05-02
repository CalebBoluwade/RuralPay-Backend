package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testNipServer spins up a fake SOAP server matching the real NIBSS response structure:
// <S:Body><ns2:XxxResponse><return>CONTENT</return></ns2:XxxResponse></S:Body>
func testNipServer(t *testing.T, responses ...string) (*httptest.Server, func()) {
	t.Helper()
	idx := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body string
		if idx < len(responses) {
			body = responses[idx]
			idx++
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w,
			`<S:Envelope xmlns:S="http://schemas.xmlsoap.org/soap/envelope/">`+
				`<S:Body><ns2:Response xmlns:ns2="http://core.nip.nibss/">`+
				`<return>%s</return>`+
				`</ns2:Response></S:Body>`+
				`</S:Envelope>`, body)
	}))
	return srv, srv.Close
}

func testNIPErrorServer(t *testing.T, statusCode int, faultMsg string) (*httptest.Server, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(statusCode)
		fmt.Fprintf(w,
			`<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/">
				<soapenv:Body><soapenv:Fault><faultstring>%s</faultstring></soapenv:Fault></soapenv:Body>
			</soapenv:Envelope>`, faultMsg)
	}))
	return srv, srv.Close
}

func newTestNipService(baseURL string) *NIBSSNIPService {
	cfg := NipConfig{
		BankCode:          "999999",
		CryptoURL:         "http://ws.crypto.nip.nibss.com/",
		CoreURL:           "http://core.nip.nibss/",
		EncryptionBaseURL: baseURL,
		BaseURL:           baseURL,
		TsqBaseURL:        baseURL,
		TimeoutSeconds:    5,
	}
	return NewNIBSSNIPServiceWith(cfg, newNipSoapClient(cfg))
}

// --- Name Enquiry ---

func TestExecuteNameEnquiry_Success(t *testing.T) {
	decryptedXML := `<NESingleResponse>
		<SessionID>sess001</SessionID>
		<DestinationInstitutionCode>999100</DestinationInstitutionCode>
		<ChannelCode>1</ChannelCode>
		<AccountNumber>0035428391</AccountNumber>
		<AccountName>John Doe</AccountName>
		<KYCLevel>1</KYCLevel>
		<BankVerificationNumber>11299200299</BankVerificationNumber>
		<ResponseCode>00</ResponseCode>
	</NESingleResponse>`

	srv, close := testNipServer(t, "ENCRYPTED", "NIPRESPONSE", decryptedXML)
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.NESingleRequest{
		SessionID:                  utils.GenerateNIPSessionId("999999"),
		DestinationInstitutionCode: "999100",
		ChannelCode:                "1",
		AccountNumber:              "0035428391",
	}

	resp, err := svc.ExecuteNameEnquiry(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "John Doe", resp.AccountName)
	assert.Equal(t, "00", resp.ResponseCode)
}

func TestExecuteNameEnquiry_NonSuccessResponseCode(t *testing.T) {
	decryptedXML := `<NESingleResponse>
		<SessionID>sess001</SessionID>
		<ResponseCode>96</ResponseCode>
	</NESingleResponse>`

	srv, close := testNipServer(t, "ENCRYPTED", "NIPRESPONSE", decryptedXML)
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.NESingleRequest{SessionID: "hey", DestinationInstitutionCode: "999100", ChannelCode: "1", AccountNumber: "0035428391"}

	_, err := svc.ExecuteNameEnquiry(context.Background(), req)
	// ExecuteNameEnquiry returns the response without checking the code —
	// response code checking is the caller's responsibility (e.g. NIPNameEnquiry.EnquireName)
	require.NoError(t, err)
}

func TestExecuteNameEnquiry_HttpError(t *testing.T) {
	srv, close := testNIPErrorServer(t, http.StatusInternalServerError, "internal error")
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.NESingleRequest{SessionID: "hey", DestinationInstitutionCode: "999100", ChannelCode: "1", AccountNumber: "0035428391"}

	_, err := svc.ExecuteNameEnquiry(context.Background(), req)
	require.Error(t, err)

	var nipErr *utils.NIPError
	assert.True(t, errors.As(err, &nipErr))
}

func TestExecuteNameEnquiry_EmptyResponseCode(t *testing.T) {
	decryptedXML := `<NESingleResponse><SessionID>sess001</SessionID><ResponseCode></ResponseCode></NESingleResponse>`

	srv, close := testNipServer(t, "ENCRYPTED", "NIPRESPONSE", decryptedXML)
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.NESingleRequest{SessionID: "hey", DestinationInstitutionCode: "999100", ChannelCode: "1", AccountNumber: "0035428391"}

	resp, err := svc.ExecuteNameEnquiry(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, resp.ResponseCode)
}

// --- Mandate Advice ---

func TestExecuteMandateAdvice_Success(t *testing.T) {
	decryptedXML := `<MandateAdviceResponse>
		<SessionID>sess002</SessionID>
		<ResponseCode>00</ResponseCode>
		<BeneficiaryAccountName>Jane Doe</BeneficiaryAccountName>
		<MandateReferenceNumber>NPP/240101120000/12345678</MandateReferenceNumber>
	</MandateAdviceResponse>`

	srv, close := testNipServer(t, "ENCRYPTED", "NIPRESPONSE", decryptedXML)
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.MandateAdviceRequest{
		SessionID:              utils.GenerateNIPSessionId("999999"),
		MandateReferenceNumber: utils.GenerateMandateRef("NPP"),
		ChannelCode:            "1",
	}

	resp, err := svc.ExecuteMandateAdvice(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "Jane Doe", resp.BeneficiaryAccountName)
	assert.Equal(t, "00", resp.ResponseCode)
}

func TestExecuteMandateAdvice_FailResponseCode(t *testing.T) {
	decryptedXML := `<MandateAdviceResponse><SessionID>sess002</SessionID><ResponseCode>96</ResponseCode></MandateAdviceResponse>`

	srv, close := testNipServer(t, "ENCRYPTED", "NIPRESPONSE", decryptedXML)
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.MandateAdviceRequest{SessionID: "hey", ChannelCode: "1"}

	_, err := svc.ExecuteMandateAdvice(context.Background(), req)
	require.Error(t, err)

	var nipErr *utils.NIPError
	require.True(t, errors.As(err, &nipErr))
	assert.Equal(t, utils.NIPResponseCode("96"), nipErr.Code)
}

func TestExecuteMandateAdvice_HttpError(t *testing.T) {
	srv, close := testNIPErrorServer(t, http.StatusInternalServerError, "server error")
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.MandateAdviceRequest{SessionID: "hey", ChannelCode: "1"}

	_, err := svc.ExecuteMandateAdvice(context.Background(), req)
	require.Error(t, err)

	var nipErr *utils.NIPError
	assert.True(t, errors.As(err, &nipErr))
	assert.Equal(t, utils.NIPResponseCode("99"), nipErr.Code)
}

// --- Balance Enquiry ---

func TestExecuteBalanceEnquiry_Success(t *testing.T) {
	decryptedXML := `<BalanceEnquiryResponse>
		<SessionID>sess003</SessionID>
		<ResponseCode>00</ResponseCode>
		<AvailableBalance>10000</AvailableBalance>
		<TargetAccountNumber>0035428391</TargetAccountNumber>
	</BalanceEnquiryResponse>`

	srv, close := testNipServer(t, "ENCRYPTED", "NIPRESPONSE", decryptedXML)
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.BalanceEnquiryRequest{
		SessionID:           utils.GenerateNIPSessionId("999999"),
		TargetAccountNumber: "0035428391",
		ChannelCode:         "1",
	}

	resp, err := svc.ExecuteBalanceEnquiry(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "10000", resp.AvailableBalance)
	assert.Equal(t, "00", resp.ResponseCode)
}

func TestExecuteBalanceEnquiry_FailResponseCode(t *testing.T) {
	decryptedXML := `<BalanceEnquiryResponse><SessionID>sess003</SessionID><ResponseCode>96</ResponseCode></BalanceEnquiryResponse>`

	srv, close := testNipServer(t, "ENCRYPTED", "NIPRESPONSE", decryptedXML)
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.BalanceEnquiryRequest{SessionID: "hey", ChannelCode: "1"}

	_, err := svc.ExecuteBalanceEnquiry(context.Background(), req)
	require.Error(t, err)

	var nipErr *utils.NIPError
	require.True(t, errors.As(err, &nipErr))
	assert.Equal(t, utils.NIPResponseCode("96"), nipErr.Code)
}

func TestExecuteBalanceEnquiry_HttpError(t *testing.T) {
	srv, close := testNIPErrorServer(t, http.StatusInternalServerError, "timeout")
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.BalanceEnquiryRequest{SessionID: "hey", ChannelCode: "1"}

	_, err := svc.ExecuteBalanceEnquiry(context.Background(), req)
	require.Error(t, err)

	var nipErr *utils.NIPError
	assert.True(t, errors.As(err, &nipErr))
}

// --- Helpers ---

func TestGenerateNipSessionId(t *testing.T) {
	id := utils.GenerateNIPSessionId("999999")
	assert.Len(t, id, 30) // 6 (bankCode) + 12 (timestamp) + 12 (random)
	assert.True(t, len(id) == 30)
}

func TestGenerateMandateRef(t *testing.T) {
	ref := utils.GenerateMandateRef("NPP")
	assert.Contains(t, ref, "NPP/")
	parts := splitMandateRef(ref)
	assert.Len(t, parts, 3)
	assert.Equal(t, "NPP", parts[0])
	assert.Len(t, parts[1], 12) // yyMMddHHmmss
	assert.Len(t, parts[2], 8)
}

func TestGenerateMandateRef_DefaultPrefix(t *testing.T) {
	ref := utils.GenerateMandateRef("")
	assert.Contains(t, ref, "RYLPAY/")
}

func TestGetNipBankCode(t *testing.T) {
	svc := newTestNipService("http://localhost")
	assert.Equal(t, "999999", svc.GetNIPBankCode())
}

func TestNIPResponseCode_Description(t *testing.T) {
	assert.Equal(t, "Approved Or Completed Successfully", utils.NIPResponseCode("00").Description())
	assert.Equal(t, "System Malfunction", utils.NIPResponseCode("96").Description())
	assert.Equal(t, "No Sufficient Funds", utils.NIPResponseCode("51").Description())
	assert.Contains(t, utils.NIPResponseCode("XX").Description(), "Unknown")
}

func TestNIPError_Unwrap(t *testing.T) {
	cause := errors.New("root cause")
	err := utils.NewNIPError(utils.NIPResponseCode("99"), cause)
	assert.Equal(t, cause, errors.Unwrap(err))
}

// --- Funds Transfer Debit ---

func TestExecuteFundsTransferDebit_Success(t *testing.T) {
	decryptedXML := `<FTSingleDebitResponse>
		<SessionID>sess004</SessionID>
		<ResponseCode>00</ResponseCode>
		<BeneficiaryAccountName>Jane Doe</BeneficiaryAccountName>
		<DebitAccountNumber>0011223344</DebitAccountNumber>
		<Amount>50000</Amount>
	</FTSingleDebitResponse>`

	srv, close := testNipServer(t, "ENCRYPTED", "NIPRESPONSE", decryptedXML)
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.FTSingleDebitRequest{
		SessionID:                  utils.GenerateNIPSessionId("999999"),
		DestinationInstitutionCode: "999100",
		ChannelCode:                "1",
		Amount:                     "50000",
		DebitAccountNumber:         "0011223344",
		BeneficiaryAccountNumber:   "0099887766",
	}

	resp, err := svc.ExecuteFundsTransferDebit(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "00", resp.ResponseCode)
	assert.Equal(t, "Jane Doe", resp.BeneficiaryAccountName)
}

func TestExecuteFundsTransferDebit_FailResponseCode(t *testing.T) {
	decryptedXML := `<FTSingleDebitResponse><SessionID>sess004</SessionID><ResponseCode>51</ResponseCode></FTSingleDebitResponse>`

	srv, close := testNipServer(t, "ENCRYPTED", "NIPRESPONSE", decryptedXML)
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.FTSingleDebitRequest{SessionID: "hey", ChannelCode: "1"}

	_, err := svc.ExecuteFundsTransferDebit(context.Background(), req)
	require.Error(t, err)

	var nipErr *utils.NIPError
	require.True(t, errors.As(err, &nipErr))
	assert.Equal(t, utils.NIPResponseCode("51"), nipErr.Code)
}

// --- Funds Transfer Credit ---

func TestExecuteFundsTransferCredit_Success(t *testing.T) {
	decryptedXML := `<FTSingleCreditResponse>
		<SessionID>sess005</SessionID>
		<ResponseCode>00</ResponseCode>
		<BeneficiaryAccountName>John Smith</BeneficiaryAccountName>
		<Amount>75000</Amount>
	</FTSingleCreditResponse>`

	srv, close := testNipServer(t, "ENCRYPTED", "NIPRESPONSE", decryptedXML)
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.FTSingleCreditRequest{
		SessionID:                  utils.GenerateNIPSessionId("999999"),
		DestinationInstitutionCode: "999100",
		ChannelCode:                "1",
		Amount:                     "75000",
		BeneficiaryAccountNumber:   "0099887766",
	}

	resp, err := svc.ExecuteFundsTransferCredit(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "00", resp.ResponseCode)
	assert.Equal(t, "John Smith", resp.BeneficiaryAccountName)
}

func TestExecuteFundsTransferCredit_FailResponseCode(t *testing.T) {
	decryptedXML := `<FTSingleCreditResponse><SessionID>sess005</SessionID><ResponseCode>91</ResponseCode></FTSingleCreditResponse>`

	srv, close := testNipServer(t, "ENCRYPTED", "NIPRESPONSE", decryptedXML)
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.FTSingleCreditRequest{SessionID: "hey", ChannelCode: "1"}

	_, err := svc.ExecuteFundsTransferCredit(context.Background(), req)
	require.Error(t, err)

	var nipErr *utils.NIPError
	require.True(t, errors.As(err, &nipErr))
	assert.Equal(t, utils.NIPResponseCode("91"), nipErr.Code)
}

// --- Transaction Status Query ---

func TestExecuteTransactionStatusQuery_Success(t *testing.T) {
	decryptedXML := `<TSQuerySingleResponse>
		<SessionID>sess006</SessionID>
		<ResponseCode>00</ResponseCode>
		<OriginalSessionID>sess001</OriginalSessionID>
		<Amount>50000</Amount>
		<BeneficiaryAccountName>Jane Doe</BeneficiaryAccountName>
	</TSQuerySingleResponse>`

	srv, close := testNipServer(t, "ENCRYPTED", "NIPRESPONSE", decryptedXML)
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.TSQuerySingleRequest{
		SessionID:                  utils.GenerateNIPSessionId("999999"),
		DestinationInstitutionCode: "999100",
		ChannelCode:                "1",
		OriginalSessionID:          "sess001",
	}

	resp, err := svc.ExecuteTransactionStatusQuery(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "00", resp.ResponseCode)
	assert.Equal(t, "sess001", resp.OriginalSessionID)
	assert.Equal(t, "Jane Doe", resp.BeneficiaryAccountName)
}

func TestExecuteTransactionStatusQuery_FailResponseCode(t *testing.T) {
	decryptedXML := `<TSQuerySingleResponse><SessionID>sess006</SessionID><ResponseCode>25</ResponseCode></TSQuerySingleResponse>`

	srv, close := testNipServer(t, "ENCRYPTED", "NIPRESPONSE", decryptedXML)
	defer close()

	svc := newTestNipService(srv.URL)
	req := &models.TSQuerySingleRequest{SessionID: "hey", ChannelCode: "1"}

	_, err := svc.ExecuteTransactionStatusQuery(context.Background(), req)
	require.Error(t, err)

	var nipErr *utils.NIPError
	require.True(t, errors.As(err, &nipErr))
	assert.Equal(t, utils.NIPResponseCode("25"), nipErr.Code)
}

func splitMandateRef(ref string) []string {
	var parts []string
	start := 0
	for i, c := range ref {
		if c == '/' {
			parts = append(parts, ref[start:i])
			start = i + 1
		}
	}
	parts = append(parts, ref[start:])
	return parts
}
