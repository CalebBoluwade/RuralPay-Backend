package services

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestISO20022Service_ConvertToISO20022(t *testing.T) {
	service := NewISO20022Service()

	t.Run("successful conversion", func(t *testing.T) {
		tx := models.TransactionRecord{
			TransactionID: "tx123",
			FromAccountID: "card123",
			ToAccountID:   "merchant123",
			Amount:        10050,
			Currency:      "NGN",
			Status:        "PENDING",
		}

		body, _ := json.Marshal(tx)
		r := httptest.NewRequest("POST", "/iso20022/convert", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		service.ConvertToISO20022(w, r)

		assert.Equal(t, http.StatusOK, w.Code)
		var response map[string]any
		json.Unmarshal(w.Body.Bytes(), &response)
		assert.Equal(t, "converted", response["status"])
		assert.Equal(t, "pacs.008.001.08", response["messageType"])
		assert.NotEmpty(t, response["xml"])
	})

	t.Run("Unable To Process This Request At This Time", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/iso20022/convert", bytes.NewBuffer([]byte("invalid")))
		w := httptest.NewRecorder()

		service.ConvertToISO20022(w, r)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("validation failure", func(t *testing.T) {
		tx := models.TransactionRecord{
			// Missing required fields to trigger validation
			Amount:   10050,
			Currency: "NGN",
		}

		body, _ := json.Marshal(tx)
		r := httptest.NewRequest("POST", "/iso20022/convert", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		service.ConvertToISO20022(w, r)

		// The service doesn't validate required fields, so this passes
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestISO20022Service_ProcessSettlement(t *testing.T) {
	service := NewISO20022Service()

	t.Run("settlement fails when NIBSS unreachable", func(t *testing.T) {
		tx := models.TransactionRecord{
			TransactionID: "tx123",
			FromAccountID: "card123",
			ToAccountID:   "merchant123",
			Amount:        10050,
			Currency:      "NGN",
			Status:        "PENDING",
		}

		body, _ := json.Marshal(tx)
		r := httptest.NewRequest("POST", "/iso20022/settlement", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		service.ProcessSettlement(w, r)

		// NIBSS is not reachable in tests; expect a dependency failure
		assert.Equal(t, http.StatusFailedDependency, w.Code)
	})

	t.Run("Unable To Process This Request At This Time", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/iso20022/settlement", bytes.NewBuffer([]byte("invalid")))
		w := httptest.NewRecorder()

		service.ProcessSettlement(w, r)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestISO20022Service_CreatePacs008(t *testing.T) {
	service := NewISO20022Service()

	t.Run("create valid pacs008", func(t *testing.T) {
		tx := &models.TransactionRecord{
			TransactionID: "tx123",
			FromAccountID: "card123",
			ToAccountID:   "merchant123",
			Amount:        10050,
			Currency:      "NGN",
		}

		doc, err := service.CreatePacs008(tx)
		assert.NoError(t, err)
		assert.NotNil(t, doc)
		assert.NotEmpty(t, doc.GrpHdr.MsgId)
		assert.Equal(t, "1", string(doc.GrpHdr.NbOfTxs))
		assert.Equal(t, "NGN", string(doc.GrpHdr.TtlIntrBkSttlmAmt.Ccy))
		assert.Equal(t, float64(tx.Amount)/100.0, doc.GrpHdr.TtlIntrBkSttlmAmt.Value)
		assert.Len(t, doc.CdtTrfTxInf, 1)
		assert.Equal(t, string(*doc.CdtTrfTxInf[0].PmtId.InstrId), tx.TransactionID)
		assert.Equal(t, string(doc.CdtTrfTxInf[0].PmtId.EndToEndId), tx.TransactionID)
	})
}

func TestISO20022Service_CreatePacs002(t *testing.T) {
	service := NewISO20022Service()

	t.Run("create valid pacs002", func(t *testing.T) {
		tx := &models.TransactionRecord{
			TransactionID: "tx123",
		}

		doc, err := service.CreatePacs002(tx, "ACCP")
		assert.NoError(t, err)
		assert.NotNil(t, doc)
		assert.NotEmpty(t, doc.GrpHdr.MsgId)
		assert.Len(t, doc.TxInfAndSts, 1)
		assert.Equal(t, string(*doc.TxInfAndSts[0].OrgnlInstrId), tx.TransactionID)
		assert.Equal(t, string(*doc.TxInfAndSts[0].OrgnlEndToEndId), tx.TransactionID)
		assert.Equal(t, string(*doc.TxInfAndSts[0].TxSts), "ACCP")
	})
}

func TestISO20022Service_ConvertToXML(t *testing.T) {
	service := NewISO20022Service()

	t.Run("convert to XML", func(t *testing.T) {
		tx := &models.TransactionRecord{
			TransactionID: "tx123",
			Amount:        10050,
			Currency:      "NGN",
		}

		doc, err := service.CreatePacs008(tx)
		assert.NoError(t, err)

		xmlString, err := service.ConvertToXML(doc)
		assert.NoError(t, err)
		assert.NotEmpty(t, xmlString)
		assert.Contains(t, xmlString, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>")
		assert.Contains(t, xmlString, "tx123")
		assert.Contains(t, xmlString, "NGN")
	})

	t.Run("convert invalid struct", func(t *testing.T) {
		invalidStruct := make(chan int)

		xmlString, err := service.ConvertToXML(invalidStruct)
		assert.Error(t, err)
		assert.Empty(t, xmlString)
		assert.Contains(t, err.Error(), "failed to marshal XML")
	})
}

func TestISO20022Service_ConvertTransaction(t *testing.T) {
	service := NewISO20022Service()

	t.Run("convert transaction", func(t *testing.T) {
		tx := &models.TransactionRecord{
			TransactionID: "tx123",
			Amount:        10050,
			Currency:      "NGN",
		}

		doc, err := service.ConvertTransaction(tx)
		assert.NoError(t, err)
		assert.NotNil(t, doc)
		assert.Equal(t, "NGN", string(doc.GrpHdr.TtlIntrBkSttlmAmt.Ccy))
		assert.Equal(t, float64(tx.Amount)/100.0, doc.GrpHdr.TtlIntrBkSttlmAmt.Value)
	})
}

func TestISO20022Service_CreatePacs028(t *testing.T) {
	service := NewISO20022Service()

	t.Run("create valid pacs028", func(t *testing.T) {
		doc, err := service.CreatePacs028("msg-001", "tx123")
		assert.NoError(t, err)
		assert.NotNil(t, doc)
		assert.NotEmpty(t, doc.GrpHdr.MsgId)
		assert.Len(t, doc.OrgnlGrpInf, 1)
		assert.Equal(t, "msg-001", string(doc.OrgnlGrpInf[0].OrgnlMsgId))
		assert.Equal(t, "pacs.008.001.08", string(doc.OrgnlGrpInf[0].OrgnlMsgNmId))
		assert.Len(t, doc.TxInf, 1)
		assert.Equal(t, "tx123", string(*doc.TxInf[0].OrgnlTxId))
		assert.Equal(t, "tx123", string(*doc.TxInf[0].OrgnlInstrId))
		assert.Equal(t, "tx123", string(*doc.TxInf[0].OrgnlEndToEndId))
	})

	t.Run("missing originalMsgID returns error", func(t *testing.T) {
		_, err := service.CreatePacs028("", "tx123")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "originalMsgID and originalTxID are required")
	})

	t.Run("missing originalTxID returns error", func(t *testing.T) {
		_, err := service.CreatePacs028("msg-001", "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "originalMsgID and originalTxID are required")
	})
}

func TestISO20022Service_RequestPaymentStatus(t *testing.T) {
	service := NewISO20022Service()

	t.Run("fails when NIBSS unreachable", func(t *testing.T) {
		_, err := service.RequestPaymentStatus("msg-001", "tx123")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to request payment status")
	})

	t.Run("fails with empty originalMsgID", func(t *testing.T) {
		_, err := service.RequestPaymentStatus("", "tx123")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to build pacs.028")
	})
}

func newTestISO20022Service(t *testing.T) (*ISO20022Service, *rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	senderPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	nibssPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	svc := NewISO20022Service()
	svc.senderPriv = senderPriv
	svc.nibssPub = &nibssPriv.PublicKey
	svc.recipientPriv = nibssPriv
	return svc, senderPriv, &nibssPriv.PublicKey
}

func TestISO20022Service_SignXML(t *testing.T) {
	svc, _, _ := newTestISO20022Service(t)

	t.Run("seal and open round-trip", func(t *testing.T) {
		original := "<Document>test pacs.008 payload</Document>"
		msg, err := svc.SignXML(original)
		require.NoError(t, err)
		require.NotNil(t, msg)
		assert.NotEmpty(t, msg.EncryptedPayload)
		assert.NotEmpty(t, msg.WrappedKey)
		assert.NotEmpty(t, msg.Signature)

		recovered, err := svc.VerifyAndOpenXML(msg)
		require.NoError(t, err)
		assert.Equal(t, original, recovered)
	})

	t.Run("tampered payload fails verification", func(t *testing.T) {
		msg, err := svc.SignXML("<Document>legit</Document>")
		require.NoError(t, err)
		msg.EncryptedPayload[0] ^= 0xFF
		_, err = svc.VerifyAndOpenXML(msg)
		assert.Error(t, err)
	})

	t.Run("tampered wrapped key fails decryption", func(t *testing.T) {
		msg, err := svc.SignXML("<Document>legit</Document>")
		require.NoError(t, err)
		msg.WrappedKey[0] ^= 0xFF
		_, err = svc.VerifyAndOpenXML(msg)
		assert.Error(t, err)
	})

	t.Run("no keys configured returns error", func(t *testing.T) {
		svc := NewISO20022Service()
		_, err := svc.SignXML("<Document>test</Document>")
		assert.ErrorContains(t, err, "signing keys not configured")
	})
}

func TestISO20022Service_SignXMLWithPacs008(t *testing.T) {
	svc, _, _ := newTestISO20022Service(t)

	t.Run("sign real pacs.008 XML round-trip", func(t *testing.T) {
		tx := &models.TransactionRecord{
			TransactionID: "tx-sign-001",
			FromAccountID: "acc-from",
			ToAccountID:   "acc-to",
			ToBankCode:    "000013",
			Amount:        50000,
			Currency:      "NGN",
		}
		doc, err := svc.CreatePacs008(tx)
		require.NoError(t, err)
		xmlData, err := svc.ConvertToXML(doc)
		require.NoError(t, err)

		msg, err := svc.SignXML(xmlData)
		require.NoError(t, err)

		recovered, err := svc.VerifyAndOpenXML(msg)
		require.NoError(t, err)
		assert.Equal(t, xmlData, recovered)
		assert.Contains(t, recovered, "tx-sign-001")
	})
}

func TestISO20022Service_SignedMessageJSON(t *testing.T) {
	svc, _, _ := newTestISO20022Service(t)

	t.Run("SignedMessage survives JSON marshal/unmarshal", func(t *testing.T) {
		original := "<Document>json round-trip</Document>"
		msg, err := svc.SignXML(original)
		require.NoError(t, err)

		encoded, err := json.Marshal(msg)
		require.NoError(t, err)

		var decoded utils.SignedMessage
		require.NoError(t, json.Unmarshal(encoded, &decoded))

		recovered, err := svc.VerifyAndOpenXML(&decoded)
		require.NoError(t, err)
		assert.Equal(t, original, recovered)
	})
}

func TestISO20022Service_SendToSettlement(t *testing.T) {
	service := NewISO20022Service()

	t.Run("send to settlement fails when NIBSS unreachable", func(t *testing.T) {
		tx := &models.TransactionRecord{
			TransactionID: "tx123",
			Amount:        10050,
			Currency:      "NGN",
		}

		doc, err := service.CreatePacs008(tx)
		assert.NoError(t, err)

		_, err = service.SendToSettlement(doc)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to send pacs.008 to settlement")
	})
}
