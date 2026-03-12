package services

import (
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ruralpay/backend/internal/utils"
)

type Bank struct {
	Code             string  `json:"code"`
	Name             string  `json:"name"`
	LogoData         string  `json:"logoData"`
	UptimePrediction float64 `json:"uptimePrediction"`
}

var svgFormat = "data:image/svg+xml;base64,"

const (
	logosDir = "./static/bank-logos"
	demoSVG  = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 200 200"><rect width="200" height="200" fill="#f0f0f0"/><path d="M100 60c-22.1 0-40 17.9-40 40s17.9 40 40 40 40-17.9 40-40-17.9-40-40-40zm0 65c-13.8 0-25-11.2-25-25s11.2-25 25-25 25 11.2 25 25-11.2 25-25 25z" fill="#999"/><text x="100" y="170" text-anchor="middle" font-family="Arial" font-size="14" fill="#666">BANK</text></svg>`
)

var bankLogos = map[string]string{
	"044":    "access-bank.svg",
	"063":    "access-bank.svg",
	"401":    "aso-savings.svg",
	"023":    "citibank.svg",
	"050":    "ecobank.svg",
	"562":    "ekondo.svg",
	"070":    "fidelity.svg",
	"011":    "firstbank.svg",
	"214":    "fcmb.svg",
	"00103":  "globus.svg",
	"012345": "pocketapp.svg",
	"058":    "gtbank.svg",
	"301":    "jaiz.svg",
	"082":    "keystone.svg",
	"526":    "parallex.svg",
	"076":    "polaris.svg",
	"101":    "providus.svg",
	"125":    "rubies.svg",
	"221":    "stanbic.svg",
	"068":    "standard-chartered.svg",
	"232":    "sterling.svg",
	"100":    "suntrust.svg",
	"302":    "taj.svg",
	"102":    "paystack-titan.svg",
	"032":    "union.svg",
	"033":    "uba.svg",
	"215":    "unity.svg",
	"035":    "wema.svg",
	"057":    "zenith.svg",
	"304":    "lotus.svg",
	"090267": "kuda.svg",
	"100002": "paga.svg",
	"110005": "paycom.svg",
	"090405": "moniepoint.svg",
	"090328": "eyowo.svg",
	"090175": "rubies.svg",
	"090110": "vfd.svg",
	"090286": "safehaven.svg",
	"090365": "corestep.svg",
	"090393": "bridgeway.svg",
	"090270": "ab-mfb.svg",
	"090371": "agosasa.svg",
	"090374": "amju.svg",
	"090376": "balogun.svg",
	"090377": "isaleoyo.svg",
	"090378": "golden-pastures.svg",
	"090392": "mozfin.svg",
	"090394": "nirsal.svg",
	"090395": "nwannegadi.svg",
	"090396": "oscotech.svg",
	"090399": "ndiorah.svg",
}

var nigerianBanks = []Bank{
	{Code: "044", Name: "Access Bank"},
	{Code: "063", Name: "Access Bank (Diamond)"},
	{Code: "401", Name: "ASO Savings and Loans"},
	{Code: "023", Name: "Citibank Nigeria"},
	{Code: "050", Name: "Ecobank Nigeria"},
	{Code: "562", Name: "Ekondo Microfinance Bank"},
	{Code: "070", Name: "Fidelity Bank"},
	{Code: "011", Name: "First Bank of Nigeria"},
	{Code: "214", Name: "First City Monument Bank"},
	{Code: "00103", Name: "Globus Bank"},
	{Code: "058", Name: "Guaranty Trust Bank"},
	{Code: "301", Name: "Jaiz Bank"},
	{Code: "082", Name: "Keystone Bank"},
	{Code: "526", Name: "Parallex Bank"},
	{Code: "076", Name: "Polaris Bank"},
	{Code: "101", Name: "Providus Bank"},
	{Code: "125", Name: "Rubies MFB"},
	{Code: "221", Name: "Stanbic IBTC Bank"},
	{Code: "068", Name: "Standard Chartered Bank"},
	{Code: "232", Name: "Sterling Bank"},
	{Code: "100", Name: "Suntrust Bank"},
	{Code: "302", Name: "TAJ Bank"},
	{Code: "102", Name: "Paystack-Titan"},
	{Code: "032", Name: "Union Bank of Nigeria"},
	{Code: "033", Name: "United Bank For Africa"},
	{Code: "215", Name: "Unity Bank"},
	{Code: "035", Name: "Wema Bank"},
	{Code: "057", Name: "Zenith Bank"},
	{Code: "304", Name: "Lotus Bank"},
	{Code: "50211", Name: "Kuda Bank"},
	{Code: "090267", Name: "Kuda Microfinance Bank"},
	{Code: "100002", Name: "Paga"},
	{Code: "110005", Name: "Paycom"},
	{Code: "090405", Name: "Moniepoint MFB"},
	{Code: "090328", Name: "Eyowo"},
	{Code: "090175", Name: "Rubies MFB"},
	{Code: "090110", Name: "VFD Microfinance Bank"},
	{Code: "090286", Name: "Safe Haven MFB"},
	{Code: "090365", Name: "Corestep MFB"},
	{Code: "090393", Name: "Bridgeway MFB"},
	{Code: "090270", Name: "AB Microfinance Bank"},
	{Code: "090371", Name: "Agosasa MFB"},
	{Code: "090374", Name: "Amju Unique MFB"},
	{Code: "090376", Name: "Balogun Gambari MFB"},
	{Code: "090377", Name: "Isaleoyo MFB"},
	{Code: "090378", Name: "New Golden Pastures MFB"},
	{Code: "090392", Name: "Mozfin MFB"},
	{Code: "090394", Name: "Nirsal MFB"},
	{Code: "090395", Name: "Nwannegadi MFB"},
	{Code: "090396", Name: "Oscotech MFB"},
	{Code: "090399", Name: "Ndiorah MFB"},
}

type BankService struct{}

func NewBankService() *BankService {
	return &BankService{}
}

func (bs *BankService) GetAllBanks(w http.ResponseWriter, r *http.Request) {
	banks := make([]Bank, len(nigerianBanks))
	copy(banks, nigerianBanks)

	for i := range banks {
		banks[i].LogoData = bs.LoadLogo(banks[i].Code)
		banks[i].UptimePrediction = bs.PredictUptime(banks[i].Code)
	}

	utils.SendSuccessResponse(w, "Banks Fetched Successfully", banks, http.StatusOK)
}

func (bs *BankService) LoadLogo(code string) string {
	filename, ok := bankLogos[code]
	if !ok {
		return svgFormat + base64.StdEncoding.EncodeToString([]byte(demoSVG))
	}

	path := filepath.Join(logosDir, filename)
	if data, err := os.ReadFile(path); err == nil {
		return svgFormat + base64.StdEncoding.EncodeToString(data)
	}

	return svgFormat + base64.StdEncoding.EncodeToString([]byte(demoSVG))
}

func (bs *BankService) PredictUptime(code string) float64 {
	tier1 := map[string]bool{"044": true, "063": true, "011": true, "058": true, "057": true, "033": true, "070": true, "214": true, "221": true, "232": true}
	tier2 := map[string]bool{"023": true, "050": true, "068": true, "032": true, "035": true, "076": true, "101": true, "102": true, "00103": true, "304": true}
	digital := map[string]bool{"50211": true, "090267": true, "090405": true, "100002": true, "110005": true}

	if tier1[code] {
		return 99.5
	}
	if tier2[code] {
		return 98.8
	}
	if digital[code] {
		return 99.2
	}
	return 97.5
}
