package services

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/moov-io/iso8583"
	"github.com/moov-io/iso8583/encoding"
	"github.com/moov-io/iso8583/field"
	"github.com/moov-io/iso8583/padding"
	"github.com/moov-io/iso8583/prefix"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/spf13/viper"
)

// Define field IDs for clarity
const (
	FieldMTI                      = 0
	FieldBitmap                   = 1
	FieldPrimaryAccountNumber     = 2
	FieldProcessingCode           = 3
	FieldAmountTransaction        = 4
	FieldTransmissionDateTime     = 7
	FieldSystemsTraceAuditNumber  = 11
	FieldTimeLocalTransaction     = 12
	FieldDateLocalTransaction     = 13
	FieldDateExpiration           = 14
	FieldDateSettlement           = 15
	FieldDateConversion           = 16
	FieldMerchantType             = 18
	FieldPOSEntryMode             = 22
	FieldCardSequenceNumber       = 23
	FieldPOSConditionCode         = 25
	FieldPOSPINCaptureCode        = 26
	FieldAmountTransactionFee     = 28
	FieldAcquiringInstIDCode      = 32
	FieldTrack2Data               = 35
	FieldRetrievalReferenceNumber = 37
	FieldServiceRestrictionCode   = 40
	FieldCardAcceptorTerminalID   = 41
	FieldCardAcceptorIDCode       = 42
	FieldCardAcceptorNameLocation = 43
	FieldCurrencyCodeTransaction  = 49
	FieldICCRelatedData           = 55
	FieldAdditionalPrivateData    = 120
	FieldPOSDataCode              = 123
	FieldSecondaryMessageHash     = 128
)

type ISO8583Service struct {
	db  *sql.DB
	HSM hsm.HSMInterface

	merchantID             string
	terminalID             string
	acquiringInstitutionID string
	//cardAcceptorID         string
	merchantName string
}

func NewISO8583Service(db *sql.DB, hsmInstance hsm.HSMInterface) *ISO8583Service {
	return &ISO8583Service{
		db:  db,
		HSM: hsmInstance,

		// Fields 41, 42, 43: Terminal / Merchant identifiers from config
		terminalID:             viper.GetString("iso8583.terminal_id"),
		acquiringInstitutionID: viper.GetString("iso8583.acquiring_institution_id"),
		merchantID:             viper.GetString("iso8583.card_acceptor_id"),
		merchantName:           fmt.Sprintf("%-40s", viper.GetString("iso8583.card_acceptor_name")),
		//cardAcceptorID:         viper.GetString("iso8583.card_acceptor_id"),
	}
}

// processingCode derives the ISO 8583 Field 3 processing code from txType.
// Format: TTFFCC — TT=transaction type, FF=from account, CC=to account.
// 00=purchase, 20=refund, 01=cash withdrawal.
func processingCode(txType string) string {
	switch txType {
	case "CREDIT", "REFUND":
		return "200000"
	case "WITHDRAWAL":
		return "010000"
	default: // DEBIT, PURCHASE
		return "000000"
	}
}

func (s *ISO8583Service) BuildISO0800Message() (*iso8583.Message, error) {

	msg := iso8583.NewMessage(s.CreateISO8583_0800_MessageSpec1987())

	// MTI (0800 = network management request)
	msg.MTI("0800")

	_ = msg.Field(FieldProcessingCode, "000000")

	// Transmission Date & Time (MMDDhhmmss)
	_ = msg.Field(FieldTransmissionDateTime, "0312083643")

	// STAN
	_ = msg.Field(FieldSystemsTraceAuditNumber, "000217")

	_ = msg.Field(FieldTimeLocalTransaction, "083643")

	_ = msg.Field(FieldDateLocalTransaction, "0312")

	// Terminal ID
	_ = msg.Field(FieldCardAcceptorTerminalID, "2011E169")

	return msg, nil
}

