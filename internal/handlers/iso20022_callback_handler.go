package handlers

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/moov-io/iso20022/pkg/acmt_v02"
	"github.com/moov-io/iso20022/pkg/pacs_v04"
	"github.com/moov-io/iso20022/pkg/pacs_v08"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/services"
	"github.com/ruralpay/backend/internal/utils"
)

type ISO20022CallbackHandler struct {
	isoSvc     *services.ISO20022Service
	nameSvc    services.ACMT024Notifier
	fundsSvc   services.PACS002Notifier
	pacs008Svc services.PACS008Processor
	pacs028Svc services.PACS028Processor
	log        *slog.Logger
}

func NewISO20022CallbackHandler(
	isoSvc *services.ISO20022Service,
	nameSvc services.ACMT024Notifier,
	fundsSvc services.PACS002Notifier,
	pacs008Svc services.PACS008Processor,
	pacs028Svc services.PACS028Processor,
	log *slog.Logger,
) *ISO20022CallbackHandler {
	if log == nil {
		log = slog.Default()
	}
	return &ISO20022CallbackHandler{
		isoSvc:     isoSvc,
		nameSvc:    nameSvc,
		fundsSvc:   fundsSvc,
		pacs008Svc: pacs008Svc,
		pacs028Svc: pacs028Svc,
		log:        log,
	}
}

// decryptBody reads the request body and, if keys are configured, verifies and decrypts
// the SignedMessage envelope. Falls back to the raw body when keys are not present.
func (h *ISO20022CallbackHandler) decryptBody(r *http.Request) ([]byte, error) {
	limited := io.LimitReader(r.Body, 1<<20)

	// Attempt to decode as a SignedMessage envelope with strict field checking.
	var msg utils.SignedMessage
	dec := json.NewDecoder(limited)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&msg); err != nil || msg.EncryptedPayload == nil {
		// Not a SignedMessage — re-read the raw body for direct XML processing.
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			return nil, err
		}
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
	h.log.Info("callback.pacs008.received")

	body, err := h.decryptBody(r)
	if err != nil {
		h.log.Error("callback.pacs008.read_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to read request body", http.StatusBadRequest, nil)
		return
	}

	var doc pacs_v08.FIToFICustomerCreditTransferV08
	if err := xml.Unmarshal(body, &doc); err != nil {
		h.log.Error("callback.pacs008.parse_failed", "error", err)
		utils.SendErrorResponse(w, "Invalid pacs.008 message", http.StatusBadRequest, nil)
		return
	}

	h.log.Info("callback.pacs008.processed",
		"msgId", string(doc.GrpHdr.MsgId),
		"nbOfTxs", string(doc.GrpHdr.NbOfTxs),
	)

	if h.pacs008Svc != nil {
		if err := h.pacs008Svc.ProcessInboundPacs008(r.Context(), string(doc.GrpHdr.MsgId), string(doc.GrpHdr.NbOfTxs)); err != nil {
			h.log.Error("callback.pacs008.process_failed", "error", err)
		}
	}

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
	h.log.Info("callback.pacs002.received")

	body, err := h.decryptBody(r)
	if err != nil {
		h.log.Error("callback.pacs002.read_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to read request body", http.StatusBadRequest, nil)
		return
	}

	var doc pacs_v08.FIToFIPaymentStatusReportV08
	if err := xml.Unmarshal(body, &doc); err != nil {
		h.log.Error("callback.pacs002.parse_failed", "error", err)
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

	h.log.Info("callback.pacs002.processed",
		"msgId", string(doc.GrpHdr.MsgId),
		"txStatus", txStatus,
		"orgnlTxId", orgnlTxId,
	)

	// Unblock any DoTransaction call waiting on the original pacs.008 MsgId.
	if h.fundsSvc != nil && orgnlTxId != "" {
		var result *models.FundsTransferResult
		var notifyErr error
		if txStatus == "RJCT" {
			notifyErr = fmt.Errorf("settlement rejected with status: %s", txStatus)
		} else {
			result = &models.FundsTransferResult{SessionID: orgnlTxId, Status: txStatus}
		}
		h.fundsSvc.NotifyPacs002(orgnlTxId, result, notifyErr)
	}

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
	h.log.Info("callback.pacs028.received")

	body, err := h.decryptBody(r)
	if err != nil {
		h.log.Error("callback.pacs028.read_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to read request body", http.StatusBadRequest, nil)
		return
	}

	var doc pacs_v04.FIToFIPaymentStatusRequestV04
	if err := xml.Unmarshal(body, &doc); err != nil {
		h.log.Error("callback.pacs028.parse_failed", "error", err)
		utils.SendErrorResponse(w, "Invalid pacs.028 message", http.StatusBadRequest, nil)
		return
	}

	var orgnlMsgId string
	if len(doc.OrgnlGrpInf) > 0 {
		orgnlMsgId = string(doc.OrgnlGrpInf[0].OrgnlMsgId)
	}

	h.log.Info("callback.pacs028.processed",
		"msgId", string(doc.GrpHdr.MsgId),
		"orgnlMsgId", orgnlMsgId,
	)

	if h.pacs028Svc != nil {
		if err := h.pacs028Svc.ProcessInboundPacs028(r.Context(), string(doc.GrpHdr.MsgId), orgnlMsgId); err != nil {
			h.log.Error("callback.pacs028.process_failed", "error", err)
		}
	}

	utils.SendSuccessResponse(w, "pacs.028 received", map[string]any{
		"messageType": "pacs.028.001.04",
		"msgId":       string(doc.GrpHdr.MsgId),
		"orgnlMsgId":  orgnlMsgId,
	}, http.StatusOK)
}

