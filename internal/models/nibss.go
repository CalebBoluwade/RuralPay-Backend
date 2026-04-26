package models

import (
	"encoding/xml"
)

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

type UserTransactionMetaData struct {
	DebitAccountName            string `json:"debitAccountName"`
	DebitAccountNumber          string `json:"debitAccountNumber"`
	DebitBankVerificationNumber string `json:"debitBankVerificationNumber"`
	DebitKYCLevel               string `json:"debitKycLevel"`
	DebitMandateCode            string `json:"debitMandateCode"`

	NameEnquiryBeneficiaryAccountName            string `json:"nameEnquiryBeneficiaryAccountName"`
	NameEnquiryBeneficiaryAccountNumber          string `json:"nameEnquiryBeneficiaryAccountNumber"`
	NameEnquiryBeneficiaryBankVerificationNumber string `json:"nameEnquiryBeneficiaryBankVerificationNumber"`
	NameEnquiryBeneficiaryKYCLevel               string `json:"nameEnquiryBeneficiaryKycLevel"`
	NameEnquiryBeneficiaryBankCode               string `json:"nameEnquiryBeneficiaryBankCode"`
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

type IdentificationVerificationResponse struct {
	XMLName     xml.Name `xml:"IdVrfctnRpt" json:"-"`
	Verified    bool     `json:"verified" xml:"Rpt>Vrfctn"`
	AccountName string   `json:"accountName" xml:"Rpt>OrgnlPtyAndAcctId>Pty>Nm"`
}

// NIP SOAP models

type NESingleRequest struct {
	XMLName                    xml.Name `xml:"NESingleRequest" json:"-"`
	SessionID                  string   `xml:"SessionID" json:"sessionId"`
	DestinationInstitutionCode string   `xml:"DestinationInstitutionCode" json:"destinationInstitutionCode"`
	ChannelCode                string   `xml:"ChannelCode" json:"channelCode"`
	AccountNumber              string   `xml:"AccountNumber" json:"accountNumber"`
}

type NESingleResponse struct {
	XMLName                    xml.Name `xml:"return" json:"-"`
	SessionID                  string   `xml:"SessionID" json:"sessionId"`
	DestinationInstitutionCode string   `xml:"DestinationInstitutionCode" json:"destinationInstitutionCode"`
	ChannelCode                string   `xml:"ChannelCode" json:"channelCode"`
	AccountNumber              string   `xml:"AccountNumber" json:"accountNumber"`
	AccountName                string   `xml:"AccountName" json:"accountName"`
	KYCLevel                   string   `xml:"KYCLevel" json:"kycLevel"`
	BankVerificationNumber     string   `xml:"BankVerificationNumber" json:"bankVerificationNumber"`
	ResponseCode               string   `xml:"ResponseCode" json:"responseCode"`
}

type MandateAdviceRequest struct {
	XMLName                           xml.Name `xml:"MandateAdviceRequest" json:"-"`
	SessionID                         string   `xml:"SessionID" json:"sessionId"`
	DestinationInstitutionCode        string   `xml:"DestinationInstitutionCode" json:"destinationInstitutionCode"`
	ChannelCode                       string   `xml:"ChannelCode" json:"channelCode"`
	MandateReferenceNumber            string   `xml:"MandateReferenceNumber" json:"mandateReferenceNumber"`
	Amount                            string   `xml:"Amount" json:"amount"`
	DebitAccountName                  string   `xml:"DebitAccountName" json:"debitAccountName"`
	DebitAccountNumber                string   `xml:"DebitAccountNumber" json:"debitAccountNumber"`
	BeneficiaryAccountName            string   `xml:"BeneficiaryAccountName" json:"beneficiaryAccountName"`
	BeneficiaryAccountNumber          string   `xml:"BeneficiaryAccountNumber" json:"beneficiaryAccountNumber"`
	BeneficiaryKYCLevel               string   `xml:"BeneficiaryKYCLevel" json:"beneficiaryKycLevel"`
	BeneficiaryBankVerificationNumber string   `xml:"BeneficiaryBankVerificationNumber" json:"beneficiaryBankVerificationNumber"`
	DebitBankVerificationNumber       string   `xml:"DebitBankVerificationNumber" json:"debitBankVerificationNumber"`
	DebitKYCLevel                     string   `xml:"DebitKYCLevel" json:"debitKycLevel"`
}

type MandateAdviceResponse struct {
	XMLName                           xml.Name `xml:"return" json:"-"`
	SessionID                         string   `xml:"SessionID" json:"sessionId"`
	DestinationInstitutionCode        string   `xml:"DestinationInstitutionCode" json:"destinationInstitutionCode"`
	ChannelCode                       string   `xml:"ChannelCode" json:"channelCode"`
	MandateReferenceNumber            string   `xml:"MandateReferenceNumber" json:"mandateReferenceNumber"`
	Amount                            string   `xml:"Amount" json:"amount"`
	DebitAccountName                  string   `xml:"DebitAccountName" json:"debitAccountName"`
	DebitAccountNumber                string   `xml:"DebitAccountNumber" json:"debitAccountNumber"`
	DebitBankVerificationNumber       string   `xml:"DebitBankVerificationNumber" json:"debitBankVerificationNumber"`
	DebitKYCLevel                     string   `xml:"DebitKYCLevel" json:"debitKycLevel"`
	BeneficiaryAccountName            string   `xml:"BeneficiaryAccountName" json:"beneficiaryAccountName"`
	BeneficiaryAccountNumber          string   `xml:"BeneficiaryAccountNumber" json:"beneficiaryAccountNumber"`
	BeneficiaryBankVerificationNumber string   `xml:"BeneficiaryBankVerificationNumber" json:"beneficiaryBankVerificationNumber"`
	BeneficiaryKYCLevel               string   `xml:"BeneficiaryKYCLevel" json:"beneficiaryKycLevel"`
	ResponseCode                      string   `xml:"ResponseCode" json:"responseCode"`
}

type BalanceEnquiryRequest struct {
	XMLName                      xml.Name `xml:"BalanceEnquiryRequest" json:"-"`
	SessionID                    string   `xml:"SessionID" json:"sessionId"`
	DestinationInstitutionCode   string   `xml:"DestinationInstitutionCode" json:"destinationInstitutionCode"`
	ChannelCode                  string   `xml:"ChannelCode" json:"channelCode"`
	AuthorizationCode            string   `xml:"AuthorizationCode" json:"authorizationCode"`
	TargetAccountName            string   `xml:"TargetAccountName" json:"targetAccountName"`
	TargetBankVerificationNumber string   `xml:"TargetBankVerificationNumber" json:"targetBankVerificationNumber"`
	TargetAccountNumber          string   `xml:"TargetAccountNumber" json:"targetAccountNumber"`
}

type BalanceEnquiryResponse struct {
	XMLName                      xml.Name `xml:"return" json:"-"`
	SessionID                    string   `xml:"SessionID" json:"sessionId"`
	DestinationInstitutionCode   string   `xml:"DestinationInstitutionCode" json:"destinationInstitutionCode"`
	ChannelCode                  string   `xml:"ChannelCode" json:"channelCode"`
	AuthorizationCode            string   `xml:"AuthorizationCode" json:"authorizationCode"`
	TargetAccountName            string   `xml:"TargetAccountName" json:"targetAccountName"`
	TargetBankVerificationNumber string   `xml:"TargetBankVerificationNumber" json:"targetBankVerificationNumber"`
	TargetAccountNumber          string   `xml:"TargetAccountNumber" json:"targetAccountNumber"`
	AvailableBalance             string   `xml:"AvailableBalance" json:"availableBalance"`
	ResponseCode                 string   `xml:"ResponseCode" json:"responseCode"`
}

type FTSingleDebitRequest struct {
	XMLName                           xml.Name `xml:"FTSingleDebitRequest" json:"-"`
	SessionID                         string   `xml:"SessionID" json:"sessionId"`
	DestinationInstitutionCode        string   `xml:"DestinationInstitutionCode" json:"destinationInstitutionCode"`
	ChannelCode                       string   `xml:"ChannelCode" json:"channelCode"`
	BeneficiaryAccountName            string   `xml:"BeneficiaryAccountName" json:"beneficiaryAccountName"`
	BeneficiaryAccountNumber          string   `xml:"BeneficiaryAccountNumber" json:"beneficiaryAccountNumber"`
	BeneficiaryBankVerificationNumber string   `xml:"BeneficiaryBankVerificationNumber" json:"beneficiaryBankVerificationNumber"`
	BeneficiaryKYCLevel               string   `xml:"BeneficiaryKYCLevel" json:"beneficiaryKycLevel"`
	DebitAccountName                  string   `xml:"DebitAccountName" json:"debitAccountName"`
	DebitAccountNumber                string   `xml:"DebitAccountNumber" json:"debitAccountNumber"`
	DebitBankVerificationNumber       string   `xml:"DebitBankVerificationNumber" json:"debitBankVerificationNumber"`
	DebitKYCLevel                     string   `xml:"DebitKYCLevel" json:"debitKycLevel"`
	MandateReferenceNumber            string   `xml:"MandateReferenceNumber" json:"mandateReferenceNumber"`
	TransactionLocation               string   `xml:"TransactionLocation" json:"transactionLocation"`
	TransactionFee                    string   `xml:"TransactionFee" json:"transactionFee"`
	Narration                         string   `xml:"Narration" json:"narration"`
	PaymentReference                  string   `xml:"PaymentReference" json:"paymentReference"`
	Amount                            string   `xml:"Amount" json:"amount"`
}

type FTSingleDebitResponse struct {
	XMLName                    xml.Name `xml:"return" json:"-"`
	SessionID                  string   `xml:"SessionID" json:"sessionId"`
	DestinationInstitutionCode string   `xml:"DestinationInstitutionCode" json:"destinationInstitutionCode"`
	ChannelCode                string   `xml:"ChannelCode" json:"channelCode"`
	BeneficiaryAccountName     string   `xml:"BeneficiaryAccountName" json:"beneficiaryAccountName"`
	BeneficiaryAccountNumber   string   `xml:"BeneficiaryAccountNumber" json:"beneficiaryAccountNumber"`
	DebitAccountName           string   `xml:"DebitAccountName" json:"debitAccountName"`
	DebitAccountNumber         string   `xml:"DebitAccountNumber" json:"debitAccountNumber"`
	TransactionLocation        string   `xml:"TransactionLocation" json:"transactionLocation"`
	Narration                  string   `xml:"Narration" json:"narration"`
	Amount                     string   `xml:"Amount" json:"amount"`
	ResponseCode               string   `xml:"ResponseCode" json:"responseCode"`
}

type FTSingleCreditRequest struct {
	XMLName                           xml.Name `xml:"FTSingleCreditRequest" json:"-"`
	SessionID                         string   `xml:"SessionID" json:"sessionId"`
	DestinationInstitutionCode        string   `xml:"DestinationInstitutionCode" json:"destinationInstitutionCode"`
	ChannelCode                       string   `xml:"ChannelCode" json:"channelCode"`
	BeneficiaryAccountName            string   `xml:"BeneficiaryAccountName" json:"beneficiaryAccountName"`
	BeneficiaryAccountNumber          string   `xml:"BeneficiaryAccountNumber" json:"beneficiaryAccountNumber"`
	BeneficiaryBankVerificationNumber string   `xml:"BeneficiaryBankVerificationNumber" json:"beneficiaryBankVerificationNumber"`
	BeneficiaryKYCLevel               string   `xml:"BeneficiaryKYCLevel" json:"beneficiaryKycLevel"`
	DebitAccountName                  string   `xml:"DebitAccountName" json:"debitAccountName"`
	DebitAccountNumber                string   `xml:"DebitAccountNumber" json:"debitAccountNumber"`
	DebitBankVerificationNumber       string   `xml:"DebitBankVerificationNumber" json:"debitBankVerificationNumber"`
	DebitKYCLevel                     string   `xml:"DebitKYCLevel" json:"debitKycLevel"`
	TransactionLocation               string   `xml:"TransactionLocation" json:"transactionLocation"`
	Narration                         string   `xml:"Narration" json:"narration"`
	Amount                            string   `xml:"Amount" json:"amount"`
	TransactionFee                    string   `xml:"TransactionFee" json:"transactionFee"`
}

type FTSingleCreditResponse struct {
	XMLName                    xml.Name `xml:"return" json:"-"`
	SessionID                  string   `xml:"SessionID" json:"sessionId"`
	DestinationInstitutionCode string   `xml:"DestinationInstitutionCode" json:"destinationInstitutionCode"`
	ChannelCode                string   `xml:"ChannelCode" json:"channelCode"`
	BeneficiaryAccountName     string   `xml:"BeneficiaryAccountName" json:"beneficiaryAccountName"`
	BeneficiaryAccountNumber   string   `xml:"BeneficiaryAccountNumber" json:"beneficiaryAccountNumber"`
	DebitAccountName           string   `xml:"DebitAccountName" json:"debitAccountName"`
	DebitAccountNumber         string   `xml:"DebitAccountNumber" json:"debitAccountNumber"`
	TransactionLocation        string   `xml:"TransactionLocation" json:"transactionLocation"`
	Narration                  string   `xml:"Narration" json:"narration"`
	Amount                     string   `xml:"Amount" json:"amount"`
	ResponseCode               string   `xml:"ResponseCode" json:"responseCode"`
}

type TSQuerySingleRequest struct {
	XMLName                    xml.Name `xml:"TSQuerySingleRequest" json:"-"`
	SessionID                  string   `xml:"SessionID" json:"sessionId"`
	DestinationInstitutionCode string   `xml:"DestinationInstitutionCode" json:"destinationInstitutionCode"`
	ChannelCode                string   `xml:"ChannelCode" json:"channelCode"`
	OriginalSessionID          string   `xml:"OriginalSessionID" json:"originalSessionId"`
}

type TSQuerySingleResponse struct {
	XMLName                    xml.Name `xml:"return" json:"-"`
	SessionID                  string   `xml:"SessionID" json:"sessionId"`
	DestinationInstitutionCode string   `xml:"DestinationInstitutionCode" json:"destinationInstitutionCode"`
	ChannelCode                string   `xml:"ChannelCode" json:"channelCode"`
	OriginalSessionID          string   `xml:"OriginalSessionID" json:"originalSessionId"`
	Amount                     string   `xml:"Amount" json:"amount"`
	BeneficiaryAccountName     string   `xml:"BeneficiaryAccountName" json:"beneficiaryAccountName"`
	BeneficiaryAccountNumber   string   `xml:"BeneficiaryAccountNumber" json:"beneficiaryAccountNumber"`
	DebitAccountName           string   `xml:"DebitAccountName" json:"debitAccountName"`
	DebitAccountNumber         string   `xml:"DebitAccountNumber" json:"debitAccountNumber"`
	ResponseCode               string   `xml:"ResponseCode" json:"responseCode"`
}
