package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/moov-io/iso20022/pkg/pacs_v08"
	"github.com/ruralpay/backend/internal/constants"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
	"github.com/spf13/viper"
)

type NIPFundsTransferImpl struct {
	Redis      *redis.Client
	NIPService *NIBSSNIPService
}

// FundsTransferService abstracts fund transfer operations.
// Callers never know or care whether NIP or ISO 20022 is used underneath.
type FundsTransferService interface {
	DoTransaction(ctx context.Context, sessionId string, req *models.PaymentRequest) (*models.FundsTransferResult, error)
}

func NewFundsTransferService(useNIBSSISOzNIPSwitch bool, redis *redis.Client) FundsTransferService {
	// Return the default implementation (NIP or ISO 20022)
	switch useNIBSSISOzNIPSwitch {
	case true:
		return newNIBSSISO20022FundsTransferImpl(redis)
	case false:
		return newNIPFundsTransferImpl(redis)
	default:
		return newNIBSSISO20022FundsTransferImpl(redis)
	}
}

func newNIPFundsTransferImpl(redis *redis.Client) FundsTransferService {
	return &NIPFundsTransferImpl{Redis: redis, NIPService: NewNIBSSNIPService()}
}

func (s *NIPFundsTransferImpl) DoTransaction(ctx context.Context, sessionId string, req *models.PaymentRequest) (*models.FundsTransferResult, error) {
	// Implementation for NIP fund transfer

	user := models.UserTransactionMetaData{
		NameEnquiryBeneficiaryAccountName:            "OgeTest",
		NameEnquiryBeneficiaryAccountNumber:          req.BeneficiaryAccountNumber,
		NameEnquiryBeneficiaryBankVerificationNumber: "22222222280",
		NameEnquiryBeneficiaryKYCLevel:               "1",
		NameEnquiryBeneficiaryBankCode:               fmt.Sprintf("%06s", req.BeneficiaryBankCode),

		DebitMandateCode:            "0220310/003/0000072702",
		DebitAccountName:            "John Doe",
		DebitAccountNumber:          req.FromAccount,
		DebitBankVerificationNumber: "22222222280",
		DebitKYCLevel:               "1",
	}

	if s.Redis != nil {
		// Fetch Name Enquiry Details For The Transaction
		err := s.Redis.HGetAll(ctx, fmt.Sprintf(constants.NameEnquiryMetadataKeyPrefix, req.TransactionID)).Scan(&user)
		if err != nil {
			slog.Error("funds_transfer.redis_user_metadata_failed", "account", req.FromAccount, "error", err)
			return nil, fmt.Errorf("failed to fetch user metadata from Redis: %w", err)
		}

		slog.Debug("funds_transfer.redis_user_metadata_fetched", "account", req.FromAccount, "user_metadata", user)
	}

	slog.Info("funds_transfer.start", "from_account", req.FromAccount, "to_account", req.BeneficiaryAccountNumber, "amount", req.Amount)
	debit, err := s.NIPService.ExecuteFundsTransferDebit(ctx, &models.FTSingleDebitRequest{
		// XMLName: ,
		SessionID:      sessionId,
		ChannelCode:    "1",
		Amount:         fmt.Sprintf("%d", req.Amount),
		TransactionFee: "0.00",
		Narration:      req.Narration,

		DestinationInstitutionCode: fmt.Sprintf("%06s", req.BeneficiaryBankCode),

		DebitAccountNumber:          user.DebitAccountNumber,
		DebitAccountName:            user.DebitAccountName,
		DebitBankVerificationNumber: user.DebitBankVerificationNumber,
		DebitKYCLevel:               user.DebitKYCLevel,
		MandateReferenceNumber:      user.DebitMandateCode,

		BeneficiaryAccountNumber:          user.NameEnquiryBeneficiaryAccountNumber,
		BeneficiaryAccountName:            user.NameEnquiryBeneficiaryAccountName,
		BeneficiaryBankVerificationNumber: user.NameEnquiryBeneficiaryBankVerificationNumber,
		BeneficiaryKYCLevel:               "1",

		TransactionLocation: fmt.Sprintf("%v,%v", req.Location.Latitude, req.Location.Longitude),
		PaymentReference:    fmt.Sprintf("%s/%s", s.NIPService.GetNIPPaymentPrefix(), user.DebitMandateCode),
	})

	if err != nil {
		slog.Error("funds_transfer.debit_failed", "error", err)
		return nil, err
	}

	if debit.ResponseCode == "00" {
		// Assuming debit is successful for demo purposes. In real implementation, use actual response code.

		credit, err := s.NIPService.ExecuteFundsTransferCredit(ctx, &models.FTSingleCreditRequest{
			// XMLName: ,
			SessionID:                  sessionId,
			DestinationInstitutionCode: req.Metadata["toBankCode"].(string),
			DebitAccountNumber:         req.FromAccount,
			//    req.BeneficiaryAccountNumber,
			Amount:    fmt.Sprintf("%d", req.Amount),
			Narration: fmt.Sprintf("Transfer from %s", req.FromAccount),
		})

		if err != nil {
			slog.Error("funds_transfer.credit_failed", "error", err)
			// Optionally, implement reversal logic here if credit fails after successful debit
			return nil, err
		}

		if credit.ResponseCode != "00" {
			slog.Error("funds_transfer.credit_response_failed", "response_code", credit.ResponseCode)
			// Optionally, implement reversal logic here if credit fails after successful debit
			return nil, fmt.Errorf("credit failed with response code: %s", credit.ResponseCode)
		}

		return &models.FundsTransferResult{
			SessionID: sessionId,
			Reference: debit.SessionID,
			Status:    "SUCCESS",
		}, nil
	}

	return &models.FundsTransferResult{
		SessionID: sessionId,
		Reference: debit.SessionID,
		Status:    "FAILED",
	}, fmt.Errorf("funds transfer failed with response code: %s", debit.ResponseCode)
}

