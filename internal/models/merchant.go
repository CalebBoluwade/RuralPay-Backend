package models

import "time"

type Merchant struct {
	ID              int       `json:"id"`
	UserID          int       `json:"userId"`
	BusinessName    string    `json:"businessName"`
	BusinessType    string    `json:"businessType"`
	TaxID           string    `json:"taxId"`
	Status          string    `json:"status"`
	CommissionRate  float64   `json:"commissionRate"`
	SettlementCycle string    `json:"settlementCycle"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}
