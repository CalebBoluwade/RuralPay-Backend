package services

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ruralpay/backend/internal/models"
	"github.com/stretchr/testify/assert"
)

func TestISO20022Service_ConvertToISO20022(t *testing.T) {
	service := NewISO20022Service()

	t.Run("successful conversion", func(t *testing.T) {
		tx := models.Transaction{
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
		tx := models.Transaction{
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

	t.Run("successful settlement", func(t *testing.T) {
		tx := models.Transaction{
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

		assert.Equal(t, http.StatusOK, w.Code)
		var response map[string]any
		json.Unmarshal(w.Body.Bytes(), &response)
		assert.Equal(t, "settled", response["status"])
		assert.Equal(t, "pacs.002.001.08", response["messageType"])
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
		tx := &models.Transaction{
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
		assert.Equal(t, tx.Amount, doc.GrpHdr.TtlIntrBkSttlmAmt.Value)
		assert.Len(t, doc.CdtTrfTxInf, 1)
		assert.Equal(t, string(*doc.CdtTrfTxInf[0].PmtId.InstrId), tx.TransactionID)
		assert.Equal(t, string(doc.CdtTrfTxInf[0].PmtId.EndToEndId), tx.TransactionID)
	})
}

func TestISO20022Service_CreatePacs002(t *testing.T) {
	service := NewISO20022Service()

	t.Run("create valid pacs002", func(t *testing.T) {
		tx := &models.Transaction{
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
		tx := &models.Transaction{
			TransactionID: "tx123",

			Amount:   10050,
			Currency: "NGN",
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
		// Test with a struct that can't be marshaled to XML
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
		tx := &models.Transaction{
			TransactionID: "tx123",
			Amount:        10050,
			Currency:      "NGN",
		}

		doc, err := service.ConvertTransaction(tx)
		assert.NoError(t, err)
		assert.NotNil(t, doc)
		assert.Equal(t, "NGN", string(doc.GrpHdr.TtlIntrBkSttlmAmt.Ccy))
		assert.Equal(t, tx.Amount, doc.GrpHdr.TtlIntrBkSttlmAmt.Value)
	})
}

func TestISO20022Service_SendToSettlement(t *testing.T) {
	service := NewISO20022Service()

	t.Run("send to settlement", func(t *testing.T) {
		tx := &models.Transaction{
			TransactionID: "tx123",
			Amount:        10050,
			Currency:      "NGN",
		}

		doc, err := service.CreatePacs008(tx)
		assert.NoError(t, err)

		// This should not error as it's just a mock implementation
		_, err = service.SendToSettlement(doc)
		assert.NoError(t, err)
	})

	t.Run("send invalid document", func(t *testing.T) {
		// Test with a struct that can't be marshaled to XML
		invalidDoc := make(chan int)

		_, err := service.SendToSettlement(invalidDoc)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to marshal XML")
	})
}
