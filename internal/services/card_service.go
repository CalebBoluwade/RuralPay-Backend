package services

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
)

type CardService struct {
	db        *sql.DB
	hsm       hsm.HSMInterface
	validator *ValidationHelper
}

// ProvisionRequest represents card provisioning request
type ProvisionRequest struct {
	UserID         int     `json:"userId" validate:"required,gt=0"`
	CardType       string  `json:"cardType" validate:"required,oneof=DEBIT CREDIT PREPAID"`
	InitialBalance float64 `json:"initialBalance" validate:"gte=0"`
}

// ActivationRequest represents card activation request
type ActivationRequest struct {
	CardID         string `json:"cardId" validate:"required"`
	ActivationCode string `json:"activationCode" validate:"required,len=6"`
}

func NewCardService(db *sql.DB, hsm hsm.HSMInterface) *CardService {
	return &CardService{
		db:        db,
		hsm:       hsm,
		validator: NewValidationHelper(),
	}
}

// ProvisionCard creates a new NFC card
// @Summary Provision a new card
// @Description Create and provision a new NFC payment card
// @Tags cards
// @Accept json
// @Produce json
// @Param card body object{userId=int,cardType=string,initialBalance=float64} true "Card provisioning data"
// @Success 200 {object} object{cardId=string,status=string}
// @Failure 400 {object} map[string]string
// @Router /cards/provision [post]
func (cps *CardService) ProvisionCard(w http.ResponseWriter, r *http.Request) {
	maxBytes := 1_048_576 // 1 MB
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxBytes))

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req ProvisionRequest
	if err := dec.Decode(&req); err != nil {
		utils.SendErrorResponse(w, "Unable To Process This Request At This Time", http.StatusBadRequest, nil)
		return
	}

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	if err := cps.validator.ValidateStruct(&req); err != nil {
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "provisioned"})
}

// ActivateCard activates a provisioned card
// @Summary Activate card
// @Description Activate a provisioned NFC card
// @Tags cards
// @Accept json
// @Produce json
// @Param activation body object{cardId=string,activationCode=string} true "Card activation data"
// @Success 200 {object} object{cardId=string,status=string}
// @Failure 400 {object} map[string]string
// @Router /cards/activate [post]
func (cps *CardService) ActivateCard(w http.ResponseWriter, r *http.Request) {
	maxBytes := 1_048_576 // 1 MB
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxBytes))

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req ActivationRequest
	if err := dec.Decode(&req); err != nil {
		utils.SendErrorResponse(w, "Unable To Process This Request At This Time", http.StatusBadRequest, nil)
		return
	}

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	if err := cps.validator.ValidateStruct(&req); err != nil {
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "activated"})
}

// GetCard retrieves card information
// @Summary Get card details
// @Description Retrieve information about a specific card
// @Tags cards
// @Produce json
// @Param cardId path string true "Card ID"
// @Success 200 {object} object{cardId=string,status=string,balance=float64}
// @Failure 404 {object} map[string]string
// @Router /cards/{cardId} [get]
func (cps *CardService) GetCard(w http.ResponseWriter, r *http.Request) {
	cardID := chi.URLParam(r, "cardId")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"cardId": cardID, "status": "active"})
}

// SuspendCard suspends a card
// @Summary Suspend card
// @Description Suspend a card to prevent transactions
// @Tags cards
// @Produce json
// @Param cardId path string true "Card ID"
// @Success 200 {object} object{cardId=string,status=string}
// @Failure 404 {object} map[string]string
// @Router /cards/{cardId}/suspend [put]
func (cps *CardService) SuspendCard(w http.ResponseWriter, r *http.Request) {
	cardID := chi.URLParam(r, "cardId")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"cardId": cardID, "status": "suspended"})
}

