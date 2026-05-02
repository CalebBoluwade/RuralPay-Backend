package services

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/constants"
	"github.com/ruralpay/backend/internal/models"
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

func newNIBSSISO20022FundsTransferImpl(redis *redis.Client) FundsTransferService {
	return &ISO20022FundsTransferImpl{Redis: redis, ISO20022Service: NewISO20022Service(redis)}
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

type ISO20022FundsTransferImpl struct {
	Redis           *redis.Client
	ISO20022Service *ISO20022Service
}

func (ISOFundTransfer *ISO20022FundsTransferImpl) DoTransaction(ctx context.Context, sessionId string, req *models.PaymentRequest) (*models.FundsTransferResult, error) {
	// Implementation for ISO 20022 Fund transfer
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
		//if _, dbErr := ISOFundTransfer.DB.ExecContext(ctx, `UPDATE transactions SET status = $1, updated_at = NOW() WHERE transaction_id = $2`, models.TransactionStatusISOCONVFailed, req.TransactionID); dbErr != nil {
		//	slog.Error("Fund.Transfer.Settlement.status_update_failed", "tx_id", req.TransactionID, "error", dbErr)
		//}
		return nil, fmt.Errorf("ISO 20020 funds transfer Conversion failed with transaction_id: %s", req.TransactionID)
	}

	slog.Info("Fund.Transfer.Settlement.sending", "tx_id", req.TransactionID)
	resp, err := ISOFundTransfer.ISO20022Service.SendToSettlement(ctx, doc)
	if err != nil {
		slog.Error("Fund.Transfer.Settlement.failed", "tx_id", req.TransactionID, "error", err)
		//shouldReverse := ISOFundTransfer.shouldReverseOnSettlementFailure(resp)
		//if shouldReverse {
		//	if _, dbErr := ISOFundTransfer.DB.ExecContext(ctx, `UPDATE transactions SET status = $1, updated_at = NOW() WHERE transaction_id = $2`, models.TransactionSettlementFailed, req.TransactionID); dbErr != nil {
		//		slog.Error("Fund.Transfer.Settlement.status_update_failed", "tx_id", req.TransactionID, "error", dbErr)
		//	}
		//} else {
		//	if _, dbErr := ISOFundTransfer.DB.ExecContext(ctx, `UPDATE transactions SET status = $1, updated_at = NOW() WHERE transaction_id = $2`, "PENDING_RETRY", req.TransactionID); dbErr != nil {
		//		slog.Error("Fund.Transfer.Settlement.status_update_failed", "tx_id", req.TransactionID, "error", dbErr)
		//	}
		//}
		return nil, err
	}

	return &resp, nil
}
