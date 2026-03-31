package services

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"fmt"
	"log"
	"os"

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
	db         *sql.DB
	HSM        hsm.HSMInterface
	spec       *iso8583.MessageSpec
	senderPriv *rsa.PrivateKey
	nibssPub   *rsa.PublicKey
}

func NewISO8583Service(db *sql.DB, hsmInstance hsm.HSMInterface) models.ISO8583Service {
	svc := &ISO8583Service{
		db:   db,
		HSM:  hsmInstance,
		spec: createISO8583Spec(),
	}

	if privPath := viper.GetString("iso20022.signing_key_path"); privPath != "" {
		if pem, err := os.ReadFile(privPath); err == nil {
			svc.senderPriv, _ = utils.ParseRSAPrivateKey(pem)
		}
	}
	if pubPath := viper.GetString("iso20022.nibss_pub_key_path"); pubPath != "" {
		if pem, err := os.ReadFile(pubPath); err == nil {
			svc.nibssPub, _ = utils.ParseRSAPublicKey(pem)
		}
	}
	return svc
}

// SignXML seals an XML string into a SignedMessage using AES-256-GCM + RSA.
// Returns an error if keys are not configured.
func (iso *ISO8583Service) SignXML(xmlData string) (*utils.SignedMessage, error) {
	if iso.senderPriv == nil || iso.nibssPub == nil {
		return nil, fmt.Errorf("signing keys not configured")
	}
	return utils.SealMessage([]byte(xmlData), iso.senderPriv, iso.nibssPub)
}

func (s *ISO8583Service) BuildISO8583Message(cardReq *models.CardPaymentRequest) ([]byte, error) {
	log.Printf("[CardProvider] Building ISO 8583 Message for txID=%s", cardReq.TransactionID)

	spec := &iso8583.MessageSpec{
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
				Length:      999,
				Description: "Primary Account Number",
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.LLL,
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
			55: field.NewString(&field.Spec{
				Length:      999,
				Description: "ICC Data",
				Enc:         encoding.Binary,
				Pref:        prefix.Binary.LLL,
			}),
		},
	}

	msg := iso8583.NewMessage(spec)
	msg.MTI("0200")
	log.Printf("[CardProvider] Set MTI: 0200")

	msg.Field(2, s.DecryptPIICredentials(cardReq.CardInfo.EncryptedPAN))
	log.Printf("[CardProvider] Set Field 2 (PAN): %s", utils.MaskPAN(s.DecryptPIICredentials(cardReq.CardInfo.EncryptedPAN)))

	msg.Field(3, "000000")
	log.Printf("[CardProvider] Set Field 3 (Processing Code): 000000")

	amountStr := fmt.Sprintf("%012d", cardReq.Amount)
	msg.Field(4, amountStr)
	log.Printf("[CardProvider] Set Field 4 (Amount): %s", amountStr)

	stan := fmt.Sprintf("%06d", cardReq.CardInfo.ATC)
	msg.Field(11, stan)
	log.Printf("[CardProvider] Set Field 11 (STAN): %s", stan)

	iccData := cardReq.CardInfo.IssuerAppData + cardReq.CardInfo.CVR
	if iccData != "" {
		msg.Field(55, iccData)
		log.Printf("[CardProvider] Set Field 55 (ICC Data): %d bytes", len(iccData))
	}

	packed, err := msg.Pack()
	if err != nil {
		log.Printf("[CardProvider] Failed to pack ISO 8583 Message: %v", err)
		return nil, err
	}

	log.Printf("[CardProvider] ISO 8583 Message Packed Successfully: %d bytes", len(packed))
	log.Printf("[CardProvider] ISO 8583 Message (hex): %x", packed)
	return packed, nil
}

func (s *ISO8583Service) ProcessMessage(ctx context.Context, rawMsg []byte) ([]byte, error) {
	msg := iso8583.NewMessage(s.spec)
	if err := msg.Unpack(rawMsg); err != nil {
		return nil, fmt.Errorf("failed to unpack message: %w", err)
	}

	mti, _ := msg.GetMTI()
	log.Printf("[ISO8583] Processing MTI: %s", mti)

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

	log.Printf("[ISO8583] Auth: PAN=%s, Amount=%s, STAN=%s", utils.MaskPAN(pan), amount, stan)

	var balance int64
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT balance, status FROM accounts WHERE card_id = $1`, pan).Scan(&balance, &status)
	if err != nil {
		if err == sql.ErrNoRows {
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

	log.Printf("[ISO8583] Financial: PAN=%s, Amount=%s, STAN=%s", utils.MaskPAN(pan), amount, stan)

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

	log.Printf("[ISO8583] Reversal: PAN=%s, OrigSTAN=%s", utils.MaskPAN(pan), origStan)

	return s.BuildFinancialResponse(msg, "00")
}

func (s *ISO8583Service) BuildAuthorizationResponse(msg *iso8583.Message, responseCode string) (*iso8583.Message, error) {
	respMsg := iso8583.NewMessage(s.spec)
	respMsg.MTI("0110")

	pan, _ := msg.GetString(2)
	respMsg.Field(2, pan)

	procCode, _ := msg.GetString(3)
	respMsg.Field(3, procCode)

	amount, _ := msg.GetString(4)
	respMsg.Field(4, amount)

	stan, _ := msg.GetString(11)
	respMsg.Field(11, stan)

	respMsg.Field(38, generateAuthCode())
	respMsg.Field(39, responseCode)

	return respMsg, nil
}

func (s *ISO8583Service) BuildFinancialResponse(msg *iso8583.Message, responseCode string) (*iso8583.Message, error) {
	respMsg := iso8583.NewMessage(s.spec)
	respMsg.MTI("0210")

	pan, _ := msg.GetString(2)
	respMsg.Field(2, pan)

	procCode, _ := msg.GetString(3)
	respMsg.Field(3, procCode)

	amount, _ := msg.GetString(4)
	respMsg.Field(4, amount)

	stan, _ := msg.GetString(11)
	respMsg.Field(11, stan)

	respMsg.Field(39, responseCode)

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
				Enc:         encoding.ASCII,
				Pref:        prefix.ASCII.LLL,
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

func (s *ISO8583Service) DecryptPIICredentials(encryptedText string) string {
	plaintext, err := s.HSM.DecryptPII(encryptedText)
	if err != nil {
		log.Printf("[CardProvider] Failed to decrypt PII: %v", err)
		return ""
	}
	return plaintext
}

func generateAuthCode() string {
	b := make([]byte, 3)
	rand.Read(b)
	return fmt.Sprintf("%06d", int(b[0])<<16|int(b[1])<<8|int(b[2]))[:6]
}