// QueryCardBin retrieves card BIN information
// @Summary Query Card BIN
// @Description Retrieve information about a card BIN
// @Tags cards
// @Produce json
// @Param bin query string true "Card BIN (first 6-8 digits)"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Router /cards/bins [get]
func (cps *CardService) QueryCardBin(w http.ResponseWriter, r *http.Request) {
	bin := r.URL.Query().Get("bin")
	if bin == "" {
		utils.SendErrorResponse(w, "BIN is required", http.StatusBadRequest, nil)
		return
	}

	if len(bin) < 6 {
		utils.SendErrorResponse(w, "Invalid BIN length", http.StatusBadRequest, nil)
		return
	}

	// Sanitize BIN
	bin = strings.ReplaceAll(bin, " ", "")
	if len(bin) < 6 {
		utils.SendErrorResponse(w, "Invalid BIN length", http.StatusBadRequest, nil)
		return
	}

	// Use first 6 digits for lookup
	lookupBin := bin
	if len(lookupBin) > 6 {
		lookupBin = lookupBin[:6]
	}

	// 1. Check local seed data
	seedData := map[string]models.BINResponse{
		"539983": {BIN: "539983", Scheme: "Mastercard", IssuerBank: "GTBank", Type: "Debit", Country: "NG", Currency: "NGN", Source: "internal"},
		"506109": {BIN: "506109", Scheme: "Verve", IssuerBank: "Interswitch", Type: "Debit", Country: "NG", Currency: "NGN", Source: "internal"},
		"402345": {BIN: "402345", Scheme: "Visa", IssuerBank: "Zenith Bank", Type: "Classic", Country: "NG", Currency: "NGN", Source: "internal"},
		"519911": {BIN: "519911", Scheme: "Mastercard", IssuerBank: "UBA", Type: "Gold", Country: "NG", Currency: "NGN", Source: "internal"},
		"528641": {BIN: "528641", Scheme: "Mastercard", IssuerBank: "Access Bank", Type: "Standard", Country: "NG", Currency: "NGN", Source: "internal"},
	}

	if data, found := seedData[lookupBin]; found {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
		return
	}

	// 2. Fetch from external API
	response, err := fetchFromBinList(bin)
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	// 3. Fallback logic
	fallback := models.BINResponse{
		BIN:    bin,
		Source: "fallback",
	}

	if strings.HasPrefix(bin, "4") {
		fallback.Scheme = "Visa"
	} else if strings.HasPrefix(bin, "5") {
		fallback.Scheme = "Mastercard"
	} else if strings.HasPrefix(bin, "506") || strings.HasPrefix(bin, "65") {
		fallback.Scheme = "Verve"
	} else {
		utils.SendErrorResponse(w, "BIN information not found", http.StatusNotFound, nil)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fallback)
}

// fetchFromBinList retrieves BIN info from external API
func fetchFromBinList(bin string) (models.BINResponse, error) {
	client := http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", fmt.Sprintf("https://lookup.binlist.net/%s", bin), nil)
	if err != nil {
		return models.BINResponse{}, err
	}
	req.Header.Set("Accept-Version", "3")

	resp, err := client.Do(req)
	if err != nil {
		return models.BINResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return models.BINResponse{}, fmt.Errorf("external API error: %d", resp.StatusCode)
	}

	var ext struct {
		Scheme string `json:"scheme"`
		Bank   struct {
			Name string `json:"name"`
		} `json:"bank"`
		Type    string `json:"type"`
		Country struct {
			Alpha2   string `json:"alpha2"`
			Currency string `json:"currency"`
		} `json:"country"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&ext); err != nil {
		return models.BINResponse{}, err
	}

	// Normalize
	return models.BINResponse{
		BIN:        bin,
		Scheme:     strings.ToUpper(ext.Scheme),
		IssuerBank: ext.Bank.Name,
		Type:       strings.Title(strings.ToLower(ext.Type)),
		Country:    ext.Country.Alpha2,
		Currency:   ext.Country.Currency,
		Source:     "external_api",
	}, nil
}
