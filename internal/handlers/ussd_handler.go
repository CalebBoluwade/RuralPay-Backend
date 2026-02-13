package handlers

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/ruralpay/backend/internal/services"
)

type USSDHandler struct {
	service   *services.USSDService
	validator *services.ValidationHelper
}

func NewUSSDHandler(service *services.USSDService) *USSDHandler {
	return &USSDHandler{
		service:   service,
		validator: services.NewValidationHelper(),
	}
}

// GenerateCode generates a USSD code for send (push) or receive (pull) payment
// @Summary Generate USSD Code
// @Description Generate a cryptographically secure USSD code based on type
// @Tags USSD
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body object{type=string,amount=int64,currency=string} true "USSD code request"
// @Success 200 {object} object{code=string}
// @Failure 400 {object} services.ErrorResponse
// @Failure 401 {object} services.ErrorResponse
// @Failure 500 {object} services.ErrorResponse
// @Router /ussd/generate [post]
func (h *USSDHandler) GenerateCode(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value("userID").(string)
	log.Printf("[USSD] GenerateCode - userID from context: %v, ok: %v", userID, ok)
	if !ok || userID == "" {
		log.Printf("[USSD] GenerateCode - Unauthorized: userID missing or invalid")
		services.SendErrorResponse(w, "Unauthorized", http.StatusUnauthorized, nil)
		return
	}

	var req struct {
		Type     string `json:"type" validate:"required,oneof=Send Receive"`
		Amount   int64  `json:"amount" validate:"required,gt=0"`
		Currency string `json:"currency,omitempty"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		log.Printf("[USSD] GenerateCode - Decode error: %v", err)
		services.SendErrorResponse(w, "Unable To Process This Request At This Time", http.StatusBadRequest, nil)
		return
	}

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		log.Printf("[USSD] GenerateCode - Multiple JSON objects detected")
		services.SendErrorResponse(w, "Request body must only contain a single JSON object", http.StatusBadRequest, nil)
		return
	}

	log.Printf("[USSD] GenerateCode - Request: type=%s, amount=%d, currency=%s", req.Type, req.Amount, req.Currency)

	if err := h.validator.ValidateStruct(&req); err != nil {
		log.Printf("[USSD] GenerateCode - Validation error: %v", err)
		services.SendErrorResponse(w, "Validation failed", http.StatusBadRequest, err)
		return
	}

	var code string
	var err error
	if req.Type == "Send" {
		code, err = h.service.GeneratePushCode(r.Context(), userID, req.Amount)
	} else {
		code, err = h.service.GeneratePullCode(r.Context(), userID, req.Amount)
	}

	if err != nil {
		log.Printf("[USSD] GenerateCode - Service error: %v", err)
		services.SendErrorResponse(w, err.Error(), http.StatusInternalServerError, nil)
		return
	}

	expiresIn := int(h.service.GetCodeTimeout().Seconds())
	formattedCode := h.service.FormatDialCode(code)

	log.Printf("[USSD] GenerateCode - Success: code=%s, formattedCode=%s, expiresIn=%d", code, formattedCode, expiresIn)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success":   true,
		"ussdCode":  formattedCode,
		"expiresIn": expiresIn,
	})
}

// ValidateCode validates and consumes a USSD code
// @Summary Validate USSD Code
// @Description Validate and consume a single-use USSD code
// @Tags USSD
// @Accept json
// @Produce json
// @Param request body object{code=string,mobileNo=string} true "Code validation request"
// @Success 200 {object} services.USSDCode
// @Failure 400 {object} services.ErrorResponse
// @Router /ussd/validate [post]
func (h *USSDHandler) ValidateCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string `json:"code" validate:"required"`
		MobileNo string `json:"mobileNo" validate:"required"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		services.SendErrorResponse(w, "Unable To Process This Request At This Time", http.StatusBadRequest, nil)
		return
	}

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		services.SendErrorResponse(w, "Request body must only contain a single JSON object", http.StatusBadRequest, nil)
		return
	}

	if err := h.validator.ValidateStruct(&req); err != nil {
		services.SendErrorResponse(w, "Validation failed", http.StatusBadRequest, err)
		return
	}

	codeType := services.PullPayment
	if len(req.Code) > 0 && req.Code[0] >= '0' && req.Code[0] <= '9' {
		// Numeric codes need type detection logic
		// For now, default to PullPayment unless service provides detection
	}

	ussdCode, err := h.service.ValidateAndConsume(r.Context(), req.Code, codeType)
	if err != nil {
		services.SendErrorResponse(w, err.Error(), http.StatusBadRequest, nil)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ussdCode)
}

// GetUserCodes retrieves all generated codes for the authenticated user
// @Summary Get User USSD Codes
// @Description Get all USSD codes generated by the authenticated user
// @Tags USSD
// @Produce json
// @Security BearerAuth
// @Success 200 {array} services.USSDCode
// @Failure 401 {object} services.ErrorResponse
// @Failure 500 {object} services.ErrorResponse
// @Router /ussd/codes [get]
func (h *USSDHandler) GetUserCodes(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value("userID").(string)
	if !ok || userID == "" {
		services.SendErrorResponse(w, "Unauthorized", http.StatusUnauthorized, nil)
		return
	}

	codes, err := h.service.GetUserCodes(r.Context(), userID)
	if err != nil {
		services.SendErrorResponse(w, err.Error(), http.StatusInternalServerError, nil)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(codes)
}
