package services

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/moov-io/iso20022/pkg/common"
	"github.com/moov-io/iso20022/pkg/pacs_v08"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
)

type ISO20022Service struct {
	validator   *ValidationHelper
	nibssClient *NIBSSClient
}

func NewISO20022Service() *ISO20022Service {
	return &ISO20022Service{
		validator:   NewValidationHelper(),
		nibssClient: NewNIBSSClient(),
	}
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

	json.NewEncoder(w).Encode(map[string]any{
		"status":      "converted",
		"messageType": "pacs.008.001.08",
		"xml":         xmlData,
	})
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

	// Create pacs.002 status report
	pacs002, err := iso.CreatePacs002(&req, "ACCP")
	if err != nil {
		utils.SendErrorResponse(w, utils.ResponseMessage(err.Error()), http.StatusFailedDependency, nil)
		return
	}

	// Send to settlement
	resp, err := iso.SendToSettlement(pacs002)
	if err != nil {
		utils.SendErrorResponse(w, utils.ResponseMessage(err.Error()), http.StatusFailedDependency, nil)
		return
	}

	json.NewEncoder(w).Encode(map[string]any{
		"status":      "settled",
		"messageType": "pacs.002.001.08",
		"response":    resp,
	})
}

func (iso *ISO20022Service) ConvertTransaction(tx *models.TransactionRecord) (*pacs_v08.FIToFICustomerCreditTransferV08, error) {
	return iso.CreatePacs008(tx)
}

func (iso *ISO20022Service) SendToSettlement(doc any) (FundsTransferSettlementResponse, error) {
	// Convert to XML
	xmlData, err := iso.ConvertToXML(doc)
	if err != nil {
		return FundsTransferSettlementResponse{}, fmt.Errorf("failed to convert to XML: %w", err)
	}

	v, err := iso.nibssClient.ProcessFundsTransferSettlement([]byte(xmlData))
	if err != nil {
		return FundsTransferSettlementResponse{
			Status: err.Error(),
		}, fmt.Errorf("failed to send to settlement: %w", err)
	}

	if v == nil {
		return FundsTransferSettlementResponse{}, fmt.Errorf("failed to send to settlement: no response")
	}

	if v.Status != "ACCP" && v.Status != "ACSC" {
		return FundsTransferSettlementResponse{}, fmt.Errorf("settlement failed with status: %s, message: %s", v.Status, v.Message)
	}

	fmt.Printf("Settlement successful: TransactionID=%s, Date=%s\n", v.TransactionId, v.TransactionDate)
	return *v, nil
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

// ConvertToXML converts ISO20022 document to XML string
func (iso *ISO20022Service) ConvertToXML(doc any) (string, error) {
	xmlData, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal XML: %w", err)
	}
	return xml.Header + string(xmlData), nil
}
