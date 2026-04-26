package utils

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-playground/validator/v10"
)

type ResponseMessage string

var (
	ErrSessionNotFound = errors.New("Session Not Found")
	ErrInvalidSession  = errors.New("Invalid Session")
	ErrDeviceMismatch  = errors.New("Device Mismatch")
)

// Error Responses
const (
	InvalidCreds         ResponseMessage = "Invalid Credentials"
	AccountNotFoundError ResponseMessage = "Account Not Found"
	UserNotFoundError    ResponseMessage = "User Not Found"
	GenerateTokenError   ResponseMessage = "Failed to Generate Token"
	TokenError           ResponseMessage = "Invalid or Expired Reset Token"
	OTPError             ResponseMessage = "Invalid or Expired OTP"
	PasswordResetError   ResponseMessage = "Failed to Reset Password"
	ValidationError      ResponseMessage = "Validation Failed"
	MultiFactorAuthError ResponseMessage = "Multi Factor Validation Failed"
	InvalidRequestError  ResponseMessage = "Invalid Request Body"
	UnauthorizedError    ResponseMessage = "Unauthorized User Access"
	SingleObjectError    ResponseMessage = "Request Body Must Only Contain a Single JSON Object"
	FetchTransaction     ResponseMessage = "Failed to Fetch Transaction"
	SingleLimitError     ResponseMessage = "Single Transaction Limit Cannot Exceed Daily Limit"
	PaymentFailed        ResponseMessage = "Payment Processing Failed"
	InvalidPaymentMode   ResponseMessage = "Invalid Payment Mode"
	ProcessingFailed     ResponseMessage = "Unable to Process Request at this time"
	OTPGenerationError   ResponseMessage = "Failed to Generate OTP"

	InternalServiceError ResponseMessage = "Internal Service Error"
)

// Success Responses
const (
	UserCreated          ResponseMessage = "User Created Successfully"
	AccountFound         ResponseMessage = "Account Found Successfully"
	LoginSuccess         ResponseMessage = "Login Successful"
	ResetLinkSent        ResponseMessage = "Password Reset Link Sent to your Email Address"
	OTPSent              ResponseMessage = "OTP Sent to your Email Address"
	PasswordResetSuccess ResponseMessage = "Password Reset Successful"
	TransactionFetched   ResponseMessage = "Transaction Data Fetched Successfully"
	PaymentInitiated     ResponseMessage = "Payment Initiated Successfully"
	PaymentSuccessful    ResponseMessage = "Payment Successful"
)

func (e ResponseMessage) Response() string {
	return string(e)
}

type APIErrorResponse struct {
	Error   string            `json:"errorMessage"`
	Success bool              `json:"success"`
	Details map[string]string `json:"details,omitempty"`
}

type APISuccessResponse struct {
	Message string `json:"message"`
	Success bool   `json:"success"`
	Details any    `json:"details,omitempty"`
}

func SendErrorResponse(w http.ResponseWriter, message ResponseMessage, statusCode int, validationErr error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	errorResp := APIErrorResponse{Error: message.Response(), Success: false}
	if validationErr != nil {
		errorResp.Details = make(map[string]string)
		for _, err := range validationErr.(validator.ValidationErrors) {
			errorResp.Details[err.Field()] = fmt.Sprintf("Field Validation Failed on '%s' tag", err.Tag())
		}
	}

	json.NewEncoder(w).Encode(errorResp)
}

func SendSuccessResponse(w http.ResponseWriter, message ResponseMessage, details any, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	// w.Header().Set("Cache-Control", "public, max-age=86400")

	successResp := APISuccessResponse{Message: message.Response(), Success: true, Details: details}
	json.NewEncoder(w).Encode(successResp)
}

// NipResponseCode represents NIBSS NIP response codes
type NipResponseCode string

