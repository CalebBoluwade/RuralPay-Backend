package handlers

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"

	"github.com/moov-io/iso20022/pkg/acmt_v02"
	"github.com/moov-io/iso20022/pkg/pacs_v04"
	"github.com/moov-io/iso20022/pkg/pacs_v08"
	"github.com/ruralpay/backend/internal/services"
	"github.com/ruralpay/backend/internal/utils"
)

type ISO20022CallbackHandler struct {
	isoSvc *services.ISO20022Service
}

func NewISO20022CallbackHandler(isoSvc *services.ISO20022Service) *ISO20022CallbackHandler {
	return &ISO20022CallbackHandler{isoSvc: isoSvc}
}

// decryptBody reads the request body and, if keys are configured, verifies and decrypts
// the SignedMessage envelope. Falls back to the raw body when keys are not present.
func (h *ISO20022CallbackHandler) decryptBody(r *http.Request) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var msg utils.SignedMessage
	if err := json.Unmarshal(raw, &msg); err != nil || msg.EncryptedPayload == nil {
		return raw, nil
	}
	xmlData, err := h.isoSvc.VerifyAndOpenXML(&msg)
	if err != nil {
		return nil, err
	}
	return []byte(xmlData), nil
}

// ReceivePacs008 receives an inbound pacs.008 FIToFICustomerCreditTransfer message
// @Summary Receive pacs.008
// @Description Callback endpoint to receive an inbound ISO20022 pacs.008 credit transfer message
// @Tags ISO20022 Callbacks
// @Accept xml
// @Produce json
// @Success 200 {object} object{messageType=string,msgId=string,nbOfTxs=string}
// @Failure 400 {object} map[string]string
// @Router /pacs008 [post]
func (h *ISO20022CallbackHandler) ReceivePacs008(w http.ResponseWriter, r *http.Request) {
	slog.Info("callback.pacs008.received")

	body, err := h.decryptBody(r)
	if err != nil {
		slog.Error("callback.pacs008.read_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to read request body", http.StatusBadRequest, nil)
		return
	}

	var doc pacs_v08.FIToFICustomerCreditTransferV08
	if err := xml.Unmarshal(body, &doc); err != nil {
		slog.Error("callback.pacs008.parse_failed", "error", err)
		utils.SendErrorResponse(w, "Invalid pacs.008 message", http.StatusBadRequest, nil)
		return
	}

	slog.Info("callback.pacs008.processed",
		"msgId", string(doc.GrpHdr.MsgId),
		"nbOfTxs", string(doc.GrpHdr.NbOfTxs),
	)

	utils.SendSuccessResponse(w, "pacs.008 received", map[string]any{
		"messageType": "pacs.008.001.08",
		"msgId":       string(doc.GrpHdr.MsgId),
		"nbOfTxs":     string(doc.GrpHdr.NbOfTxs),
	}, http.StatusOK)
}

// ReceivePacs002 receives an inbound pacs.002 FIToFIPaymentStatusReport message
// @Summary Receive pacs.002
// @Description Callback endpoint to receive an inbound ISO20022 pacs.002 payment status report
// @Tags ISO20022 Callbacks
// @Accept xml
// @Produce json
// @Success 200 {object} object{messageType=string,msgId=string,txStatus=string}
// @Failure 400 {object} map[string]string
// @Router /pacs002 [post]
func (h *ISO20022CallbackHandler) ReceivePacs002(w http.ResponseWriter, r *http.Request) {
	slog.Info("callback.pacs002.received")

	body, err := h.decryptBody(r)
	if err != nil {
		slog.Error("callback.pacs002.read_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to read request body", http.StatusBadRequest, nil)
		return
	}

	var doc pacs_v08.FIToFIPaymentStatusReportV08
	if err := xml.Unmarshal(body, &doc); err != nil {
		slog.Error("callback.pacs002.parse_failed", "error", err)
		utils.SendErrorResponse(w, "Invalid pacs.002 message", http.StatusBadRequest, nil)
		return
	}

	var txStatus, orgnlTxId string
	if len(doc.TxInfAndSts) > 0 {
		tx := doc.TxInfAndSts[0]
		if tx.TxSts != nil {
			txStatus = string(*tx.TxSts)
		}
		if tx.OrgnlTxId != nil {
			orgnlTxId = string(*tx.OrgnlTxId)
		}
	}

	slog.Info("callback.pacs002.processed",
		"msgId", string(doc.GrpHdr.MsgId),
		"txStatus", txStatus,
		"orgnlTxId", orgnlTxId,
	)

	utils.SendSuccessResponse(w, "pacs.002 received", map[string]any{
		"messageType": "pacs.002.001.08",
		"msgId":       string(doc.GrpHdr.MsgId),
		"txStatus":    txStatus,
		"orgnlTxId":   orgnlTxId,
	}, http.StatusOK)
}

