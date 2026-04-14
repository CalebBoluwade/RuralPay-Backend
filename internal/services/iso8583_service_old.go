package services

// import (
// 	"context"
// 	"crypto/rand"
// 	"database/sql"
// 	"errors"
// 	"fmt"
// 	"log/slog"
// 	"time"

// 	"github.com/moov-io/iso8583"
// 	"github.com/moov-io/iso8583/encoding"
// 	"github.com/moov-io/iso8583/field"
// 	"github.com/moov-io/iso8583/prefix"
// 	"github.com/ruralpay/backend/internal/hsm"
// 	"github.com/ruralpay/backend/internal/models"
// 	"github.com/ruralpay/backend/internal/utils"
// 	"github.com/spf13/viper"
// )

// type ISO8583Service struct {
// 	db   *sql.DB
// 	HSM  hsm.HSMInterface
// 	spec *iso8583.MessageSpec

// 	merchantID             string
// 	terminalID             string
// 	acquiringInstitutionID string
// 	//cardAcceptorID         string
// 	merchantName string
// }

// func NewISO8583Service(db *sql.DB, hsmInstance hsm.HSMInterface) *ISO8583Service {
// 	return &ISO8583Service{
// 		db:   db,
// 		HSM:  hsmInstance,
// 		spec: createISO8583Spec(),

// 		// Fields 41, 42, 43: Terminal / Merchant identifiers from config
// 		terminalID:             viper.GetString("iso8583.terminal_id"),
// 		acquiringInstitutionID: viper.GetString("iso8583.acquiring_institution_id"),
// 		merchantID:             viper.GetString("iso8583.card_acceptor_id"),
// 		merchantName:           fmt.Sprintf("%-40s", viper.GetString("iso8583.card_acceptor_name")),
// 		//cardAcceptorID:         viper.GetString("iso8583.card_acceptor_id"),
// 	}
// }

// // processingCode derives the ISO 8583 Field 3 processing code from txType.
// // Format: TTFFCC — TT=transaction type, FF=from account, CC=to account.
// // 00=purchase, 20=refund, 01=cash withdrawal.
// func processingCode(txType string) string {
// 	switch txType {
// 	case "CREDIT", "REFUND":
// 		return "200000"
// 	case "WITHDRAWAL":
// 		return "010000"
// 	default: // DEBIT, PURCHASE
// 		return "000000"
// 	}
// }

// func (s *ISO8583Service) BuildISO0800Message() (*iso8583.Message, error) {
// 	now := time.Now().UTC()
// 	stan := fmt.Sprintf("%06d", now.Unix()%1000000)

// 	msg := iso8583.NewMessage(nibssSpec)

// 	// MTI (0800 = network management request)
// 	msg.MTI("0800")
// 	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 0, "value", "0800")

// 	_ = msg.Field(3, "000000")
// 	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 3, "value", "000000")

// 	// Transmission Date & Time (MMDDhhmmss)
// 	_ = msg.Field(7, now.Format("0102150405"))
// 	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 7, "value", now.Format("0102150405"))

// 	// STAN
// 	_ = msg.Field(11, stan)
// 	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 11, "value", stan)

// 	// Acquirer ID
// 	_ = msg.Field(32, s.acquiringInstitutionID)
// 	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 32, "value", s.acquiringInstitutionID)

// 	// Terminal ID
// 	_ = msg.Field(41, s.terminalID)
// 	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 41, "value", s.terminalID)

// 	_ = msg.Field(42, s.merchantID)
// 	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 42, "value", s.merchantID)

// 	// Network Management Code (001 = sign-on / key exchange)
// 	_ = msg.Field(70, "301")
// 	slog.Debug("ISO8583.PerformKeyExchange.Build", "field", 70, "value", "301")

// 	return msg, nil
// }

// func (s *ISO8583Service) BuildISO0200Message(cardReq *models.CardPaymentRequest) (*iso8583.Message, error) {
// 	slog.Info("ISO8583.build0200.start", "tx_id", cardReq.TransactionID)

// 	msg := iso8583.NewMessage(nibssSpec)
// 	msg.MTI("0200")

// 	pan, err := s.HSM.DecryptPII(cardReq.CardInfo.EncryptedPAN)
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to decrypt PAN: %w", err)
// 	}

// 	now := time.Now().UTC()
// 	stan := fmt.Sprintf("%06d", cardReq.CardInfo.ATC)
// 	localTime := now.Format("150405")
// 	localDate := now.Format("0102")
// 	expiry := expiryToYYMM(cardReq.CardInfo.ExpiryDate)

