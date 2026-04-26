package handlers

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"regexp"

	"github.com/spf13/viper"
)

type bankEntry struct {
	Name        string
	Logo        string
	Scheme      string
	Route       string
	FallbackURL string
}

// banks with logos that exist in static/bank-logos and their registered app URI schemes
var qrBanks = []bankEntry{
	{"GTBank", "gtbank.svg", "gtworld", "", "https://gtbank.com/gtworld"},
	{"Zenith Bank", "zenith.svg", "zenithmobile", "", "https://zenithbank.com/mobile"},
	{"Access Bank", "access-bank.svg", "accessbank", "", "https://accessbankplc.com/app"},
	{"First Bank", "firstbank.svg", "firstmobile", "", "https://firstbanknigeria.com/app"},
	{"UBA", "uba.svg", "ubamobile", "", "https://ubagroup.com/app"},
	{"FCMB", "fcmb.svg", "fcmbmobile", "", "https://fcmb.com/app"},
	{"Kuda", "kuda.svg", "kudabank", "", "https://kuda.com/app"},
	{"Moniepoint", "moniepoint.svg", "moniepoint", "", "https://moniepoint.com/app"},
	{"Wema / ALAT", "wema.svg", "alat", "", "https://alat.ng/app"},
	{"Sterling Bank", "sterling.svg", "sterlingmobile", "", "https://sterling.ng/app"},
	{"Polaris Bank", "polaris.svg", "polarismobile", "", "https://polarisbanklimited.com/app"},
	{"Keystone Bank", "keystone.svg", "keystonemobile", "", "https://keystonebankng.com/app"},
	{"Providus Bank", "providus.svg", "providusmobile", "", "https://providusbank.com/app"},
	{"Union Bank", "unionbank.svg", "unionmobile", "", "https://unionbankng.com/app"},
	{"Standard Chartered", "standard-chartered.svg", "scmobile", "", "https://sc.com/ng/app"},
	{"PocketApp", "pocketapp.svg", "pocketapp", "", "https://pocketapp.com.ng/app"},
	{"Paystack-Titan", "paystack-titan.svg", "paystack", "", "https://paystack.com/app"},
	{"Paycom (OPay)", "paycom.svg", "opay", "", "https://opay-inc.com/app"},
}

// qrTokenRe restricts tokens to alphanumeric, hyphen and underscore only.
var qrTokenRe = regexp.MustCompile(`^[A-Za-z0-9\-_]{1,128}$`)

var qrLandingTmpl = template.Must(template.ParseFiles("./static/templates/qr_landing.html"))

type qrLandingData struct {
	Token   string
	Nonce   string
	Banks   []bankEntry
}

func ruralPayEntry(token string) bankEntry {
	scheme := viper.GetString("app.scheme")
	if scheme == "" {
		scheme = "ruralpay"
	}
	route := viper.GetString("app.qr_route")
	if route == "" {
		route = "pay/qr"
	}
	domain := viper.GetString("app.domain")
	if domain == "" {
		domain = "app.ruralpay.com"
	}
	fallback := template.URL(fmt.Sprintf("https://%s/%s?token=%s", domain, route, token))
	return bankEntry{
		Name:        "RuralPay",
		Logo:        "RuralPay.png",
		Scheme:      scheme,
		Route:       route,
		FallbackURL: string(fallback),
	}
}

func QRLandingHandler(w http.ResponseWriter, r *http.Request) {
	nonce, err := generateNonce()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	token := r.URL.Query().Get("token")
	if !qrTokenRe.MatchString(token) {
		http.Error(w, "invalid token", http.StatusBadRequest)
		return
	}

	banks := append([]bankEntry{ruralPayEntry(token)}, qrBanks...)

	var buf bytes.Buffer
	if err := qrLandingTmpl.Execute(&buf, qrLandingData{
		Token: token,
		Nonce: nonce,
		Banks: banks,
	}); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Security-Policy", fmt.Sprintf(
		"default-src 'self'; script-src 'self' 'nonce-%s'; style-src 'self'",
		nonce,
	))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func generateNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
