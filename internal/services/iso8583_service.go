package services

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/moov-io/iso8583"
	"github.com/moov-io/iso8583/encoding"
	"github.com/moov-io/iso8583/field"
	"github.com/moov-io/iso8583/prefix"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
	"github.com/spf13/viper"
)

type ISO8583Service struct {
	db   *sql.DB
	HSM  hsm.HSMInterface
	spec *iso8583.MessageSpec
}

func NewISO8583Service(db *sql.DB, hsmInstance hsm.HSMInterface) models.ISO8583Service {
	svc := &ISO8583Service{
		db:   db,
		HSM:  hsmInstance,
		spec: createISO8583Spec(),
	}

	return svc
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

func (s *ISO8583Service) BuildISO8583Message(cardReq *models.CardPaymentRequest) ([]byte, error) {
	slog.Info("ISO8583.build.start", "tx_id", cardReq.TransactionID)

	msg := iso8583.NewMessage(s.spec)
	msg.MTI("0200")
	slog.Info("ISO8583.build.mti", "mti", "0200")

	pan, err := s.HSM.DecryptPII(cardReq.CardInfo.EncryptedPAN)
	if err != nil {
		slog.Error("ISO8583.build.pan_decrypt_failed", "error", err)
		return nil, err
	}

	_ = msg.Field(2, pan)
	slog.Debug("ISO8583.build.field", "field", 2, "name", "PAN", "value", utils.MaskPAN(pan))

	procCode := processingCode(cardReq.TxType)
	_ = msg.Field(3, procCode)
	slog.Debug("ISO8583.build.field", "field", 3, "name", "Processing Code", "value", procCode)

	amountStr := fmt.Sprintf("%012d", cardReq.Amount)
	_ = msg.Field(4, amountStr)
	slog.Debug("ISO8583.build.field", "field", 4, "name", "Amount", "value_", amountStr)

	stan := fmt.Sprintf("%06d", cardReq.CardInfo.ATC)
	_ = msg.Field(11, stan)
	slog.Debug("ISO8583.build.field", "field", 11, "name", "STAN", "value", stan)

	// Field 12: Local Transaction Time (HHMMSS)
	localTime := fmt.Sprintf("%06d", cardReq.TransactionDate%1000000)
	_ = msg.Field(12, localTime)
	slog.Debug("ISO8583.build.field", "field", 12, "name", "Local Transaction Time", "value", localTime)

	// Field 13: Local Transaction Date (MMDD)
	localDate := fmt.Sprintf("%04d", (cardReq.TransactionDate/1000000)%10000)
	_ = msg.Field(13, localDate)
	slog.Debug("ISO8583.build.field", "field", 13, "name", "Local Transaction Date", "value", localDate)

	// Field 14: Expiration Date (YYMM)
	_ = msg.Field(14, expiryToYYMM(cardReq.CardInfo.ExpiryDate))
	slog.Debug("ISO8583.build.field", "field", 14, "name", "Expiration Date", "value", expiryToYYMM(cardReq.CardInfo.ExpiryDate))

	// Field 15: Settlement Date (MMDD) — same as local date
	_ = msg.Field(15, localDate)
	slog.Debug("ISO8583.build.field", "field", 15, "name", "Settlement Date", "value", localDate)

	// Field 18: Merchant Category Code
	_ = msg.Field(18, "5011")
	slog.Debug("ISO8583.build.field", "field", 18, "name", "MCC", "value", "5011")

	// Field 22: POS Entry Mode — 051 = chip read, PIN not required
	_ = msg.Field(22, "051")
	slog.Debug("ISO8583.build.field", "field", 22, "name", "POS Entry Mode", "value", "051")

	// Field 25: POS Condition Code
	_ = msg.Field(25, "00")
	slog.Debug("ISO8583.build.field", "field", 25, "name", "POS Condition Code", "value", "00")

	// Field 26: POS PIN Capture Code
	_ = msg.Field(26, "04")
	slog.Debug("ISO8583.build.field", "field", 26, "name", "POS PIN Capture Code", "value", "04")

	// Field 28: Transaction Fee Amount — D=debit, 8 digit zero-padded
	_ = msg.Field(28, "D00000000")
	slog.Debug("ISO8583.build.field", "field", 28, "name", "Transaction Fee Amount", "value", "D00000000")

	// Fields 32 & 33: Acquiring / Forwarding Institution ID
	acquiringID := viper.GetString("iso8583.acquiring_institution_id")
	forwardingID := viper.GetString("iso8583.forwarding_institution_id")
	_ = msg.Field(32, acquiringID)
	slog.Debug("ISO8583.build.field", "field", 32, "name", "Acquiring Institution ID", "value", acquiringID)
	_ = msg.Field(33, forwardingID)
	slog.Debug("ISO8583.build.field", "field", 33, "name", "Forwarding Institution ID", "value", forwardingID)

	// Field 35: Track 2 Data — PAN=YYMM (service code omitted for chip)
	track2 := fmt.Sprintf("%s=%s", pan, expiryToYYMM(cardReq.CardInfo.ExpiryDate))
	_ = msg.Field(35, track2)
	slog.Debug("ISO8583.build.field", "field", 35, "name", "Track 2 Data", "value", utils.MaskPAN(track2))

	// Field 37: Retrieval Reference Number — MMDDHHNNNNNN (4+2+6 = 12 chars)
	rrn := fmt.Sprintf("%s%s%06d", localDate, localTime[:2], cardReq.CardInfo.ATC)
	_ = msg.Field(37, rrn)
	slog.Debug("ISO8583.build.field", "field", 37, "name", "RRN", "value", rrn)

	// Field 40: Network Management Information Code
	_ = msg.Field(40, "601")
	slog.Debug("ISO8583.build.field", "field", 40, "name", "Network Mgmt Info Code", "value", "601")

	// Fields 41, 42, 43: Terminal / Merchant identifiers from config
	terminalID := viper.GetString("iso8583.terminal_id")
	cardAcceptorID := viper.GetString("iso8583.card_acceptor_id")
	cardAcceptorName := fmt.Sprintf("%-40s", viper.GetString("iso8583.card_acceptor_name"))
	_ = msg.Field(41, terminalID)
	slog.Debug("ISO8583.build.field", "field", 41, "name", "Terminal ID", "value", terminalID)
	_ = msg.Field(42, cardAcceptorID)
	slog.Debug("ISO8583.build.field", "field", 42, "name", "Card Acceptor ID", "value", cardAcceptorID)
	_ = msg.Field(43, cardAcceptorName)
	slog.Debug("ISO8583.build.field", "field", 43, "name", "Card Acceptor Name", "value", cardAcceptorName)

	if cardReq.CardInfo.IssuerAppData != "" {
		iccBytes, err := hex.DecodeString(cardReq.CardInfo.IssuerAppData)
		if err != nil {
			slog.Error("ISO8583.build.icc_decode_failed", "error", err)
			return nil, fmt.Errorf("invalid ICC data: %w", err)
		}
		_ = msg.Field(55, string(iccBytes))
		slog.Debug("ISO8583.build.field", "field", 55, "name", "ICC Data", "bytes", len(iccBytes))
	}

	_ = msg.Field(128, "088ecae48e4f2acef0c33aeb531a37534f0e58d6eac66d793e38ea2415fa3e12")

	packed, err := msg.Pack()
	if err != nil {
		slog.Error("ISO8583.build.pack_failed", "error", err)
		return nil, err
	}

	slog.Info("ISO8583.build.success", "tx_id", cardReq.TransactionID, "packed_bytes", len(packed))
	slog.Debug("ISO8583.build.hex", "hex", fmt.Sprintf("%x", packed))
	return packed, nil
}

// expiryToYYMM converts MM/YY expiry format to YYMM as required by ISO 8583 Field 14.
func expiryToYYMM(expiry string) string {
	if len(expiry) != 5 || expiry[2] != '/' {
		return "0000"
	}
	return expiry[3:5] + expiry[0:2] // YY + MM
}

func (s *ISO8583Service) ProcessMessage(ctx context.Context, rawMsg []byte) ([]byte, error) {
	msg := iso8583.NewMessage(s.spec)
	if err := msg.Unpack(rawMsg); err != nil {
		return nil, fmt.Errorf("failed to unpack message: %w", err)
	}

	mti, _ := msg.GetMTI()
	slog.Info("ISO8583.process.mti", "mti", mti)

	var respMsg *iso8583.Message
	var err error

	switch mti {
	case "0100": // Authorization Request
		respMsg, err = s.processAuthorization(ctx, msg)
	case "0200": // Financial Request
		respMsg, err = s.processFinancial(ctx, msg)
	case "0400": // Reversal Request
		respMsg, err = s.processReversal(ctx, msg)
	default:
		return nil, fmt.Errorf("unsupported MTI: %s", mti)
	}

	if err != nil {
		return nil, err
	}

	return respMsg.Pack()
}

func (s *ISO8583Service) processAuthorization(ctx context.Context, msg *iso8583.Message) (*iso8583.Message, error) {
	pan, _ := msg.GetString(2)
	amount, _ := msg.GetString(4)
	stan, _ := msg.GetString(11)

	slog.Debug("iso8583.auth", "pan", utils.MaskPAN(pan), "amount", amount, "stan", stan)

	var balance int64
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT balance, status FROM accounts WHERE card_id = $1`, pan).Scan(&balance, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return s.BuildAuthorizationResponse(msg, "14")
		}
		return s.BuildAuthorizationResponse(msg, "96")
	}

	if status != "ACTIVE" {
		return s.BuildAuthorizationResponse(msg, "57")
	}

	_ = generateAuthCode()
	return s.BuildAuthorizationResponse(msg, "00")
}

func (s *ISO8583Service) processFinancial(ctx context.Context, msg *iso8583.Message) (*iso8583.Message, error) {
	pan, _ := msg.GetString(2)
	amount, _ := msg.GetString(4)
	stan, _ := msg.GetString(11)

	slog.Debug("iso8583.financial", "pan", utils.MaskPAN(pan), "amount", amount, "stan", stan)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return s.BuildFinancialResponse(msg, "96")
	}
	defer tx.Rollback()

	var balance int64
	err = tx.QueryRowContext(ctx, `SELECT balance FROM accounts WHERE card_id = $1 FOR UPDATE`, pan).Scan(&balance)
	if err != nil {
		return s.BuildFinancialResponse(msg, "14")
	}

	_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance - $1 WHERE card_id = $2`, amount, pan)
	if err != nil {
		return s.BuildFinancialResponse(msg, "96")
	}

	if err := tx.Commit(); err != nil {
		return s.BuildFinancialResponse(msg, "96")
	}

	return s.BuildFinancialResponse(msg, "00")
}

