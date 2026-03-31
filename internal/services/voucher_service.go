package services

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/lib/pq"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
)

type VoucherService struct {
	db *sql.DB
}

func NewVoucherService(db *sql.DB) *VoucherService {
	return &VoucherService{db: db}
}

// FetchVouchers godoc
// @Summary List all vouchers
// @Description Retrieve all available vouchers
// @Tags Vouchers
// @Produce json
// @Success 200 {array} models.Voucher
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /vouchers [get]
func (s *VoucherService) FetchVouchers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id, voucher_code, voucher_description, voucher_discount_amount, voucher_type, voucher_allowed_services
		FROM vouchers
		ORDER BY created_at DESC`)
	if err != nil {
		slog.Error("voucher.fetch.query_failed", "error", err)
		utils.SendErrorResponse(w, utils.InternalServiceError, http.StatusInternalServerError, nil)
		return
	}
	defer rows.Close()

	vouchers := []models.Voucher{}
	for rows.Next() {
		var v models.Voucher
		if err := rows.Scan(&v.ID, &v.VoucherCode, &v.VoucherDescription, &v.VoucherDiscountAmount, &v.VoucherType, pq.Array(&v.VoucherAllowedServices)); err != nil {
			slog.Error("voucher.fetch.scan_failed", "error", err)
			utils.SendErrorResponse(w, utils.InternalServiceError, http.StatusInternalServerError, nil)
			return
		}
		vouchers = append(vouchers, v)
	}
	if err := rows.Err(); err != nil {
		slog.Error("voucher.fetch.rows_error", "error", err)
		utils.SendErrorResponse(w, utils.InternalServiceError, http.StatusInternalServerError, nil)
		return
	}

	utils.SendSuccessResponse(w, "Vouchers Fetched Successfully", vouchers, http.StatusOK)
}
