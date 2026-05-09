package services

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ruralpay/backend/internal/utils"
	"github.com/spf13/viper"
)

type Bank struct {
	BankCode         string  `json:"bankCode"`
	CBNCode          string  `json:"cbnCode"`
	Name             string  `json:"name"`
	LogoData         string  `json:"logoData"`
	UptimePrediction float64 `json:"uptimePrediction"`
}

var svgFormat = "data:image/svg+xml;base64,"

const (
	logosDir = "./static/bank-logos"
	demoSVG  = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 200 200"><rect width="200" height="200" fill="#f0f0f0"/><path d="M100 60c-22.1 0-40 17.9-40 40s17.9 40 40 40 40-17.9 40-40-17.9-40-40-40zm0 65c-13.8 0-25-11.2-25-25s11.2-25 25-25 25 11.2 25 25-11.2 25-25 25z" fill="#999"/><text x="100" y="170" text-anchor="middle" font-family="Arial" font-size="14" fill="#666">BANK</text></svg>`
)

// NPSParticipant represents a single participant from the NPS getParticipants response.
type NPSParticipant struct {
	InstitutionCode string   `xml:"institutionCode,attr" json:"institutionCode"`
	BICFIC          string   `xml:"bicfic"               json:"bicfic"`
	Name            string   `xml:"name"                 json:"name"`
	CountryCode     string   `xml:"countryCode"          json:"countryCode"`
	Status          string   `xml:"status"               json:"status"`
	CategoryCode    string   `xml:"categoryCode"         json:"categoryCode"`
	Currencies      []string `xml:"currencies>currency"  json:"currencies"`
	Operations      []string `xml:"operationsAllowed>operation" json:"operationsAllowed"`
}

type npsParticipantsResponse struct {
	XMLName      xml.Name         `xml:"participants"`
	Participants []NPSParticipant `xml:"participant"`
}

type BankService struct {
	db         *sql.DB
	npsClient  *http.Client
}

func NewBankService(db *sql.DB) *BankService {
	return &BankService{
		db: db,
		npsClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
			Timeout: utils.GetTimeout("nibss.http_timeout", 30),
		},
	}
}

// GetNPSParticipants calls the NPS getParticipants endpoint and returns the list of participants.
func (bs *BankService) GetNPSParticipants(ctx context.Context) ([]NPSParticipant, error) {
	npsIP := viper.GetString("nibss.iso20022.base.url")
	if npsIP == "" {
		return nil, fmt.Errorf("nibss.iso20022.base.url is not configured")
	}

	url := fmt.Sprintf("%s/nps/getParticipants", npsIP)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create NPS request: %w", err)
	}

	resp, err := bs.npsClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("NPS getParticipants request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("NPS getParticipants returned status %d", resp.StatusCode)
	}

	var result npsParticipantsResponse
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode NPS participants response: %w", err)
	}

	slog.Info("nps.get_participants.success", "count", len(result.Participants))
	return result.Participants, nil
}

func (bs *BankService) GetAllBanks(w http.ResponseWriter, r *http.Request) {
	query := `
		SELECT bankCode, cbnCode, name, logo_filename, 99 
		FROM banks 
		ORDER BY name ASC
	`

	rows, err := bs.db.Query(query)
	if err != nil {
		slog.Error("Failed to fetch banks", "error", err)
		utils.SendErrorResponse(w, "Failed to fetch banks", http.StatusInternalServerError, nil)
		return
	}
	defer rows.Close()

	var banks []Bank
	for rows.Next() {
		var bank Bank
		var logoFilename sql.NullString

		err := rows.Scan(&bank.BankCode, &bank.CBNCode, &bank.Name, &logoFilename, &bank.UptimePrediction)
		if err != nil {
			slog.Error("Failed to scan bank data", "error", err)
			utils.SendErrorResponse(w, "Failed to scan bank data", http.StatusFailedDependency, nil)
			return
		}

		// Load logo from file system if filename is available
		if logoFilename.Valid {
			bank.LogoData = bs.LoadLogo(logoFilename.String)
		} else {
			bank.LogoData = svgFormat + base64.StdEncoding.EncodeToString([]byte(demoSVG))
		}

		banks = append(banks, bank)
	}

	if err = rows.Err(); err != nil {
		slog.Error("Error occurred while iterating through The Banks", "error", err)
		utils.SendErrorResponse(w, utils.InternalServiceError, http.StatusFailedDependency, nil)
		return
	}

	utils.SendSuccessResponse(w, "Banks Fetched Successfully", banks, http.StatusOK)
}

func (bs *BankService) GetBankByCode(w http.ResponseWriter, r *http.Request, code string) {
	query := `
		SELECT bankCode, cbnCode, name, logo_filename, 99 
		FROM banks 
		WHERE bankCode = $1
	`

	var bank Bank
	var logoFilename sql.NullString

	err := bs.db.QueryRow(query, code).Scan(&bank.BankCode, &bank.CBNCode, &bank.Name, &logoFilename, &bank.UptimePrediction)
	if err == sql.ErrNoRows {
		slog.Warn("No Bank Found", "code", code)
		utils.SendErrorResponse(w, "Bank not found", http.StatusNotFound, nil)
		return
	}
	if err != nil {
		slog.Error("Failed to Fetch Bank", "error", err)
		utils.SendErrorResponse(w, utils.InternalServiceError, http.StatusFailedDependency, nil)
		return
	}

	// Load logo from file system if filename is available
	if logoFilename.Valid {
		bank.LogoData = bs.LoadLogo(logoFilename.String)
	} else {
		bank.LogoData = svgFormat + base64.StdEncoding.EncodeToString([]byte(demoSVG))
	}

	utils.SendSuccessResponse(w, "Bank Fetched Successfully", bank, http.StatusOK)
}

func (bs *BankService) LoadLogo(filename string) string {
	path := filepath.Join(logosDir, filename)
	if data, err := os.ReadFile(path); err == nil {
		return svgFormat + base64.StdEncoding.EncodeToString(data)
	}

	// slog.Debug("Failed To Read Bank Logo File. Returning default SVG")
	// Return default SVG if file not found
	return svgFormat + base64.StdEncoding.EncodeToString([]byte(demoSVG))
}