// ReceivePacs028 receives an inbound pacs.028 FIToFIPaymentStatusRequest message
// @Summary Receive pacs.028
// @Description Callback endpoint to receive an inbound ISO20022 pacs.028 payment status request
// @Tags ISO20022 Callbacks
// @Accept xml
// @Produce json
// @Success 200 {object} object{messageType=string,msgId=string,orgnlMsgId=string}
// @Failure 400 {object} map[string]string
// @Router /pacs028 [post]
func (h *ISO20022CallbackHandler) ReceivePacs028(w http.ResponseWriter, r *http.Request) {
	slog.Info("callback.pacs028.received")

	body, err := h.decryptBody(r)
	if err != nil {
		slog.Error("callback.pacs028.read_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to read request body", http.StatusBadRequest, nil)
		return
	}

	var doc pacs_v04.FIToFIPaymentStatusRequestV04
	if err := xml.Unmarshal(body, &doc); err != nil {
		slog.Error("callback.pacs028.parse_failed", "error", err)
		utils.SendErrorResponse(w, "Invalid pacs.028 message", http.StatusBadRequest, nil)
		return
	}

	var orgnlMsgId string
	if len(doc.OrgnlGrpInf) > 0 {
		orgnlMsgId = string(doc.OrgnlGrpInf[0].OrgnlMsgId)
	}

	slog.Info("callback.pacs028.processed",
		"msgId", string(doc.GrpHdr.MsgId),
		"orgnlMsgId", orgnlMsgId,
	)

	utils.SendSuccessResponse(w, "pacs.028 received", map[string]any{
		"messageType": "pacs.028.001.04",
		"msgId":       string(doc.GrpHdr.MsgId),
		"orgnlMsgId":  orgnlMsgId,
	}, http.StatusOK)
}

// ReceiveAcmt023 receives an inbound acmt.023 IdentificationVerificationRequest message
// @Summary Receive acmt.023
// @Description Callback endpoint to receive an inbound ISO20022 acmt.023 identification verification request
// @Tags ISO20022 Callbacks
// @Accept xml
// @Produce json
// @Success 200 {object} object{messageType=string,msgId=string,vrfctnCount=int}
// @Failure 400 {object} map[string]string
// @Router /acmt023 [post]
func (h *ISO20022CallbackHandler) ReceiveAcmt023(w http.ResponseWriter, r *http.Request) {
	slog.Info("callback.acmt023.received")

	body, err := h.decryptBody(r)
	if err != nil {
		slog.Error("callback.acmt023.read_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to read request body", http.StatusBadRequest, nil)
		return
	}

	var doc acmt_v02.IdentificationVerificationRequestV02
	if err := xml.Unmarshal(body, &doc); err != nil {
		slog.Error("callback.acmt023.parse_failed", "error", err)
		utils.SendErrorResponse(w, "Invalid acmt.023 message", http.StatusBadRequest, nil)
		return
	}

	slog.Info("callback.acmt023.processed",
		"msgId", string(doc.Assgnmt.MsgId),
		"vrfctnCount", len(doc.Vrfctn),
	)

	utils.SendSuccessResponse(w, "acmt.023 received", map[string]any{
		"messageType": "acmt.023.001.02",
		"msgId":       string(doc.Assgnmt.MsgId),
		"vrfctnCount": len(doc.Vrfctn),
	}, http.StatusOK)
}

// ReceiveAcmt024 receives an inbound acmt.024 IdentificationVerificationReport message
// @Summary Receive acmt.024
// @Description Callback endpoint to receive an inbound ISO20022 acmt.024 identification verification report
// @Tags ISO20022 Callbacks
// @Accept xml
// @Produce json
// @Success 200 {object} object{messageType=string,msgId=string,verified=bool}
// @Failure 400 {object} map[string]string
// @Router /acmt024 [post]
func (h *ISO20022CallbackHandler) ReceiveAcmt024(w http.ResponseWriter, r *http.Request) {
	slog.Info("callback.acmt024.received")

	body, err := h.decryptBody(r)
	if err != nil {
		slog.Error("callback.acmt024.read_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to read request body", http.StatusBadRequest, nil)
		return
	}

	var doc acmt_v02.IdentificationVerificationReportV02
	if err := xml.Unmarshal(body, &doc); err != nil {
		slog.Error("callback.acmt024.parse_failed", "error", err)
		utils.SendErrorResponse(w, "Invalid acmt.024 message", http.StatusBadRequest, nil)
		return
	}

	var verified bool
	if len(doc.Rpt) > 0 {
		verified = doc.Rpt[0].Vrfctn
	}

	slog.Info("callback.acmt024.processed",
		"msgId", string(doc.Assgnmt.MsgId),
		"verified", verified,
	)

	utils.SendSuccessResponse(w, "acmt.024 received", map[string]any{
		"messageType": "acmt.024.001.02",
		"msgId":       string(doc.Assgnmt.MsgId),
		"verified":    verified,
	}, http.StatusOK)
}
