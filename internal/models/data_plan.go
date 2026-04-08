package models

// DataPlan represents a mobile data plan.
type DataPlan struct {
	ID       string `json:"id"`
	Size     string `json:"size"`
	Validity string `json:"validity"`
	Price    int    `json:"price"`
}
