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

// NIPResponseCode represents NIBSS NIP response codes
type NIPResponseCode string

var nipResponseDescriptions = map[NIPResponseCode]string{
	"00": "Approved Or Completed Successfully",
	"01": "Status Unknown, Please Wait For Settlement Report",
	"03": "Invalid Sender",
	"05": "Do Not Honor",
	"06": "Dormant Account",
	"07": "Invalid Account",
	"08": "Account Name Mismatch",
	"09": "Request Processing In Progress",
	"12": "Invalid Transaction",
	"13": "Invalid Amount",
	"14": "Invalid Batch Number",
	"15": "Invalid Session Or Record ID",
	"16": "Unknown Bank Code",
	"17": "Invalid Channel",
	"18": "Wrong Method Call",
	"21": "No Action Taken",
	"25": "Unable To Locate Record",
	"26": "Duplicate Record",
	"30": "Format Error",
	"34": "Suspected Fraud",
	"35": "Contact Sending Bank",
	"51": "No Sufficient Funds",
	"57": "Transaction Not Permitted To Sender",
	"58": "Transaction Not Permitted On Channel",
	"61": "Transfer Limit Exceeded",
	"63": "Security Violation",
	"65": "Exceeds Withdrawal Frequency",
	"68": "Response Received Too Late",
	"69": "Unsuccessful Account/Amount Block",
	"70": "Unsuccessful Account/Amount Unblock",
	"71": "Empty Mandate Reference Number",
	"91": "Beneficiary Bank Not Available",
	"92": "Routing Error",
	"94": "Duplicate Transaction",
	"96": "System Malfunction",
	"97": "Timeout Waiting For Response From Destination",
	"99": "Error Occurred While Sending Request",
}

func (c NIPResponseCode) Description() string {
	if desc, ok := nipResponseDescriptions[c]; ok {
		return desc
	}
	return fmt.Sprintf("Unknown NIP response code: %s", string(c))
}

// NIPError is a typed error carrying a NIPResponseCode
type NIPError struct {
	Code    NIPResponseCode
	Message string
	Cause   error
}

func (e *NIPError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code.Description()
}

func (e *NIPError) Unwrap() error { return e.Cause }

func NewNIPError(code NIPResponseCode, cause ...error) *NIPError {
	e := &NIPError{Code: code}
	if len(cause) > 0 {
		e.Cause = cause[0]
	}
	return e
}

func NewNIPErrorMsg(code NIPResponseCode, msg string, cause ...error) *NIPError {
	e := &NIPError{Code: code, Message: msg}
	if len(cause) > 0 {
		e.Cause = cause[0]
	}
	return e
}
