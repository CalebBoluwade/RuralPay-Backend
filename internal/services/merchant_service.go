package services

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"

	"github.com/go-playground/validator/v10"
	"github.com/ruralpay/backend/internal/models"
)

type MerchantService struct {
	db        *sql.DB
	validator *validator.Validate
}

type OnboardMerchantRequest struct {
	BusinessName    string  `json:"businessName" validate:"required,min=2"`
	BusinessType    string  `json:"businessType" validate:"required"`
	TaxID           string  `json:"taxId" validate:"required"`
	CommissionRate  float64 `json:"commissionRate" validate:"gte=0,lte=100"`
	SettlementCycle string  `json:"settlementCycle" validate:"required,oneof=DAILY WEEKLY MONTHLY"`
}

type UpdateMerchantRequest struct {
	BusinessName    string  `json:"businessName,omitempty"`
	BusinessType    string  `json:"businessType,omitempty"`
	CommissionRate  float64 `json:"commissionRate,omitempty" validate:"omitempty,gte=0,lte=100"`
	SettlementCycle string  `json:"settlementCycle,omitempty" validate:"omitempty,oneof=DAILY WEEKLY MONTHLY"`
}

func NewMerchantService(db *sql.DB) *MerchantService {
	return &MerchantService{
		db:        db,
		validator: validator.New(),
	}
}