// 	currencyCode := cardReq.CardInfo.CurrencyCode
// 	if currencyCode == "" {
// 		currencyCode = "566"
// 	}
// 	countryCode := cardReq.CardInfo.CountryCode
// 	if countryCode == "" {
// 		countryCode = "566"
// 	}

// 	// Core financial fields
// 	_ = msg.Field(2, pan)
// 	_ = msg.Field(3, processingCode(cardReq.TxType))
// 	_ = msg.Field(4, fmt.Sprintf("%012d", cardReq.Amount))
// 	_ = msg.Field(7, now.Format("0102150405"))
// 	_ = msg.Field(11, stan)
// 	_ = msg.Field(12, localTime)
// 	_ = msg.Field(13, localDate)
// 	_ = msg.Field(14, expiry)
// 	_ = msg.Field(15, localDate)
// 	_ = msg.Field(18, "5011")
// 	_ = msg.Field(22, "051")
// 	_ = msg.Field(25, "00")
// 	_ = msg.Field(26, "04")
// 	_ = msg.Field(28, "D00000000")
// 	_ = msg.Field(32, s.acquiringInstitutionID)
// 	_ = msg.Field(33, viper.GetString("iso8583.forwarding_institution_id"))
// 	_ = msg.Field(35, fmt.Sprintf("%s=%s", pan, expiry))
// 	_ = msg.Field(37, fmt.Sprintf("%s%s%06d", localDate, localTime[:2], cardReq.CardInfo.ATC))
// 	_ = msg.Field(40, "601")
// 	_ = msg.Field(41, s.terminalID)
// 	_ = msg.Field(42, s.merchantID)
// 	_ = msg.Field(43, fmt.Sprintf("%-40s", viper.GetString("iso8583.card_acceptor_name")))
// 	_ = msg.Field(49, currencyCode)

// 	// F59: Echo data — zero-padded amount with institution prefix
// 	_ = msg.Field(59, fmt.Sprintf("%018d", cardReq.Amount))

// 	// F90: Original Data Elements — MTI(4)+STAN(6)+DateTime(10)+AcqInst(11)+FwdInst(11) = 42
// 	_ = msg.Field(90, fmt.Sprintf("0200%s%s%011d%011d",
// 		stan,
// 		now.Format("0102150405"),
// 		parseInstitutionID(s.acquiringInstitutionID),
// 		parseInstitutionID(viper.GetString("iso8583.forwarding_institution_id")),
// 	))

// 	// F100: Receiving Institution ID
// 	_ = msg.Field(100, viper.GetString("iso8583.receiving_institution_id"))

// 	// F102/F103: Account identifiers
// 	_ = msg.Field(102, fmt.Sprintf("%09d", cardReq.Amount))
// 	_ = msg.Field(103, fmt.Sprintf("%09d", cardReq.Amount))

// 	// F123: POS Data Code (15 chars)
// 	_ = msg.Field(123, "511201513344002")

// 	// F127: Private use — sub-packager with ICC XML in sub-field 25
// 	// Structure: bitmap(16 ASCII hex) + sub-field-25-length(4 digits) + XML
// 	// Sub-bitmap 0000008000000000 = only sub-field 25 set
// 	if cardReq.CardInfo.IssuerAppData != "" && isValidEMVHex(cardReq.CardInfo.IssuerAppData) {
// 		iccXML := buildICCXML(cardReq, countryCode, currencyCode)
// 		f127Value := fmt.Sprintf("0000008000000000%04d%s", len(iccXML), iccXML)
// 		_ = msg.Field(127, f127Value)
// 	}

// 	// F128: MAC (64-char hex string)
// 	_ = msg.Field(128, "088ecae48e4f2acef0c33aeb531a37534f0e58d6eac66d793e38ea2415fa3e12")

// 	slog.Info("ISO8583.build0200.success", "tx_id", cardReq.TransactionID)
// 	return msg, nil
// }

// // buildICCXML constructs the ICC XML payload for F127 sub-field 25,
// // matching the jPOS test message format exactly.
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

// // isValidEMVHex returns true if s is a non-empty hex string of valid EMV tag length (max 510 hex chars = 255 bytes).
// func isValidEMVHex(s string) bool {
// 	if len(s) == 0 || len(s) > 510 || len(s)%2 != 0 {
// 		return false
// 	}
// 	for _, c := range s {
// 		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
// 			return false
// 		}
// 	}
// 	return true
// }

