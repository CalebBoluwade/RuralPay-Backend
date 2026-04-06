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
	ProcessingFailed     ResponseMessage = "Unable to Process Payment Request at this time"
	InvalidPaymentMode   ResponseMessage = "Invalid Payment Mode"
	OTPGenerationError   ResponseMessage = "Failed to Generate OTP"

	InternalServiceError ResponseMessage = "Internal Service Error"
)

// Success Responses
const (
	UserCreated          ResponseMessage = "User Created Successfully"
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