func (s *ISO8583Service) processReversal(ctx context.Context, msg *iso8583.Message) (*iso8583.Message, error) {
	pan, _ := msg.GetString(2)
	origStan, _ := msg.GetString(90)

	slog.Info("ISO8583.reversal", "pan", utils.MaskPAN(pan), "orig_stan", origStan)

	return s.BuildFinancialResponse(msg, "00")
}

func (s *ISO8583Service) BuildAuthorizationResponse(msg *iso8583.Message, responseCode string) (*iso8583.Message, error) {
	respMsg := iso8583.NewMessage(s.spec)
	respMsg.MTI("0110")

	pan, _ := msg.GetString(2)
	_ = respMsg.Field(2, pan)

	procCode, _ := msg.GetString(3)
	_ = respMsg.Field(3, procCode)

	amount, _ := msg.GetString(4)
	_ = respMsg.Field(4, amount)

	stan, _ := msg.GetString(11)
	_ = respMsg.Field(11, stan)

	_ = respMsg.Field(38, generateAuthCode())
	_ = respMsg.Field(39, responseCode)

	return respMsg, nil
}

func (s *ISO8583Service) BuildFinancialResponse(msg *iso8583.Message, responseCode string) (*iso8583.Message, error) {
	respMsg := iso8583.NewMessage(s.spec)
	respMsg.MTI("0210")

	pan, _ := msg.GetString(2)
	_ = respMsg.Field(2, pan)

	procCode, _ := msg.GetString(3)
	_ = respMsg.Field(3, procCode)

	amount, _ := msg.GetString(4)
	_ = respMsg.Field(4, amount)

	stan, _ := msg.GetString(11)
	_ = respMsg.Field(11, stan)

	_ = respMsg.Field(39, responseCode)

	return respMsg, nil
}

func createISO8583Spec() *iso8583.MessageSpec {
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

func generateAuthCode() string {
	b := make([]byte, 3)
	rand.Read(b)
	return fmt.Sprintf("%06d", int(b[0])<<16|int(b[1])<<8|int(b[2]))[:6]
}