// // parseInstitutionID converts a numeric institution ID string to int64 for zero-padded formatting.
// func parseInstitutionID(s string) int64 {
// 	var n int64
// 	fmt.Sscanf(s, "%d", &n)
// 	return n
// }

// // expiryToYYMM converts MM/YY expiry format to YYMM as required by ISO 8583 Field 14.
// func expiryToYYMM(expiry string) string {
// 	if len(expiry) != 5 || expiry[2] != '/' {
// 		return "0000"
// 	}
// 	return expiry[3:5] + expiry[0:2] // YY + MM
// }

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

// func (s *ISO8583Service) processAuthorization(ctx context.Context, msg *iso8583.Message) (*iso8583.Message, error) {
// 	pan, _ := msg.GetString(2)
// 	amount, _ := msg.GetString(4)
// 	stan, _ := msg.GetString(11)

// 	slog.Debug("iso8583.auth", "pan", utils.MaskPAN(pan), "amount", amount, "stan", stan)

// 	var balance int64
// 	var status string
// 	err := s.db.QueryRowContext(ctx, `SELECT balance, status FROM accounts WHERE card_id = $1`, pan).Scan(&balance, &status)
// 	if err != nil {
// 		if errors.Is(err, sql.ErrNoRows) {
// 			return s.BuildAuthorizationResponse(msg, "14")
// 		}
// 		return s.BuildAuthorizationResponse(msg, "96")
// 	}

// 	if status != "ACTIVE" {
// 		return s.BuildAuthorizationResponse(msg, "57")
// 	}

// 	_ = generateAuthCode()
// 	return s.BuildAuthorizationResponse(msg, "00")
// }

// func (s *ISO8583Service) processFinancial(ctx context.Context, msg *iso8583.Message) (*iso8583.Message, error) {
// 	pan, _ := msg.GetString(2)
// 	amount, _ := msg.GetString(4)
// 	stan, _ := msg.GetString(11)

// 	slog.Debug("iso8583.financial", "pan", utils.MaskPAN(pan), "amount", amount, "stan", stan)

// 	//tx, err := s.db.BeginTx(ctx, nil)
// 	//if err != nil {
// 	//	return s.BuildFinancialResponse(msg, "96")
// 	//}
// 	//defer tx.Rollback()
// 	//
// 	//var balance int64
// 	//err = tx.QueryRowContext(ctx, `SELECT balance FROM accounts WHERE card_id = $1 FOR UPDATE`, pan).Scan(&balance)
// 	//if err != nil {
// 	//	return s.BuildFinancialResponse(msg, "14")
// 	//}
// 	//
// 	//_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance - $1 WHERE card_id = $2`, amount, pan)
// 	//if err != nil {
// 	//	return s.BuildFinancialResponse(msg, "96")
// 	//}
// 	//
// 	//if err := tx.Commit(); err != nil {
// 	//	return s.BuildFinancialResponse(msg, "96")
// 	//}

// 	return s.BuildFinancialResponse(msg, "00")
// }

// func (s *ISO8583Service) processReversal(ctx context.Context, msg *iso8583.Message) (*iso8583.Message, error) {
// 	pan, _ := msg.GetString(2)
// 	origStan, _ := msg.GetString(90)

// 	slog.Info("ISO8583.reversal", "pan", utils.MaskPAN(pan), "orig_stan", origStan)

// 	return s.BuildFinancialResponse(msg, "00")
// }

// func (s *ISO8583Service) BuildAuthorizationResponse(msg *iso8583.Message, responseCode string) (*iso8583.Message, error) {
// 	respMsg := iso8583.NewMessage(s.spec)
// 	respMsg.MTI("0110")

// 	pan, _ := msg.GetString(2)
// 	_ = respMsg.Field(2, pan)

// 	procCode, _ := msg.GetString(3)
// 	_ = respMsg.Field(3, procCode)

// 	amount, _ := msg.GetString(4)
// 	_ = respMsg.Field(4, amount)

// 	stan, _ := msg.GetString(11)
// 	_ = respMsg.Field(11, stan)

// 	_ = respMsg.Field(38, generateAuthCode())
// 	_ = respMsg.Field(39, responseCode)

// 	return respMsg, nil
// }

// func (s *ISO8583Service) BuildFinancialResponse(msg *iso8583.Message, responseCode string) (*iso8583.Message, error) {
// 	respMsg := iso8583.NewMessage(s.spec)
// 	respMsg.MTI("0210")

