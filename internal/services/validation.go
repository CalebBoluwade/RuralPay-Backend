package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/go-playground/validator/v10"
	"github.com/ruralpay/backend/internal/utils"
)

// ValidationHelper provides shared validation functionality
type ValidationHelper struct {
	validator *validator.Validate
}

// NewValidationHelper creates a new validation helper
func NewValidationHelper() *ValidationHelper {
	return &ValidationHelper{
		validator: validator.New(),
	}
}

// ValidateStruct validates a struct and returns validation errors
func (vh *ValidationHelper) ValidateStruct(s any) error {
	return vh.validator.Struct(s)
}

// SendErrorResponse sends a JSON error response
func (vh *ValidationHelper) SendErrorResponse(w http.ResponseWriter, message string, statusCode int, validationErr error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	errorResp := utils.APIErrorResponse{Error: message, Success: false}
	if validationErr != nil {
		errorResp.Details = make(map[string]string)
		for _, err := range validationErr.(validator.ValidationErrors) {
			errorResp.Details[err.Field()] = fmt.Sprintf("Field Validation Failed on '%s' tag", err.Tag())
		}
	}

	json.NewEncoder(w).Encode(errorResp)
}

// Account validation helpers
var (
	accountIdRegex = regexp.MustCompile(`^[0-9]{10,20}$`)
	bankCodeRegex  = regexp.MustCompile(`^[0-9A-Za-z]{3,6}$`)
)

func IsValidAccountId(s string) bool {
	return accountIdRegex.MatchString(s)
}

func IsValidBankCode(s string) bool {
	return bankCodeRegex.MatchString(s)
}