type pacs002Result struct {
	resp *models.FundsTransferResult
	err  error
}

// PACS002Notifier is implemented by ISO20022FundsTransferImpl.
// The callback handler calls it when NIBSS delivers a pacs.002 settlement result.
type PACS002Notifier interface {
	NotifyPacs002(orgnlMsgID string, result *models.FundsTransferResult, err error)
}

// PACS008Processor is called by the callback handler when NIBSS sends us an
// inbound pacs.008 credit transfer (another bank crediting one of our customers).
type PACS008Processor interface {
	ProcessInboundPacs008(ctx context.Context, msgID, nbOfTxs string) error
}

// PACS028Processor is called by the callback handler when NIBSS sends us a
// pacs.028 payment status request (asking for the status of a prior transaction).
type PACS028Processor interface {
	ProcessInboundPacs028(ctx context.Context, msgID, orgnlMsgID string) error
}

type ISO20022FundsTransferImpl struct {
	Redis           *redis.Client
	ISO20022Service *ISO20022Service
	pacsTimeout     time.Duration
	pending         sync.Map // pacs.008 MsgId -> chan pacs002Result
}

func newNIBSSISO20022FundsTransferImpl(redis *redis.Client) FundsTransferService {
	return &ISO20022FundsTransferImpl{
		Redis:           redis,
		ISO20022Service: NewISO20022Service(redis),
		pacsTimeout:     utils.GetTimeout("nibss.pacs_timeout", 45),
	}
}

// NotifyPacs002 is called by the callback handler when NIBSS delivers a pacs.002.
// It unblocks the DoTransaction call waiting on orgnlMsgID.
func (s *ISO20022FundsTransferImpl) NotifyPacs002(orgnlMsgID string, result *models.FundsTransferResult, err error) {
	if ch, ok := s.pending.LoadAndDelete(orgnlMsgID); ok {
		ch.(chan pacs002Result) <- pacs002Result{resp: result, err: err}
	}
}

// ProcessInboundPacs008 handles a pacs.008 credit transfer sent to us by another bank.
func (s *ISO20022FundsTransferImpl) ProcessInboundPacs008(ctx context.Context, msgID, nbOfTxs string) error {
	slog.Info("pacs008.inbound.processing", "msgId", msgID, "nbOfTxs", nbOfTxs)
	// TODO: look up the beneficiary account from the pacs.008 CdtTrfTxInf and post the credit.
	return nil
}