// OnboardMerchant godoc
// @Summary Onboard a new merchant
// @Description Create a new merchant account for the authenticated user
// @Tags Merchants
// @Accept json
// @Produce json
// @Param request body OnboardMerchantRequest true "Merchant onboarding details"
// @Success 200 {object} models.Merchant
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /merchants [post]
func (s *MerchantService) OnboardMerchant(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("userID")
	if userID == nil {
		SendErrorResponse(w, "Unauthorized", http.StatusUnauthorized, nil)
		return
	}

	var req OnboardMerchantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		SendErrorResponse(w, "Invalid Request", http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		SendErrorResponse(w, "Validation Failed", http.StatusBadRequest, err)
		return
	}

	var merchant models.Merchant
	err := s.db.QueryRow(`
		INSERT INTO merchants (user_id, business_name, business_type, tax_id, status, commission_rate, settlement_cycle)
		SELECT $1, $2, $3, $4, 'pending', $5, $6
		WHERE NOT EXISTS(SELECT 1 FROM merchants WHERE user_id = $1)
		RETURNING id, user_id, business_name, business_type, tax_id, status, commission_rate, settlement_cycle, created_at, updated_at`,
		userID, req.BusinessName, req.BusinessType, req.TaxID, req.CommissionRate, req.SettlementCycle,
	).Scan(&merchant.ID, &merchant.UserID, &merchant.BusinessName, &merchant.BusinessType, &merchant.TaxID,
		&merchant.Status, &merchant.CommissionRate, &merchant.SettlementCycle,
		&merchant.CreatedAt, &merchant.UpdatedAt)

	if err == sql.ErrNoRows {
		SendErrorResponse(w, "Merchant Already Exists for this User", http.StatusConflict, nil)
		return
	}
	if err != nil {
		log.Printf("[MERCHANT] Failed to Create Merchant: %v", err)
		SendErrorResponse(w, "Failed to Create Merchant", http.StatusInternalServerError, err)
		return
	}

	log.Printf("[MERCHANT] Merchant Created Successfully - ID: %d, User: %v", merchant.ID, userID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(merchant)
}

// GetMerchant godoc
// @Summary Get merchant details
// @Description Retrieve merchant Data for the authenticated user
// @Tags Merchants
// @Produce json
// @Success 200 {object} models.Merchant
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /merchants [get]
func (s *MerchantService) GetMerchantData(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("userID")
	if userID == nil {
		SendErrorResponse(w, "Unauthorized", http.StatusUnauthorized, nil)
		return
	}

	var merchant models.Merchant
	err := s.db.QueryRow(`
		SELECT id, user_id, business_name, business_type, tax_id, status, commission_rate, settlement_cycle, created_at, updated_at
		FROM merchants WHERE user_id = $1`, userID).Scan(
		&merchant.ID, &merchant.UserID, &merchant.BusinessName, &merchant.BusinessType, &merchant.TaxID,
		&merchant.Status, &merchant.CommissionRate, &merchant.SettlementCycle,
		&merchant.CreatedAt, &merchant.UpdatedAt)

	if err == sql.ErrNoRows {
		SendErrorResponse(w, "Merchant Not Found", http.StatusNotFound, nil)
		return
	}

	if err != nil {
		log.Printf("[MERCHANT] Failed to get merchant: %v", err)
		SendErrorResponse(w, "Internal server error", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(merchant)
}

// UpdateMerchant godoc
// @Summary Update merchant details
// @Description Update merchant information for the authenticated user
// @Tags Merchants
// @Accept json
// @Produce json
// @Param request body UpdateMerchantRequest true "Merchant update details"
// @Success 200 {object} models.Merchant
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /merchants [put]
func (s *MerchantService) UpdateMerchant(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("userID")
	if userID == nil {
		SendErrorResponse(w, "Unauthorized", http.StatusUnauthorized, nil)
		return
	}

	var req UpdateMerchantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		SendErrorResponse(w, "Invalid request", http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		SendErrorResponse(w, "Validation failed", http.StatusBadRequest, err)
		return
	}

	result, err := s.db.Exec(`
		UPDATE merchants 
		SET business_name = COALESCE(NULLIF($1, ''), business_name),
		    business_type = COALESCE(NULLIF($2, ''), business_type),
		    commission_rate = CASE WHEN $3 > 0 THEN $3 ELSE commission_rate END,
		    settlement_cycle = COALESCE(NULLIF($4, ''), settlement_cycle),
		    updated_at = NOW()
		WHERE user_id = $5`,
		req.BusinessName, req.BusinessType, req.CommissionRate, req.SettlementCycle, userID)

	if err != nil {
		log.Printf("[MERCHANT] Failed to update merchant: %v", err)
		SendErrorResponse(w, "Failed to update merchant", http.StatusInternalServerError, err)
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		SendErrorResponse(w, "Merchant not found", http.StatusNotFound, nil)
		return
	}

	// s.GetMerchant(w, r)
}

// UpdateMerchantStatus godoc
// @Summary Update merchant status
// @Description Update the status of a merchant (admin only)
// @Tags Merchants
// @Accept json
// @Produce json
// @Param request body object{merchantId=int,status=string} true "Status update details"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /merchants/status [put]
func (s *MerchantService) UpdateMerchantStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MerchantID int    `json:"merchantId" validate:"required"`
		Status     string `json:"status" validate:"required,oneof=active suspended rejected"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		SendErrorResponse(w, "Invalid request", http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		SendErrorResponse(w, "Validation failed", http.StatusBadRequest, err)
		return
	}

	result, err := s.db.Exec("UPDATE merchants SET status = $1, updated_at = NOW() WHERE id = $2", req.Status, req.MerchantID)
	if err != nil {
		log.Printf("[MERCHANT] Failed to update merchant status: %v", err)
		SendErrorResponse(w, "Failed to update merchant status", http.StatusInternalServerError, err)
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		SendErrorResponse(w, "Merchant not found", http.StatusNotFound, nil)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "Merchant status updated", "status": req.Status})
}

// ListMerchants godoc
// @Summary List all merchants
// @Description Retrieve a list of all merchants, optionally filtered by status
// @Tags Merchants
// @Produce json
// @Param status query string false "Filter by status" Enums(pending, active, suspended, rejected)
// @Success 200 {array} models.Merchant
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /merchants [get]
func (s *MerchantService) ListMerchants(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")

	query := "SELECT id, user_id, business_name, business_type, tax_id, status, commission_rate, settlement_cycle, created_at, updated_at FROM merchants"
	args := []interface{}{}

	if status != "" {
		query += " WHERE status = $1"
		args = append(args, status)
	}
	query += " ORDER BY created_at DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		log.Printf("[MERCHANT] Failed to list merchants: %v", err)
		SendErrorResponse(w, "Internal server error", http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	merchants := []models.Merchant{}
	for rows.Next() {
		var m models.Merchant
		if err := rows.Scan(&m.ID, &m.UserID, &m.BusinessName, &m.BusinessType, &m.TaxID,
			&m.Status, &m.CommissionRate, &m.SettlementCycle,
			&m.CreatedAt, &m.UpdatedAt); err != nil {
			log.Printf("[MERCHANT] Failed to scan merchant: %v", err)
			continue
		}
		merchants = append(merchants, m)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(merchants)
}
