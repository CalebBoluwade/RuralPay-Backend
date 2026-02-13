package handlers

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/ruralpay/backend/internal/services"
)

type QRHandler struct {
	service   *services.QRService
	validator *services.ValidationHelper
}

func NewQRHandler(service *services.QRService) *QRHandler {
	return &QRHandler{
		service:   service,
		validator: services.NewValidationHelper(),
	}
}

// GenerateQR generates a QR Code
// @Summary Generate QR Code
// @Description Generate a QR code for payment
// @Tags QR
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body object{amount=int64} true "QR generation request"
// @Success 200 {object} object{qrCode=string}
// @Failure 400 {object} services.ErrorResponse
// @Failure 401 {object} services.ErrorResponse
// @Router /qr/generate [post]
func (h *QRHandler) GenerateQR(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value("userID").(string)
	if !ok || userID == "" {
		services.SendErrorResponse(w, "Unauthorized", http.StatusUnauthorized, nil)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1_048_576)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	// if err := dec.Decode(&req); err != nil {
	// 	services.SendErrorResponse(w, "Unable To Process This Request At This Time", http.StatusBadRequest, nil)
	// 	return
	// }

	// if err := dec.Decode(&struct{}{}); err != io.EOF {
	// 	services.SendErrorResponse(w, "Request body must only contain a single JSON object", http.StatusBadRequest, nil)
	// 	return
	// }

	// if err := h.validator.ValidateStruct(&req); err != nil {
	// 	services.SendErrorResponse(w, "Validation failed", http.StatusBadRequest, err)
	// 	return
	// }

	qrCode, qrImage, err := h.service.GenerateQRCode(r.Context(), userID)
	if err != nil {
		services.SendErrorResponse(w, err.Error(), http.StatusInternalServerError, nil)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"qrCode":  qrCode,
		"qrImage": qrImage,
	})
}

// ProcessQR processes a scanned QR code
// @Summary Process QR Code
// @Description Process a scanned QR code data
// @Tags QR
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body object{qrData=string} true "QR processing request"
// @Success 200 {object} object{userId=string,amount=int64}
// @Failure 400 {object} services.ErrorResponse
// @Router /qr/process [post]
func (h *QRHandler) ProcessQR(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QRData string `json:"qrData" validate:"required"`
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

	result, err := h.service.ProcessQRCode(r.Context(), req.QRData)
	if err != nil {
		services.SendErrorResponse(w, err.Error(), http.StatusBadRequest, nil)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"data":    result,
	})
}
