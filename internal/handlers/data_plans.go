package handlers

import (
	"net/http"

	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
)

// GetDataPlans returns a list of available data plans.
// @Summary Get data plans
// @Description Get a list of available data plans
// @Tags Data Plans
// @Produce json
// @Success 200 {object} utils.APISuccessResponse{details=[]models.DataPlan}
// @Router /api/v1/data-plans [get]
func GetDataPlans(w http.ResponseWriter, r *http.Request) {
	dataPlans := []models.DataPlan{
		{ID: "1", Size: "1GB", Validity: "1 Day", Price: 300},
		{ID: "2", Size: "2GB", Validity: "7 Days", Price: 500},
		{ID: "3", Size: "5GB", Validity: "30 Days", Price: 1500},
		{ID: "4", Size: "10GB", Validity: "30 Days", Price: 2500},
		{ID: "5", Size: "20GB", Validity: "30 Days", Price: 4500},
		{ID: "6", Size: "50GB", Validity: "30 Days", Price: 10000},
	}

	utils.SendSuccessResponse(w, "Data plans retrieved successfully", dataPlans, http.StatusOK)
}