// 	pan, _ := msg.GetString(2)
// 	_ = respMsg.Field(2, pan)

// 	procCode, _ := msg.GetString(3)
// 	_ = respMsg.Field(3, procCode)

// 	amount, _ := msg.GetString(4)
// 	_ = respMsg.Field(4, amount)

// 	stan, _ := msg.GetString(11)
// 	_ = respMsg.Field(11, stan)

// 	_ = respMsg.Field(39, responseCode)

// 	return respMsg, nil
// }

// func createISO8583Spec() *iso8583.MessageSpec {
// 	return &iso8583.MessageSpec{
// 		Fields: map[int]field.Field{
// 			0: field.NewString(&field.Spec{
// 				Length:      4,
// 				Description: "Message Type Indicator",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			1: field.NewBitmap(&field.Spec{
// 				Description: "Bitmap",
// 				Enc:         encoding.Binary,
// 				Pref:        prefix.Binary.Fixed,
// 			}),
// 			2: field.NewString(&field.Spec{
// 				Length:      19,
// 				Description: "Primary Account Number",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.LL,
// 			}),
// 			3: field.NewString(&field.Spec{
// 				Length:      6,
// 				Description: "Processing Code",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			4: field.NewString(&field.Spec{
// 				Length:      12,
// 				Description: "Amount, Transaction",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			11: field.NewString(&field.Spec{
// 				Length:      6,
// 				Description: "System Trace Audit Number",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			12: field.NewString(&field.Spec{
// 				Length:      6,
// 				Description: "Local Transaction Time",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			13: field.NewString(&field.Spec{
// 				Length:      4,
// 				Description: "Local Transaction Date",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			14: field.NewString(&field.Spec{
// 				Length:      4,
// 				Description: "Expiration Date",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			15: field.NewString(&field.Spec{
// 				Length:      4,
// 				Description: "Settlement Date",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			18: field.NewString(&field.Spec{
// 				Length:      4,
// 				Description: "Merchant Category Code",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			22: field.NewString(&field.Spec{
// 				Length:      3,
// 				Description: "Point of Service Entry Mode",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			25: field.NewString(&field.Spec{
// 				Length:      2,
// 				Description: "Point of Service Condition Code",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			26: field.NewString(&field.Spec{
// 				Length:      2,
// 				Description: "Point of Service PIN Capture Code",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			28: field.NewString(&field.Spec{
// 				Length:      9,
// 				Description: "Transaction Fee Amount",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			32: field.NewString(&field.Spec{
// 				Length:      11,
// 				Description: "Acquiring Institution ID Code",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.LL,
// 			}),
// 			33: field.NewString(&field.Spec{
// 				Length:      11,
// 				Description: "Forwarding Institution ID Code",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.LL,
// 			}),
// 			35: field.NewString(&field.Spec{
// 				Length:      37,
// 				Description: "Track 2 Data",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.LL,
// 			}),
// 			37: field.NewString(&field.Spec{
// 				Length:      12,
// 				Description: "Retrieval Reference Number",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			40: field.NewString(&field.Spec{
// 				Length:      3,
// 				Description: "Network Management Information Code",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			41: field.NewString(&field.Spec{
// 				Length:      8,
// 				Description: "Card Acceptor Terminal ID",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			42: field.NewString(&field.Spec{
// 				Length:      15,
// 				Description: "Card Acceptor ID Code",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			43: field.NewString(&field.Spec{
// 				Length:      40,
// 				Description: "Card Acceptor Name/Location",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			38: field.NewString(&field.Spec{
// 				Length:      6,
// 				Description: "Authorization Code",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			39: field.NewString(&field.Spec{
// 				Length:      2,
// 				Description: "Response Code",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 			55: field.NewString(&field.Spec{
// 				Length:      999,
// 				Description: "ICC Data",
// 				Enc:         encoding.Binary,
// 				Pref:        prefix.Binary.LLL,
// 			}),
// 			90: field.NewString(&field.Spec{
// 				Length:      42,
// 				Description: "Original Data Elements",
// 				Enc:         encoding.ASCII,
// 				Pref:        prefix.ASCII.Fixed,
// 			}),
// 		},
// 	}
// }

// func generateAuthCode() string {
// 	b := make([]byte, 3)
// 	rand.Read(b)
// 	return fmt.Sprintf("%06d", int(b[0])<<16|int(b[1])<<8|int(b[2]))[:6]
// }