// ReceiveAdmi002 receives an inbound admi.002 MessageReject from NIBSS.
// NIBSS sends this when a previously submitted message failed schema validation,
// contained invalid/missing fields, or violated structural rules.
// @Summary Receive admi.002
// @Description Callback endpoint to receive an inbound ISO20022 admi.002 message reject
// @Tags ISO20022 Callbacks
// @Accept xml
// @Produce json
// @Success 200 {object} object{messageType=string,orgnlMsgId=string,rejectReason=string}
// @Failure 400 {object} map[string]string
// @Router /admi002 [post]
func (h *ISO20022CallbackHandler) ReceiveAdmi002(w http.ResponseWriter, r *http.Request) {
	h.log.Info("callback.admi002.received")

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		h.log.Error("callback.admi002.read_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to read request body", http.StatusBadRequest, nil)
		return
	}

	h.log.Debug("callback.admi002.raw", "body", string(body))

	var doc struct {
		OrgnlMsgId   string `xml:"MsgRjct>RltdRef>Ref"`
		RejectCode   string `xml:"MsgRjct>Rsn>RjctgPtyRsn"`
		RejectDtTm   string `xml:"MsgRjct>Rsn>RjctnDtTm"`
		RejectDesc   string `xml:"MsgRjct>Rsn>RsnDesc"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		h.log.Error("callback.admi002.parse_failed", "error", err, "body", string(body))
		utils.SendErrorResponse(w, "Invalid admi.002 message", http.StatusBadRequest, nil)
		return
	}

	h.log.Error("callback.admi002.message_rejected",
		"orgnlMsgId", doc.OrgnlMsgId,
		"rejectCode", doc.RejectCode,
		"rejectDtTm", doc.RejectDtTm,
		"rejectDesc", doc.RejectDesc,
	)

	rejectErr := fmt.Errorf("NIBSS rejected message [%s]: %s - %s", doc.RejectCode, doc.RejectDesc, doc.RejectDtTm)

	// Unblock any waiting EnquireName call
	if h.nameSvc != nil && doc.OrgnlMsgId != "" {
		h.nameSvc.NotifyAcmt024(doc.OrgnlMsgId, nil, rejectErr)
	}
	// Unblock any waiting funds transfer call
	if h.fundsSvc != nil && doc.OrgnlMsgId != "" {
		h.fundsSvc.NotifyPacs002(doc.OrgnlMsgId, nil, rejectErr)
	}

	utils.SendSuccessResponse(w, "admi.002 received", map[string]any{
		"messageType": "admi.002.001.01",
		"orgnlMsgId":  doc.OrgnlMsgId,
		"rejectCode":  doc.RejectCode,
		"rejectDtTm":  doc.RejectDtTm,
		"rejectDesc":  doc.RejectDesc,
	}, http.StatusOK)
}
// @Summary Receive acmt.023
// @Description Callback endpoint to receive an inbound ISO20022 acmt.023 identification verification request
// @Tags ISO20022 Callbacks
// @Accept xml
// @Produce json
// @Success 200 {object} object{messageType=string,msgId=string,vrfctnCount=int}
// @Failure 400 {object} map[string]string
// @Router /acmt023 [post]
func (h *ISO20022CallbackHandler) ReceiveAcmt023(w http.ResponseWriter, r *http.Request) {
	h.log.Info("callback.acmt023.received")

	body, err := h.decryptBody(r)
	if err != nil {
		h.log.Error("callback.acmt023.read_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to read request body", http.StatusBadRequest, nil)
		return
	}

	var doc acmt_v02.IdentificationVerificationRequestV02
	if err := xml.Unmarshal(body, &doc); err != nil {
		h.log.Error("callback.acmt023.parse_failed", "error", err)
		utils.SendErrorResponse(w, "Invalid acmt.023 message", http.StatusBadRequest, nil)
		return
	}

	h.log.Info("callback.acmt023.processed",
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
	h.log.Info("callback.acmt024.received")

	body, err := h.decryptBody(r)
	if err != nil {
		h.log.Error("callback.acmt024.read_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to read request body", http.StatusBadRequest, nil)
		return
	}

	var doc acmt_v02.IdentificationVerificationReportV02
	if err := xml.Unmarshal(body, &doc); err != nil {
		h.log.Error("callback.acmt024.parse_failed", "error", err)
		utils.SendErrorResponse(w, "Invalid acmt.024 message", http.StatusBadRequest, nil)
		return
	}

	var verified bool
	var accountName, accountNumber string
	if len(doc.Rpt) > 0 {
		rpt := doc.Rpt[0]
		verified = rpt.Vrfctn
		if rpt.UpdtdPtyAndAcctId != nil && rpt.UpdtdPtyAndAcctId.Pty != nil && rpt.UpdtdPtyAndAcctId.Pty.Nm != nil {
			accountName = string(*rpt.UpdtdPtyAndAcctId.Pty.Nm)
		}
		if rpt.OrgnlPtyAndAcctId != nil && rpt.OrgnlPtyAndAcctId.Acct != nil {
			accountNumber = string(rpt.OrgnlPtyAndAcctId.Acct.IBAN)
		}
	}

	h.log.Info("callback.acmt024.processed",
		"msgId", string(doc.Assgnmt.MsgId),
		"verified", verified,
		"accountName", accountName,
		"accountNumber", accountNumber,
	)

	// Unblock any EnquireName call waiting on this msgId.
	if h.nameSvc != nil {
		h.nameSvc.NotifyAcmt024(string(doc.Assgnmt.MsgId), &models.IdentificationVerificationResponse{
			Verified:      verified,
			AccountName:   accountName,
			AccountNumber: accountNumber,
		}, nil)
	}

	utils.SendSuccessResponse(w, "acmt.024 received", map[string]any{
		"messageType":   "acmt.024.001.02",
		"msgId":         string(doc.Assgnmt.MsgId),
		"verified":      verified,
		"accountName":   accountName,
		"accountNumber": accountNumber,
	}, http.StatusOK)
}