const (
	NipApproved                         NipResponseCode = "00"
	NipStatusUnknown                    NipResponseCode = "01"
	NipInvalidSender                    NipResponseCode = "03"
	NipDoNotHonor                       NipResponseCode = "05"
	NipDormantAccount                   NipResponseCode = "06"
	NipInvalidAccount                   NipResponseCode = "07"
	NipAccountNameMismatch              NipResponseCode = "08"
	NipProcessing                       NipResponseCode = "09"
	NipInvalidTransaction               NipResponseCode = "12"
	NipInvalidAmount                    NipResponseCode = "13"
	NipInvalidBatchNumber               NipResponseCode = "14"
	NipInvalidSessionRecordID           NipResponseCode = "15"
	NipUnknownBankCode                  NipResponseCode = "16"
	NipInvalidChannel                   NipResponseCode = "17"
	NipWrongMethodCall                  NipResponseCode = "18"
	NipNoActionTaken                    NipResponseCode = "21"
	NipUnableToLocateRecord             NipResponseCode = "25"
	NipDuplicateRecord                  NipResponseCode = "26"
	NipFormatError                      NipResponseCode = "30"
	NipSuspectedFraud                   NipResponseCode = "34"
	NipContactSendingBank               NipResponseCode = "35"
	NipNoSufficientFunds                NipResponseCode = "51"
	NipTransactionNotPermittedOnSender  NipResponseCode = "57"
	NipTransactionNotPermittedOnChannel NipResponseCode = "58"
	NipTransferLimitExceeded            NipResponseCode = "61"
	NipSecurityViolation                NipResponseCode = "63"
	NipExceedsWithdrawalFrequency       NipResponseCode = "65"
	NipResponseTooLate                  NipResponseCode = "68"
	NipUnsuccessfulAccountAmountBlock   NipResponseCode = "69"
	NipUnsuccessfulAccountAmountUnblock NipResponseCode = "70"
	NipEmptyMandateRef                  NipResponseCode = "71"
	NipBeneficiaryBankUnavailable       NipResponseCode = "91"
	NipRoutingError                     NipResponseCode = "92"
	NipDuplicateTransaction             NipResponseCode = "94"
	NipSystemMalfunction                NipResponseCode = "96"
	NipTimeoutWaitingForResponse        NipResponseCode = "97"
	NipInternalServerError              NipResponseCode = "99"
)

var nipResponseDescriptions = map[NipResponseCode]string{
	NipApproved:                         "Approved or completed successfully",
	NipStatusUnknown:                    "Status unknown, please wait for settlement report",
	NipInvalidSender:                    "Invalid Sender",
	NipDoNotHonor:                       "Do not honor",
	NipDormantAccount:                   "Dormant Account",
	NipInvalidAccount:                   "Invalid Account",
	NipAccountNameMismatch:              "Account Name Mismatch",
	NipProcessing:                       "Request processing in progress",
	NipInvalidTransaction:               "Invalid transaction",
	NipInvalidAmount:                    "Invalid Amount",
	NipInvalidBatchNumber:               "Invalid Batch Number",
	NipInvalidSessionRecordID:           "Invalid Session or Record ID",
	NipUnknownBankCode:                  "Unknown Bank Code",
	NipInvalidChannel:                   "Invalid Channel",
	NipWrongMethodCall:                  "Wrong Method Call",
	NipNoActionTaken:                    "No action taken",
	NipUnableToLocateRecord:             "Unable to locate record",
	NipDuplicateRecord:                  "Duplicate record",
	NipFormatError:                      "Format error",
	NipSuspectedFraud:                   "Suspected fraud",
	NipContactSendingBank:               "Contact sending bank",
	NipNoSufficientFunds:                "No sufficient funds",
	NipTransactionNotPermittedOnSender:  "Transaction not permitted to sender",
	NipTransactionNotPermittedOnChannel: "Transaction not permitted on channel",
	NipTransferLimitExceeded:            "Transfer limit Exceeded",
	NipSecurityViolation:                "Security violation",
	NipExceedsWithdrawalFrequency:       "Exceeds withdrawal frequency",
	NipResponseTooLate:                  "Response received too late",
	NipUnsuccessfulAccountAmountBlock:   "Unsuccessful Account/Amount block",
	NipUnsuccessfulAccountAmountUnblock: "Unsuccessful Account/Amount unblock",
	NipEmptyMandateRef:                  "Empty Mandate Reference Number",
	NipBeneficiaryBankUnavailable:       "Beneficiary Bank not available",
	NipRoutingError:                     "Routing error",
	NipDuplicateTransaction:             "Duplicate transaction",
	NipSystemMalfunction:                "System malfunction",
	NipTimeoutWaitingForResponse:        "Timeout waiting for response from destination",
	NipInternalServerError:              "Error occurred while sending request",
}

func (c NipResponseCode) Description() string {
	if desc, ok := nipResponseDescriptions[c]; ok {
		return desc
	}
	return fmt.Sprintf("Unknown NIP response code: %s", string(c))
}

// NipError is a typed error carrying a NipResponseCode
type NipError struct {
	Code    NipResponseCode
	Message string
	Cause   error
}

func (e *NipError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code.Description()
}

func (e *NipError) Unwrap() error { return e.Cause }

func NewNipError(code NipResponseCode, cause ...error) *NipError {
	e := &NipError{Code: code}
	if len(cause) > 0 {
		e.Cause = cause[0]
	}
	return e
}

func NewNipErrorMsg(code NipResponseCode, msg string, cause ...error) *NipError {
	e := &NipError{Code: code, Message: msg}
	if len(cause) > 0 {
		e.Cause = cause[0]
	}
	return e
}