// ProcessInboundPacs028 handles a pacs.028 payment status request from NIBSS.
func (s *ISO20022FundsTransferImpl) ProcessInboundPacs028(ctx context.Context, msgID, orgnlMsgID string) error {
	slog.Info("pacs028.inbound.processing", "msgId", msgID, "orgnlMsgId", orgnlMsgID)
	// TODO: look up the original transaction by orgnlMsgID and send back a pacs.002 status report.
	return nil
}

func (ISOFundTransfer *ISO20022FundsTransferImpl) DoTransaction(ctx context.Context, sessionId string, req *models.PaymentRequest) (*models.FundsTransferResult, error) {
	modelTx := &models.TransactionRecord{
		TransactionID:      req.TransactionID,
		BeneficiaryAccount: req.BeneficiaryAccountNumber,
		OriginatorAccount:  req.FromAccount,
		Amount:             req.Amount,
		Narration:          req.Narration,
		Metadata:           req.Metadata,
	}

	doc, err := ISOFundTransfer.ISO20022Service.CreatePacs008(modelTx)
	if err != nil {
		slog.Error("Fund.Transfer.Settlement.iso_conversion_failed", "tx_id", req.TransactionID, "error", err)
		return nil, fmt.Errorf("ISO 20022 funds transfer conversion failed with transaction_id: %s", req.TransactionID)
	}

	msgID := string(doc.GrpHdr.MsgId)
	resultCh := make(chan pacs002Result, 1)
	ISOFundTransfer.pending.Store(msgID, resultCh)
	defer ISOFundTransfer.pending.Delete(msgID)

	slog.Info("Fund.Transfer.Settlement.sending", "tx_id", req.TransactionID, "pacs008_msg_id", msgID)
	if err := ISOFundTransfer.sendPacs008(ctx, doc); err != nil {
		slog.Error("Fund.Transfer.Settlement.failed", "tx_id", req.TransactionID, "error", err)
		return nil, err
	}

	// Block until the pacs.002 callback arrives or we time out.
	timerCtx, cancel := context.WithTimeout(ctx, ISOFundTransfer.pacsTimeout)
	defer cancel()

	select {
	case res := <-resultCh:
		if res.err != nil {
			return nil, res.err
		}
		return res.resp, nil
	case <-timerCtx.Done():
		return nil, fmt.Errorf("timed out waiting for pacs.002 settlement callback for tx %s", req.TransactionID)
	}
}

func (s *ISO20022FundsTransferImpl) sendPacs008(ctx context.Context, doc *pacs_v08.FIToFICustomerCreditTransferV08) error {
	xmlData, err := s.ISO20022Service.ConvertPacs008ToXML(doc)
	if err != nil {
		return fmt.Errorf("failed to convert pacs.008 to XML: %w", err)
	}
	payload := []byte(xmlData)

	_, err = s.ISO20022Service.ISO20022Breaker.Execute(func() (any, error) {
		httpCtx, cancel := context.WithTimeout(context.Background(), utils.GetTimeout("nibss.acmt_send_timeout", 10))
		defer cancel()

		req, err := http.NewRequestWithContext(httpCtx, "POST", s.ISO20022Service.GetPACSBaseURL(), bytes.NewBuffer(payload))
		if err != nil {
			return nil, fmt.Errorf("failed to create pacs.008 request: %w", err)
		}
		req.Header.Set(constants.ContentType, "text/xml; charset=utf-8")
		req.Header.Set("Accept", "text/xml")
		if apiKey := viper.GetString("nibss.api_key"); apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := s.ISO20022Service.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("pacs.008 request failed: %w", err)
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read pacs.008 response body: %w", err)
		}
		slog.Debug("pacs.008 response", "status", resp.StatusCode, "body", string(respBody))

		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("NIBSS pacs.008 API returned status %d: %s", resp.StatusCode, string(respBody))
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("NIBSS pacs.008 API returned status %d: %s", resp.StatusCode, string(respBody))
		}
		slog.Debug("pacs.008 accepted", "status", resp.StatusCode)
		return nil, nil
	})
	return err
}