func (s *ISO8583Service) BuildISO0200MessageTest() *iso8583.Message {
	// Create a new message with version 1987 (most common for ISO8583)
	message := iso8583.NewMessage(s.CreateISO8583MessageSpec1987())

	// Mandatory fields (according to spec)
	message.MTI("0200")
	message.Field(FieldMTI, "0200")
	message.Field(FieldPrimaryAccountNumber, "4187451802934507")
	message.Field(FieldProcessingCode, "000000")
	message.Field(FieldAmountTransaction, "000000004000")  // 40.00 in lowest denomination
	message.Field(FieldTransmissionDateTime, "0312083643") // MMDDhhmmss
	message.Field(FieldSystemsTraceAuditNumber, "000217")
	message.Field(FieldTimeLocalTransaction, "083643")
	message.Field(FieldDateLocalTransaction, "0312")
	message.Field(FieldDateExpiration, "2506")
	message.Field(FieldDateSettlement, "0312") // MMDD
	message.Field(FieldDateConversion, "0312") // MMDD
	message.Field(FieldMerchantType, "1234")
	message.Field(FieldPOSEntryMode, "051")
	message.Field(FieldCardSequenceNumber, "001")
	message.Field(FieldPOSConditionCode, "00")
	message.Field(FieldPOSPINCaptureCode, "06")
	message.Field(FieldAmountTransactionFee, "C00000000")
	message.Field(FieldAcquiringInstIDCode, "418745")
	message.Field(FieldTrack2Data, "5399237066431798=1209")
	message.Field(FieldRetrievalReferenceNumber, "007941000217")
	message.Field(FieldServiceRestrictionCode, "226")
	message.Field(FieldCardAcceptorTerminalID, "2011E169")
	message.Field(FieldCardAcceptorIDCode, "3662DA447419008")
	message.Field(FieldCardAcceptorNameLocation, "PAYZEEP                30 TOWN PLANNLANG")
	message.Field(FieldCurrencyCodeTransaction, "566")
	message.Field(FieldICCRelatedData, "9F03060000000000009F26084F2282A42BFB77719F2701809F100706010A03A4A0049F37044586EB309F36020B0F950508C00080009A03260312500a566973612044656269749C01009F02060000000040005F2A020566820238009F1A0205669F3303E0F8C89F34034103029F1E0850333930393030308407A00000000310109B02E8009F090201409F410400000002")
	message.Field(FieldAdditionalPrivateData, "010806.582240209003.56566") // Geo coordinates
	message.Field(FieldPOSDataCode, "510101511344101")

	// Compute and set secondary hash (field 128) over the message
	// _, _ := computeSecondaryHash(message)
	message.Field(FieldSecondaryMessageHash, "0A9D1397683307E32D0FD4F5D55E48D5E2B071FEC746A2A2A32E647816F95EDD")

	LogMessageAsXML(message, "incoming")

	return message
}

