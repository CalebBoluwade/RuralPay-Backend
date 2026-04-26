package services

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
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
// @Router /merchant [post]
func (s *MerchantService) OnboardMerchant(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("userID")
	if userID == nil {
		utils.SendErrorResponse(w, utils.UnauthorizedError, http.StatusUnauthorized, nil)
		return
	}

	var req OnboardMerchantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
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
		utils.SendErrorResponse(w, "Merchant Already Exists for this User", http.StatusConflict, nil)
		return
	}
	if err != nil {
		slog.Error("merchant.onboard.failed", "error", err)
		utils.SendErrorResponse(w, "Failed to Create Merchant", http.StatusFailedDependency, err)
		return
	}

	slog.Info("merchant.onboard.success", "merchant_id", merchant.ID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(merchant)
}

type TransactionStatusBreakdown struct {
	Status           string `json:"status"`
	TransactionCount int64  `json:"transactionCount"`
	TotalAmount      int64  `json:"totalAmount"`
}

type MerchantStats struct {
	TodayCompletedCount  int64                        `json:"todayCompletedCount"`
	TodayCompletedVolume int64                        `json:"todayCompletedVolume"`
	TodayProfit          float64                      `json:"todayProfit"`
	TotalCompletedVolume int64                        `json:"totalCompletedVolume"`
	TotalProfit          float64                      `json:"totalProfit"`
	TotalCompletedCount  int64                        `json:"totalCompletedCount"`
	ByStatus             []TransactionStatusBreakdown `json:"byStatus"`
}

// GetMerchant godoc
// @Summary Update Merchant Business Account
// @Description Retrieve merchant data and stats for the authenticated user
// @Tags Merchants
// @Produce json
// @Failure 200 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /merchant/account [get]
func (s *MerchantService) UpdateMerchantBusinessAccount(w http.ResponseWriter, r *http.Request) {
	_, merchantID := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	account_id := chi.URLParam(r, "accountNumber")

	result, err := s.db.ExecContext(r.Context(), `
		UPDATE merchants 
		SET account_id = $2
		WHERE id = $1
		`, merchantID, account_id)

	if err != nil {
		slog.Error("merchant.update.business_account.failed", "error", err)
		utils.SendErrorResponse(w, "Internal server error", http.StatusInternalServerError, err)
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		utils.SendErrorResponse(w, "Merchant not found", http.StatusNotFound, nil)
		return
	}

	utils.SendSuccessResponse(w, "Business Account Updated Successfully", nil, http.StatusOK)
}

