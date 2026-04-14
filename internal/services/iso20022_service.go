package services

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/moov-io/iso20022/pkg/acmt_v02"
	"github.com/moov-io/iso20022/pkg/common"
	"github.com/moov-io/iso20022/pkg/pacs_v04"
	"github.com/moov-io/iso20022/pkg/pacs_v08"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
	"github.com/spf13/viper"
)

type ISO20022Service struct {
	validator     *ValidationHelper
	nibssClient   *NIBSSClient
	senderPriv    *rsa.PrivateKey
	nibssPub      *rsa.PublicKey
	recipientPriv *rsa.PrivateKey // only used in tests to simulate NIBSS decryption
}

func NewISO20022Service() *ISO20022Service {
	svc := &ISO20022Service{
		validator:   NewValidationHelper(),
		nibssClient: NewNIBSSClient(),
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
func (iso *ISO20022Service) SignXML(xmlData string) (*utils.SignedMessage, error) {
	if iso.senderPriv == nil || iso.nibssPub == nil {
		return nil, fmt.Errorf("signing keys not configured")
	}
	return utils.SealMessage([]byte(xmlData), iso.senderPriv, iso.nibssPub)
}

// VerifyAndOpenXML verifies and decrypts an inbound SignedMessage back to XML.
// Signature is verified against the sender's own public key.
// AES key is unwrapped with recipientPriv when set (test), otherwise senderPriv.
func (iso *ISO20022Service) VerifyAndOpenXML(msg *utils.SignedMessage) (string, error) {
	if iso.senderPriv == nil || iso.nibssPub == nil {
		return "", fmt.Errorf("signing keys not configured")
	}
	unwrapKey := iso.senderPriv
	if iso.recipientPriv != nil {
		unwrapKey = iso.recipientPriv
	}
	plaintext, err := utils.OpenMessage(msg, &iso.senderPriv.PublicKey, unwrapKey)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// ConvertToISO20022 converts transaction to ISO20022 format
// @Summary Convert to ISO20022
// @Description Convert transaction data to ISO20022 XML format
// @Tags iso20022
// @Accept json
// @Produce json
// @Param transaction body models.TransactionRecord true "Transaction to convert"
// @Success 200 {object} object{status=string,messageType=string,xml=string}
// @Failure 500 {object} map[string]string
// @Router /iso20022/convert [post]
func (iso *ISO20022Service) ConvertToISO20022(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var req models.TransactionRecord
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.SendErrorResponse(w, "Unable To Process This Request At This Time", http.StatusBadRequest, nil)
		return
	}

	if err := iso.validator.ValidateStruct(&req); err != nil {
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	// Create pacs.008 document
	pacs008, err := iso.CreatePacs008(&req)
	if err != nil {
		utils.SendErrorResponse(w, utils.ResponseMessage(err.Error()), http.StatusFailedDependency, nil)
		return
	}

	// Convert to XML
	xmlData, err := iso.ConvertToXML(pacs008)
	if err != nil {
		utils.SendErrorResponse(w, utils.ResponseMessage(err.Error()), http.StatusFailedDependency, nil)
		return
	}

	utils.SendSuccessResponse(w, "Successfully converted to ISO20022", map[string]any{
		"messageType": "pacs.008.001.08",
		"xml":         xmlData,
	}, http.StatusOK)
}

// ProcessSettlement processes transaction settlement
// @Summary Process settlement
// @Description Process transaction settlement using ISO20022
// @Tags iso20022
// @Accept json
// @Produce json
// @Param transaction body models.TransactionRecord true "Transaction to settle"
// @Success 200 {object} object{status=string,messageType=string}
// @Failure 500 {object} map[string]string
// @Router /iso20022/settlement [post]
func (iso *ISO20022Service) ProcessSettlement(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var req models.TransactionRecord
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.SendErrorResponse(w, "Unable To Process This Request At This Time", http.StatusBadRequest, nil)
		return
	}

	if err := iso.validator.ValidateStruct(&req); err != nil {
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	pacs008, err := iso.CreatePacs008(&req)
	if err != nil {
		utils.SendErrorResponse(w, utils.ResponseMessage(err.Error()), http.StatusFailedDependency, nil)
		return
	}

	resp, err := iso.SendToSettlement(r.Context(), pacs008)
	if err != nil {
		utils.SendErrorResponse(w, utils.ResponseMessage(err.Error()), http.StatusFailedDependency, nil)
		return
	}

	utils.SendSuccessResponse(w, "Successful", map[string]any{
		"messageType": "pacs.002.001.08",
		"response":    resp,
	}, http.StatusOK)
}

func (iso *ISO20022Service) ConvertTransaction(tx *models.TransactionRecord) (*pacs_v08.FIToFICustomerCreditTransferV08, error) {
	return iso.CreatePacs008(tx)
}

func (iso *ISO20022Service) SendToSettlement(ctx context.Context, pacs008 *pacs_v08.FIToFICustomerCreditTransferV08) (models.SettlementResult, error) {
	xmlData, err := iso.ConvertToXML(pacs008)
	if err != nil {
		return models.SettlementResult{}, fmt.Errorf("failed to convert pacs.008 to XML: %w", err)
	}

	payload := []byte(xmlData)
	if iso.senderPriv != nil && iso.nibssPub != nil {
		signed, err := iso.SignXML(xmlData)
		if err != nil {
			return models.SettlementResult{}, fmt.Errorf("failed to sign pacs.008: %w", err)
		}
		payload, err = json.Marshal(signed)
		if err != nil {
			return models.SettlementResult{}, fmt.Errorf("failed to marshal signed message: %w", err)
		}
	}

	pacs002, err := iso.nibssClient.ProcessFundsTransferSettlement(ctx, payload)
	if err != nil {
		return models.SettlementResult{}, fmt.Errorf("failed to send pacs.008 to settlement: %w", err)
	}

	if pacs002 == nil || len(pacs002.TxInfAndSts) == 0 {
		return models.SettlementResult{}, fmt.Errorf("pacs.002 response missing TxInfAndSts")
	}

	txSts := pacs002.TxInfAndSts[0]
	if txSts.TxSts == nil {
		return models.SettlementResult{}, fmt.Errorf("pacs.002 TxSts is nil")
	}

	status := string(*txSts.TxSts)
	result := models.SettlementResult{Status: status}

	if txSts.OrgnlTxId != nil {
		result.TransactionID = string(*txSts.OrgnlTxId)
	}

	if status == "RJCT" && len(txSts.StsRsnInf) > 0 && txSts.StsRsnInf[0].Rsn != nil && txSts.StsRsnInf[0].Rsn.Cd != nil {
		result.RejectReason = string(*txSts.StsRsnInf[0].Rsn.Cd)
	}

	if status != "ACSC" && status != "ACCP" {
		return result, fmt.Errorf("settlement rejected with status: %s, reason: %s", status, result.RejectReason)
	}

	return result, nil
}

// CreatePacs008 creates a pacs.008 FIToFICustomerCreditTransfer message
func (iso *ISO20022Service) CreatePacs008(tx *models.TransactionRecord) (*pacs_v08.FIToFICustomerCreditTransferV08, error) {
	msgId := uuid.New().String()
	creDtTm := time.Now()
	settlementDate := time.Now()

	doc := &pacs_v08.FIToFICustomerCreditTransferV08{
		GrpHdr: pacs_v08.GroupHeader93{
			MsgId:   common.Max35Text(msgId),
			CreDtTm: common.ISODateTime(creDtTm),
			NbOfTxs: "1",
			TtlIntrBkSttlmAmt: &pacs_v08.ActiveCurrencyAndAmount{
				Ccy:   common.ActiveCurrencyCode(tx.Currency),
				Value: float64(tx.Amount) / 100.0,
			},
			IntrBkSttlmDt: (*common.ISODate)(&settlementDate),
			SttlmInf: pacs_v08.SettlementInstruction7{
				SttlmMtd: "CLRG", // Clearing
			},
		},
		CdtTrfTxInf: []pacs_v08.CreditTransferTransaction39{
			{
				PmtId: pacs_v08.PaymentIdentification7{
					InstrId:    &[]common.Max35Text{common.Max35Text(tx.TransactionID)}[0],
					EndToEndId: common.Max35Text(tx.TransactionID),
					TxId:       &[]common.Max35Text{common.Max35Text(tx.TransactionID)}[0],
				},
				IntrBkSttlmAmt: pacs_v08.ActiveCurrencyAndAmount{
					Ccy:   common.ActiveCurrencyCode(tx.Currency),
					Value: float64(tx.Amount) / 100.0,
				},
				IntrBkSttlmDt: (*common.ISODate)(&settlementDate),
				ChrgBr:        "SLEV",
				DbtrAgt: pacs_v08.BranchAndFinancialInstitutionIdentification6{
					FinInstnId: pacs_v08.FinancialInstitutionIdentification18{
						BICFI: &[]common.BICFIDec2014Identifier{common.BICFIDec2014Identifier("RURALPAY")}[0],
					},
				},
				Dbtr: pacs_v08.PartyIdentification135{
					Nm: &[]common.Max140Text{common.Max140Text(tx.FromAccountID)}[0],
				},
				CdtrAgt: pacs_v08.BranchAndFinancialInstitutionIdentification6{
					FinInstnId: pacs_v08.FinancialInstitutionIdentification18{
						ClrSysMmbId: &pacs_v08.ClearingSystemMemberIdentification2{
							MmbId: common.Max35Text(tx.ToBankCode),
						},
					},
				},
				Cdtr: pacs_v08.PartyIdentification135{
					Nm: &[]common.Max140Text{common.Max140Text(tx.ToAccountID)}[0],
				},
			},
		},
	}

	return doc, nil
}

// CreatePacs002 creates a pacs.002 payment status report
func (iso *ISO20022Service) CreatePacs002(tx *models.TransactionRecord, status string) (*pacs_v08.FIToFIPaymentStatusReportV08, error) {
	msgId := uuid.New().String()
	creDtTm := time.Now()

	doc := &pacs_v08.FIToFIPaymentStatusReportV08{
		GrpHdr: pacs_v08.GroupHeader53{
			MsgId:   common.Max35Text(msgId),
			CreDtTm: common.ISODateTime(creDtTm),
		},
		TxInfAndSts: []pacs_v08.PaymentTransaction80{
			{
				OrgnlInstrId:    &[]common.Max35Text{common.Max35Text(tx.TransactionID)}[0],
				OrgnlEndToEndId: &[]common.Max35Text{common.Max35Text(tx.TransactionID)}[0],
				OrgnlTxId:       &[]common.Max35Text{common.Max35Text(tx.TransactionID)}[0],
				TxSts:           &[]pacs_v08.ExternalPaymentTransactionStatus1Code{pacs_v08.ExternalPaymentTransactionStatus1Code(status)}[0], // ACCP, RJCT, ACSC, etc.
			},
		},
	}

	return doc, nil
}

// CreateAcmt023 builds an acmt.023 Identification Verification Request for the given account
func (iso *ISO20022Service) CreateAcmt023(accountNumber, bankCode string) (*acmt_v02.IdentificationVerificationRequestV02, error) {
	msgID := common.Max35Text(uuid.New().String())
	now := common.ISODateTime(time.Now())

	doc := &acmt_v02.IdentificationVerificationRequestV02{
		Assgnmt: acmt_v02.IdentificationAssignment2{
			MsgId:   msgID,
			CreDtTm: now,
			Assgnr:  acmt_v02.Party12Choice{Pty: acmt_v02.PartyIdentification43{}},
			Assgne:  acmt_v02.Party12Choice{Pty: acmt_v02.PartyIdentification43{Nm: ptr140(bankCode)}},
		},
		Vrfctn: []acmt_v02.IdentificationVerification2{
			{
				Id: msgID,
				PtyAndAcctId: acmt_v02.IdentificationInformation2{
					Acct: &acmt_v02.AccountIdentification4Choice{
						Othr: acmt_v02.GenericAccountIdentification1{
							Id: common.Max34Text(accountNumber),
						},
					},
				},
			},
		},
	}
	return doc, nil
}

func ptr140(s string) *common.Max140Text { return new(common.Max140Text(s)) }
func ptr35(s string) *common.Max35Text   { return new(common.Max35Text(s)) }

// CreatePacs028 builds a pacs.028 FIToFIPaymentStatusRequest for the given original transaction
func (iso *ISO20022Service) CreatePacs028(originalMsgID, originalTxID string) (*pacs_v04.FIToFIPaymentStatusRequestV04, error) {
	if originalMsgID == "" || originalTxID == "" {
		return nil, fmt.Errorf("originalMsgID and originalTxID are required")
	}

	doc := &pacs_v04.FIToFIPaymentStatusRequestV04{
		GrpHdr: pacs_v04.GroupHeader91{
			MsgId:   common.Max35Text(uuid.New().String()),
			CreDtTm: common.ISODateTime(time.Now()),
		},
		OrgnlGrpInf: []pacs_v04.OriginalGroupInformation27{
			{
				OrgnlMsgId:   common.Max35Text(originalMsgID),
				OrgnlMsgNmId: "pacs.008.001.08",
			},
		},
		TxInf: []pacs_v04.PaymentTransaction121{
			{
				OrgnlInstrId:    ptr35(originalTxID),
				OrgnlEndToEndId: ptr35(originalTxID),
				OrgnlTxId:       ptr35(originalTxID),
			},
		},
	}
	return doc, nil
}

// RequestPaymentStatus sends a pacs.028 to NIBSS and returns the pacs.002 status response
func (iso *ISO20022Service) RequestPaymentStatus(ctx context.Context, originalMsgID, originalTxID string) (models.SettlementResult, error) {
	pacs028, err := iso.CreatePacs028(originalMsgID, originalTxID)
	if err != nil {
		return models.SettlementResult{}, fmt.Errorf("failed to build pacs.028: %w", err)
	}

	xmlData, err := iso.ConvertToXML(pacs028)
	if err != nil {
		return models.SettlementResult{}, fmt.Errorf("failed to convert pacs.028 to XML: %w", err)
	}

	pacs002, err := iso.nibssClient.RequestPaymentStatus(ctx, []byte(xmlData))
	if err != nil {
		return models.SettlementResult{}, fmt.Errorf("failed to request payment status: %w", err)
	}

	if pacs002 == nil || len(pacs002.TxInfAndSts) == 0 {
		return models.SettlementResult{}, fmt.Errorf("pacs.002 response missing TxInfAndSts")
	}

	txSts := pacs002.TxInfAndSts[0]
	if txSts.TxSts == nil {
		return models.SettlementResult{}, fmt.Errorf("pacs.002 TxSts is nil")
	}

	status := string(*txSts.TxSts)
	result := models.SettlementResult{Status: status}

	if txSts.OrgnlTxId != nil {
		result.TransactionID = string(*txSts.OrgnlTxId)
	}

	if status == "RJCT" && len(txSts.StsRsnInf) > 0 && txSts.StsRsnInf[0].Rsn != nil && txSts.StsRsnInf[0].Rsn.Cd != nil {
		result.RejectReason = string(*txSts.StsRsnInf[0].Rsn.Cd)
	}

	return result, nil
}

// ConvertToXML converts ISO20022 document to XML string
func (iso *ISO20022Service) ConvertToXML(doc any) (string, error) {
	xmlData, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal XML: %w", err)
	}
	return xml.Header + string(xmlData), nil
}