func (s *ISO8583Service) BuildISO0200Message(cardReq *models.CardPaymentRequest) (*iso8583.Message, error) {
	slog.Info("ISO8583.build0200.start", "tx_id", cardReq.TransactionID)

	newISO8583Message := iso8583.NewMessage(s.CreateISO8583MessageSpec1987())
	newISO8583Message.MTI("0200")

	pan, err := s.HSM.DecryptPII(cardReq.CardInfo.EncryptedPAN)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt PAN: %w", err)
	}

	now := time.Now().UTC()
	stan := fmt.Sprintf("%06d", cardReq.CardInfo.ATC)
	localTime := now.Format("150405")
	localDate := now.Format("0102")
	expiry := expiryToYYMM(cardReq.CardInfo.ExpiryDate)

	currencyCode := cardReq.CardInfo.CurrencyCode
	if currencyCode == "" {
		currencyCode = "566"
	}
	countryCode := cardReq.CardInfo.CountryCode
	if countryCode == "" {
		countryCode = "566"
	}

	// Core financial fields
	_ = newISO8583Message.Field(2, pan)
	_ = newISO8583Message.Field(3, processingCode(cardReq.TxType))
	_ = newISO8583Message.Field(4, fmt.Sprintf("%012d", cardReq.Amount))
	_ = newISO8583Message.Field(7, now.Format("0102150405"))
	_ = newISO8583Message.Field(11, stan)
	_ = newISO8583Message.Field(12, localTime)
	_ = newISO8583Message.Field(13, localDate)
	_ = newISO8583Message.Field(14, expiry)
	_ = newISO8583Message.Field(15, localDate)
	_ = newISO8583Message.Field(18, "5011")
	_ = newISO8583Message.Field(22, "051")
	_ = newISO8583Message.Field(25, "00")
	_ = newISO8583Message.Field(26, "04")
	_ = newISO8583Message.Field(28, "D00000000")
	_ = newISO8583Message.Field(32, s.acquiringInstitutionID)
	_ = newISO8583Message.Field(33, viper.GetString("iso8583.forwarding_institution_id"))
	_ = newISO8583Message.Field(35, fmt.Sprintf("%s=%s", pan, expiry))
	_ = newISO8583Message.Field(37, fmt.Sprintf("%s%s%06d", localDate, localTime[:2], cardReq.CardInfo.ATC))
	_ = newISO8583Message.Field(40, "601")
	_ = newISO8583Message.Field(41, s.terminalID)
	_ = newISO8583Message.Field(42, s.merchantID)
	_ = newISO8583Message.Field(43, fmt.Sprintf("%-40s", viper.GetString("iso8583.card_acceptor_name")))
	_ = newISO8583Message.Field(49, currencyCode)

	// F59: Echo data — zero-padded amount with institution prefix
	_ = newISO8583Message.Field(59, fmt.Sprintf("%018d", cardReq.Amount))

	// F90: Original Data Elements — MTI(4)+STAN(6)+DateTime(10)+AcqInst(11)+FwdInst(11) = 42
	_ = newISO8583Message.Field(90, fmt.Sprintf("0200%s%s%011d%011d",
		stan,
		now.Format("0102150405"),
		parseInstitutionID(s.acquiringInstitutionID),
		parseInstitutionID(viper.GetString("iso8583.forwarding_institution_id")),
	))

	// F100: Receiving Institution ID
	_ = newISO8583Message.Field(100, viper.GetString("iso8583.receiving_institution_id"))

	// F102/F103: Account identifiers
	_ = newISO8583Message.Field(102, fmt.Sprintf("%09d", cardReq.Amount))
	_ = newISO8583Message.Field(103, fmt.Sprintf("%09d", cardReq.Amount))

	// F123: POS Data Code (15 chars)
	_ = newISO8583Message.Field(123, "511201513344002")

	// F127: Private use — sub-packager with ICC XML in sub-field 25
	// Structure: bitmap(16 ASCII hex) + sub-field-25-length(4 digits) + XML
	// Sub-bitmap 0000008000000000 = only sub-field 25 set
	// if cardReq.CardInfo.IssuerAppData != "" && isValidEMVHex(cardReq.CardInfo.IssuerAppData) {
	// 	iccXML := buildICCXML(cardReq, countryCode, currencyCode)
	// 	f127Value := fmt.Sprintf("0000008000000000%04d%s", len(iccXML), iccXML)
	// 	_ = newISO8583Message.Field(127, f127Value)
	// }

	// F128: MAC (64-char hex string)
	_ = newISO8583Message.Field(128, "088ecae48e4f2acef0c33aeb531a37534f0e58d6eac66d793e38ea2415fa3e12")

	slog.Info("ISO8583.build0200.success", "tx_id", cardReq.TransactionID)
	return newISO8583Message, nil
}

// computeSecondaryHash calculates SHA-256 of the binary message without field 128,
// then returns it as a hex string (as required by spec, ANSI 64 hex).
func computeSecondaryHash(msg *iso8583.Message) (string, error) {
	// Clone the message and remove field 128 if present
	clone, err := msg.Clone()
	if err != nil {
		return "", fmt.Errorf("cloning message for hash: %w", err)
	}
	// Remove field 128 by unsetting it completely
	clone.UnsetField(FieldSecondaryMessageHash)

	// Pack the clone to binary
	packed, err := clone.Pack()
	if err != nil {
		return "", fmt.Errorf("packing for hash: %w", err)
	}
	// SHA-256
	sum := sha256.Sum256(packed)
	return hex.EncodeToString(sum[:]), nil
}

// buildICCXML constructs the ICC XML payload for F127 sub-field 25,
// matching the jPOS test message format exactly.
// func buildICCXML(cardReq *models.CardPaymentRequest, countryCode, currencyCode string) string {
// 	return fmt.Sprintf(
// 		`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+
// 			`<IccData><IccRequest>`+
// 			`<AmountAuthorized>%012d</AmountAuthorized>`+
// 			`<AmountOther>000000000000</AmountOther>`+
// 			`<ApplicationInterchangeProfile>5C00</ApplicationInterchangeProfile>`+
// 			`<ApplicationTransactionCounter>%04X</ApplicationTransactionCounter>`+
// 			`<Cryptogram>%s</Cryptogram>`+
// 			`<CryptogramInformationData>80</CryptogramInformationData>`+
// 			`<CvmResults>%s</CvmResults>`+
// 			`<IssuerApplicationData>%s</IssuerApplicationData>`+
// 			`<TerminalCountryCode>%s</TerminalCountryCode>`+
// 			`<TerminalVerificationResult>0000008000</TerminalVerificationResult>`+
// 			`<TransactionCurrencyCode>%s</TransactionCurrencyCode>`+
// 			`<TransactionType>00</TransactionType>`+
// 			`</IccRequest></IccData>`,
// 		cardReq.Amount,
// 		cardReq.CardInfo.ATC,
// 		cardReq.CardInfo.Cryptogram,
// 		cardReq.CardInfo.CVR,
// 		cardReq.CardInfo.IssuerAppData,
// 		countryCode,
// 		currencyCode,
// 	)
// }