// GetMerchant godoc
// @Summary Get merchant details
// @Description Retrieve merchant data and stats for the authenticated user
// @Tags Merchants
// @Produce json
// @Success 200 {object} MerchantStats
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /merchant [get]
func (s *MerchantService) GetMerchantData(w http.ResponseWriter, r *http.Request) {
	_, merchantID := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	rows, err := s.db.Query(`
		WITH base AS (
			SELECT m.commission_rate, t.status, t.amount, t.created_at
			FROM merchants m
			LEFT JOIN transactions t ON t.credit_id = m.account_id
			WHERE m.id = $1
		)
		SELECT
			status,
			COUNT(*)                                                                                     AS transaction_count,
			COALESCE(SUM(amount), 0)                                                                     AS total_amount,
			MAX(commission_rate)                                                                         AS commission_rate,
			COUNT(*) FILTER (WHERE status = 'COMPLETED' AND created_at >= CURRENT_DATE)                  AS today_completed_count,
			COALESCE(SUM(amount) FILTER (WHERE status = 'COMPLETED' AND created_at >= CURRENT_DATE), 0) AS today_completed_volume,
			COUNT(*) FILTER (WHERE status = 'COMPLETED')                                                 AS total_completed_count,
			COALESCE(SUM(amount) FILTER (WHERE status = 'COMPLETED'), 0)                                 AS total_completed_volume
		FROM base
		GROUP BY status`, merchantID)
	if err == sql.ErrNoRows || rows == nil {
		utils.SendErrorResponse(w, "Merchant Not Found", http.StatusNotFound, nil)
		return
	}
	if err != nil {
		slog.Error("merchant.stats.query_failed", "error", err)
		utils.SendErrorResponse(w, "Internal server error", http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	var stats MerchantStats
	var commissionRate float64
	for rows.Next() {
		var ss TransactionStatusBreakdown
		var todayCount, todayVolume, totalCount, totalVolume int64
		rows.Scan(&ss.Status, &ss.TransactionCount, &ss.TotalAmount,
			&commissionRate,
			&todayCount, &todayVolume,
			&totalCount, &totalVolume)
		if ss.Status != "" {
			stats.ByStatus = append(stats.ByStatus, ss)
		}
		stats.TodayCompletedCount += todayCount
		stats.TodayCompletedVolume += todayVolume
		stats.TotalCompletedCount += totalCount
		stats.TotalCompletedVolume += totalVolume
	}

	stats.TodayProfit = float64(stats.TodayCompletedVolume) * (commissionRate / 100)
	stats.TotalProfit = float64(stats.TotalCompletedVolume) * (commissionRate / 100)

	utils.SendSuccessResponse(w, "Fetched Merchant Stats Successfully", stats, http.StatusOK)
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
// @Router /merchant [put]
func (s *MerchantService) UpdateMerchant(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("userID")
	if userID == nil {
		utils.SendErrorResponse(w, utils.UnauthorizedError, http.StatusUnauthorized, nil)
		return
	}

	reqCtx := r.Context()

	var req UpdateMerchantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	result, err := s.db.ExecContext(reqCtx, `
		UPDATE merchants 
		SET business_name = COALESCE(NULLIF($1, ''), business_name),
		    business_type = COALESCE(NULLIF($2, ''), business_type),
		    commission_rate = CASE WHEN $3 > 0 THEN $3 ELSE commission_rate END,
		    settlement_cycle = COALESCE(NULLIF($4, ''), settlement_cycle),
		    updated_at = NOW()
		WHERE user_id = $5`,
		req.BusinessName, req.BusinessType, req.CommissionRate, req.SettlementCycle, userID)

	if err != nil {
		slog.Error("merchant.update.failed", "error", err)
		utils.SendErrorResponse(w, "Failed to update merchant", http.StatusFailedDependency, err)
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		utils.SendErrorResponse(w, "Merchant not found", http.StatusNotFound, nil)
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
// @Router /merchant/status [put]
func (s *MerchantService) UpdateMerchantStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MerchantID int    `json:"merchantId" validate:"required"`
		Status     string `json:"status" validate:"required,oneof=active suspended rejected"`
	}

	reqCtx := r.Context()

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if err := s.validator.Struct(&req); err != nil {
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	result, err := s.db.ExecContext(reqCtx, "UPDATE merchants SET status = $1, updated_at = NOW() WHERE id = $2", req.Status, req.MerchantID)
	if err != nil {
		slog.Error("merchant.update_status.failed", "error", err)
		utils.SendErrorResponse(w, "Failed to update merchant status", http.StatusFailedDependency, err)
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		utils.SendErrorResponse(w, "Merchant not found", http.StatusNotFound, nil)
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
// @Router /merchant/list [get]
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
		slog.Error("merchant.list.query_failed", "error", err)
		utils.SendErrorResponse(w, "Internal server error", http.StatusFailedDependency, err)
		return
	}
	defer rows.Close()

	merchants := []models.Merchant{}
	for rows.Next() {
		var m models.Merchant
		if err := rows.Scan(&m.ID, &m.UserID, &m.BusinessName, &m.BusinessType, &m.TaxID,
			&m.Status, &m.CommissionRate, &m.SettlementCycle,
			&m.CreatedAt, &m.UpdatedAt); err != nil {
			slog.Error("merchant.list.scan_failed", "error", err)
			continue
		}
		merchants = append(merchants, m)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(merchants)
}
