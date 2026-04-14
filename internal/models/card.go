package models

import (
	"encoding/xml"
	"time"
)

// Field represents a single ISO 8583 field in the XML payload.
type Field struct {
	ID    string `xml:"id,attr"`
	Value string `xml:"value,attr"`
}

// IsoMsg represents the raw XML payload from the POS.
type IsoMsg struct {
	XMLName   xml.Name `xml:"isomsg"`
	Direction string   `xml:"direction,attr"`
	Fields    []Field  `xml:"field"`
}

type BINResponse struct {
	BIN            string `json:"bin"`
	Scheme         string `json:"scheme"`     // Visa, Mastercard, Verve
	IssuerBank     string `json:"issuerBank"` // GTBank, Zenith, etc.
	IssuerBankLogo string `json:"issuerBankLogo,omitempty"`
	Type           string `json:"type"`     // Debit, Credit
	Country        string `json:"country"`  // NG, US
	Currency       string `json:"currency"` // NGN, USD
	Source         string `json:"source"`   // "internal" or "external"
}

type CardInfo struct {
	EncryptedPAN  string `json:"PAN"`
	EncryptedPIN  string `json:"PIN"`
	ExpiryDate    string `json:"expiryDate"`
	ATC           int    `json:"ATC"`
	CVR           string `json:"CVR"`
	Cryptogram    string `json:"cryptogram"`    // EMV tag 9F26 — Application Cryptogram
	IssuerAppData string `json:"issuerAppData"` // EMV tag 9F10 — Issuer Application Data
	CountryCode   string `json:"countryCode"`
	CurrencyCode  string `json:"currencyCode"`
}

type CardPaymentRequest struct {
	TransactionID   string      `json:"transactionId"`
	TransactionDate int64       `json:"transactionDate"`
	MerchantID      int         `json:"merchantId" validate:"required,gt=0"`
	Amount          int64       `json:"amount" validate:"required,gt=0"`
	PaymentMode     PaymentMode `json:"paymentMode"`
	CardInfo        CardInfo    `json:"cardInfo"`
	TxType          string      `json:"txType"`
	Location        *Location   `json:"location,omitempty"`
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

type AuthorizationRequest struct {
	PAN              string
	ProcessingCode   string
	Amount           int64
	TransmissionTime time.Time
	STAN             string
	MerchantType     string
	AcquirerID       string
	ForwardingID     string
	Track2Data       string
	RRN              string
	TerminalID       string
	MerchantID       string
	AdditionalData   string
}

type AuthorizationResponse struct {
	ResponseCode      string
	AuthorizationCode string
	TransactionID     string
	STAN              string
	RRN               string
	ResponseMessage   string
	Timestamp         time.Time
}

type FinancialRequest struct {
	PAN               string
	ProcessingCode    string
	Amount            int64
	TransmissionTime  time.Time
	STAN              string
	MerchantType      string
	AcquirerID        string
	RRN               string
	AuthorizationCode string
	TerminalID        string
	MerchantID        string
	AdditionalData    string
}

type FinancialResponse struct {
	ResponseCode    string
	TransactionID   string
	STAN            string
	RRN             string
	ResponseMessage string
	Timestamp       time.Time
}