// isValidEMVHex returns true if s is a non-empty hex string of valid EMV tag length (max 510 hex chars = 255 bytes).
func isValidEMVHex(s string) bool {
	if len(s) == 0 || len(s) > 510 || len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// parseInstitutionID converts a numeric institution ID string to int64 for zero-padded formatting.
func parseInstitutionID(s string) int64 {
	var n int64
	fmt.Sscanf(s, "%d", &n)
	return n
}

// expiryToYYMM converts MM/YY expiry format to YYMM as required by ISO 8583 Field 14.
func expiryToYYMM(expiry string) string {
	if len(expiry) != 5 || expiry[2] != '/' {
		return "0000"
	}
	return expiry[3:5] + expiry[0:2] // YY + MM
}

// func (s *ISO8583Service) ProcessMessage(ctx context.Context, rawMsg []byte) (*iso8583.Message, error) {
// 	msg := iso8583.NewMessage(s.spec)
// 	if err := msg.Unpack(rawMsg); err != nil {
// 		return nil, fmt.Errorf("failed to unpack message: %w", err)
// 	}

// 	mti, _ := msg.GetMTI()
// 	slog.Info("ISO8583.process.mti", "mti", mti)

// 	var respMsg *iso8583.Message
// 	var err error

// 	switch mti {
// 	case "0100": // Authorization Request
// 		respMsg, err = s.processAuthorization(ctx, msg)
// 	case "0200": // Financial Request
// 		respMsg, err = s.processFinancial(ctx, msg)
// 	case "0400": // Reversal Request
// 		respMsg, err = s.processReversal(ctx, msg)
// 	default:
// 		return nil, fmt.Errorf("unsupported MTI: %s", mti)
// 	}

// 	if err != nil {
// 		return nil, err
// 	}

// 	return respMsg, nil
// }

func createISO8583MessageSpec() *iso8583.MessageSpec {
	return &iso8583.MessageSpec{
		Fields: map[int]field.Field{
			0: field.NewString(&field.Spec{
				Length:      4,
				Description: "Message Type Indicator",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			1: field.NewBitmap(&field.Spec{
				Description: "Bitmap",
				Enc:         encoding.Binary,
				Pref:        prefix.Binary.Fixed,
			}),
			2: field.NewString(&field.Spec{
				Length:      19,
				Description: "Primary Account Number",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.LL,
			}),
			3: field.NewString(&field.Spec{
				Length:      6,
				Description: "Processing Code",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			4: field.NewString(&field.Spec{
				Length:      12,
				Description: "Amount, Transaction",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			11: field.NewString(&field.Spec{
				Length:      6,
				Description: "System Trace Audit Number",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			12: field.NewString(&field.Spec{
				Length:      6,
				Description: "Local Transaction Time",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			13: field.NewString(&field.Spec{
				Length:      4,
				Description: "Local Transaction Date",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			14: field.NewString(&field.Spec{
				Length:      4,
				Description: "Expiration Date",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			15: field.NewString(&field.Spec{
				Length:      4,
				Description: "Settlement Date",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			18: field.NewString(&field.Spec{
				Length:      4,
				Description: "Merchant Category Code",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			22: field.NewString(&field.Spec{
				Length:      3,
				Description: "Point of Service Entry Mode",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			25: field.NewString(&field.Spec{
				Length:      2,
				Description: "Point of Service Condition Code",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			26: field.NewString(&field.Spec{
				Length:      2,
				Description: "Point of Service PIN Capture Code",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			28: field.NewString(&field.Spec{
				Length:      9,
				Description: "Transaction Fee Amount",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			32: field.NewString(&field.Spec{
				Length:      11,
				Description: "Acquiring Institution ID Code",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.LL,
			}),
			33: field.NewString(&field.Spec{
				Length:      11,
				Description: "Forwarding Institution ID Code",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.LL,
			}),
			35: field.NewString(&field.Spec{
				Length:      37,
				Description: "Track 2 Data",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.LL,
			}),
			37: field.NewString(&field.Spec{
				Length:      12,
				Description: "Retrieval Reference Number",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			40: field.NewString(&field.Spec{
				Length:      3,
				Description: "Network Management Information Code",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			41: field.NewString(&field.Spec{
				Length:      8,
				Description: "Card Acceptor Terminal ID",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			42: field.NewString(&field.Spec{
				Length:      15,
				Description: "Card Acceptor ID Code",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			43: field.NewString(&field.Spec{
				Length:      40,
				Description: "Card Acceptor Name/Location",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			38: field.NewString(&field.Spec{
				Length:      6,
				Description: "Authorization Code",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			39: field.NewString(&field.Spec{
				Length:      2,
				Description: "Response Code",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
			55: field.NewString(&field.Spec{
				Length:      999,
				Description: "ICC Data",
				Enc:         encoding.Binary,
				Pref:        prefix.Binary.LLL,
			}),
			90: field.NewString(&field.Spec{
				Length:      42,
				Description: "Original Data Elements",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.Fixed,
			}),
		},
	}
}

func (s *ISO8583Service) CreateISO8583_0800_MessageSpec1987() *iso8583.MessageSpec {
	spec := &iso8583.MessageSpec{
		Name: "NUS 0800 Authorization Request",
		// ISO 8583:1987 uses ASCII bitmap, no length header
		Fields: map[int]field.Field{
			FieldMTI: field.NewString(&field.Spec{
				Length: 4,
				Enc:    encoding.ASCII,
				Pref:   prefix.ASCII.Fixed,
				Pad:    padding.None,
			}),
			FieldBitmap: field.NewBitmap(&field.Spec{
				Length: 8, // 8 bytes = 64 bits for primary bitmap
				Enc:    encoding.Binary,
				Pref:   prefix.Binary.Fixed,
				Pad:    padding.None,
			}),
			FieldProcessingCode:          field.NewString(&field.Spec{Length: 6, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldTransmissionDateTime:    field.NewString(&field.Spec{Length: 10, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldSystemsTraceAuditNumber: field.NewString(&field.Spec{Length: 6, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldTimeLocalTransaction:    field.NewString(&field.Spec{Length: 6, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldDateLocalTransaction:    field.NewString(&field.Spec{Length: 4, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldCardAcceptorTerminalID:  field.NewString(&field.Spec{Length: 8, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
		},
	}

	return spec
}

func (s *ISO8583Service) CreateISO8583MessageSpec1987() *iso8583.MessageSpec {
	spec := &iso8583.MessageSpec{
		Name: "NUS 0200 Financial Request",
		// ISO 8583:1987 uses ASCII bitmap, no length header
		Fields: map[int]field.Field{
			FieldMTI: field.NewString(&field.Spec{
				Length: 4,
				Enc:    encoding.BCD, // MTI should be BCD encoded
				Pref:   prefix.ASCII.Fixed,
				Pad:    padding.None,
			}),
			FieldBitmap: field.NewBitmap(&field.Spec{
				Length: 8, // 8 bytes = 64 bits for primary bitmap
				Enc:    encoding.Binary,
				Pref:   prefix.Binary.Fixed,
				Pad:    padding.None,
			}),

			// LLVAR fields
			FieldPrimaryAccountNumber: field.NewString(&field.Spec{
				Length: 19,
				Enc:    encoding.ASCII,
				Pref:   prefix.ASCII.LL,
				Pad:    padding.None,
			}),
			FieldAcquiringInstIDCode: field.NewString(&field.Spec{
				Length: 11,
				Enc:    encoding.ASCII,
				Pref:   prefix.ASCII.LL,
				Pad:    padding.None,
			}),
			FieldTrack2Data: field.NewString(&field.Spec{
				Length: 37,
				Enc:    encoding.ASCII,
				Pref:   prefix.ASCII.LL,
				Pad:    padding.None,
			}),

			// LLLVAR fields
			FieldICCRelatedData: field.NewString(&field.Spec{
				Length: 510,
				Enc:    encoding.ASCII,
				Pref:   prefix.ASCII.LLL,
				Pad:    padding.None,
			}),
			FieldAdditionalPrivateData: field.NewString(&field.Spec{
				Length: 999,
				Enc:    encoding.ASCII,
				Pref:   prefix.ASCII.LLL,
				Pad:    padding.None,
			}),
			FieldPOSDataCode: field.NewString(&field.Spec{
				Length: 15,
				Enc:    encoding.ASCII,
				Pref:   prefix.ASCII.LLL,
				Pad:    padding.None,
			}),

			// Fixed-length fields (ASCII)
			FieldProcessingCode:           field.NewString(&field.Spec{Length: 6, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldAmountTransaction:        field.NewString(&field.Spec{Length: 12, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldTransmissionDateTime:     field.NewString(&field.Spec{Length: 10, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldSystemsTraceAuditNumber:  field.NewString(&field.Spec{Length: 6, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldTimeLocalTransaction:     field.NewString(&field.Spec{Length: 6, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldDateLocalTransaction:     field.NewString(&field.Spec{Length: 4, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldDateExpiration:           field.NewString(&field.Spec{Length: 4, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldDateSettlement:           field.NewString(&field.Spec{Length: 4, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldDateConversion:           field.NewString(&field.Spec{Length: 4, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldMerchantType:             field.NewString(&field.Spec{Length: 4, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldPOSEntryMode:             field.NewString(&field.Spec{Length: 3, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldCardSequenceNumber:       field.NewString(&field.Spec{Length: 3, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldPOSConditionCode:         field.NewString(&field.Spec{Length: 2, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldPOSPINCaptureCode:        field.NewString(&field.Spec{Length: 2, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldAmountTransactionFee:     field.NewString(&field.Spec{Length: 9, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldRetrievalReferenceNumber: field.NewString(&field.Spec{Length: 12, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldServiceRestrictionCode:   field.NewString(&field.Spec{Length: 3, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldCardAcceptorTerminalID:   field.NewString(&field.Spec{Length: 8, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldCardAcceptorIDCode:       field.NewString(&field.Spec{Length: 15, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldCardAcceptorNameLocation: field.NewString(&field.Spec{Length: 40, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldCurrencyCodeTransaction:  field.NewString(&field.Spec{Length: 3, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
			FieldSecondaryMessageHash:     field.NewString(&field.Spec{Length: 64, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: padding.None}),
		},
	}
	return spec
}

// LogMessageAsXML logs the ISO8583 message in jPOS-style XML format.
func LogMessageAsXML(msg *iso8583.Message, direction string) {
	var builder strings.Builder
	builder.WriteString(`<isomsg direction="`)
	builder.WriteString(direction)
	builder.WriteString(`">`)

	// MTI as field 0
	builder.WriteString(`\n  <field id="0" value="`)

	MTI, _ := msg.GetMTI()
	builder.WriteString(xmlEscape(MTI))
	builder.WriteString(`"/>`) // MTI: Message Type Indicator

	// Field descriptions for better readability
	fieldDescriptions := map[int]string{
		1:   "Primary Bitmap",
		2:   "Primary Account Number (PAN)",
		3:   "Processing Code",
		4:   "Transaction Amount",
		7:   "Transmission Date/Time",
		11:  "Systems Trace Audit Number",
		12:  "Local Transaction Time",
		13:  "Local Transaction Date",
		14:  "Expiration Date",
		15:  "Settlement Date",
		16:  "Conversion Date",
		18:  "Merchant Category Code",
		22:  "POS Entry Mode",
		23:  "Card Sequence Number",
		25:  "POS Condition Code",
		26:  "POS PIN Capture Code",
		28:  "Transaction Fee Amount",
		32:  "Acquiring Institution ID",
		35:  "Track 2 Data",
		37:  "Retrieval Reference Number",
		40:  "Service Restriction Code",
		41:  "Card Acceptor Terminal ID",
		42:  "Card Acceptor ID Code",
		43:  "Card Acceptor Name/Location",
		49:  "Currency Code",
		55:  "ICC/EMV Data",
		120: "Additional Private Data",
		123: "POS Data Code",
		128: "Message Authentication Code (MAC)",
	}

	// Iterate through all fields in order
	for id := 1; id <= 128; id++ {
		val, err := msg.GetString(id)
		if err != nil || val == "" {
			continue
		}

		// Format field with proper spacing and description
		builder.WriteString(`\n  <field id="`)
		builder.WriteString(fmt.Sprintf("%d", id))
		builder.WriteString(`" value="`)
		builder.WriteString(xmlEscape(val))
		builder.WriteString(`"/>`)

		// Add description comment if available
		if desc, exists := fieldDescriptions[id]; exists {
			builder.WriteString(fmt.Sprintf(" <!-- %s -->", desc))
		}
	}
	builder.WriteString("\n</isomsg>")

	slog.Info("ISO8583 Message", "direction", direction, "xml", builder.String())
}

// xmlEscape escapes special XML characters.
func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}
