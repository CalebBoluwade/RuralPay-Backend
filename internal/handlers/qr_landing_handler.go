package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
)

type bankEntry struct {
	Name        string
	Logo        string
	Scheme      string
	FallbackURL string
}

// banks with logos that exist in static/bank-logos and their registered app URI schemes
var qrBanks = []bankEntry{
	{"GTBank", "gtbank.svg", "gtworld", "https://gtbank.com/gtworld"},
	{"Zenith Bank", "zenith.svg", "zenithmobile", "https://zenithbank.com/mobile"},
	{"Access Bank", "access-bank.svg", "accessbank", "https://accessbankplc.com/app"},
	{"First Bank", "firstbank.svg", "firstmobile", "https://firstbanknigeria.com/app"},
	{"UBA", "uba.svg", "ubamobile", "https://ubagroup.com/app"},
	{"FCMB", "fcmb.svg", "fcmbmobile", "https://fcmb.com/app"},
	{"Kuda", "kuda.svg", "kudabank", "https://kuda.com/app"},
	{"Moniepoint", "moniepoint.svg", "moniepoint", "https://moniepoint.com/app"},
	{"Wema / ALAT", "wema.svg", "alat", "https://alat.ng/app"},
	{"Sterling Bank", "sterling.svg", "sterlingmobile", "https://sterling.ng/app"},
	{"Polaris Bank", "polaris.svg", "polarismobile", "https://polarisbanklimited.com/app"},
	{"Keystone Bank", "keystone.svg", "keystonemobile", "https://keystonebankng.com/app"},
	{"Providus Bank", "providus.svg", "providusmobile", "https://providusbank.com/app"},
	{"Union Bank", "unionbank.svg", "unionmobile", "https://unionbankng.com/app"},
	{"Standard Chartered", "standard-chartered.svg", "scmobile", "https://sc.com/ng/app"},
	{"PocketApp", "pocketapp.svg", "pocketapp", "https://pocketapp.com.ng/app"},
	{"Paystack-Titan", "paystack-titan.svg", "paystack", "https://paystack.com/app"},
	{"Paycom (OPay)", "paycom.svg", "opay", "https://opay-inc.com/app"},
}

var qrLandingTmpl = template.Must(template.ParseFiles("./static/templates/qr_landing.html"))

type qrLandingData struct {
	Token string
	Nonce string
	Banks []bankEntry
}

func QRLandingHandler(w http.ResponseWriter, r *http.Request) {
	nonce, err := generateNonce()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Security-Policy", fmt.Sprintf(
		"default-src 'self'; script-src 'self' 'nonce-%s'; style-src 'self'",
		nonce,
	))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	qrLandingTmpl.Execute(w, qrLandingData{
		Token: r.URL.Query().Get("token"),
		Nonce: nonce,
		Banks: qrBanks,
	})
}

func generateNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
