package middleware

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/viper"
)

// sanitizeISO strips control characters to prevent log injection.
func sanitizeISO(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// ISO20022CallbackAuth verifies HMAC signatures on ISO20022 callback requests
// Supports two authentication methods:
// 1. HMAC-SHA256: X-Signature header contains hex-encoded HMAC-SHA256(body, secret)
// 2. Mutual TLS: Client certificate verification via tls.ConnectionState
func ISO20022CallbackAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callbackEndpoints := map[string]bool{
			"/pacs008": true,
			"/pacs002": true,
			"/pacs028": true,
			"/acmt023": true,
			"/acmt024": true,
		}

		if !callbackEndpoints[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		if viper.GetBool("iso20022.callback.tls.enabled") {
			if err := verifyMutualTLS(r); err != nil {
				slog.Warn("callback.auth.mtls_failed", "error", sanitizeISO(err.Error()), "remote_addr", sanitizeISO(r.RemoteAddr))
				http.Error(w, "MTLS verification failed", http.StatusUnauthorized)
				return
			}
			slog.Debug("callback.auth.mtls_verified", "remote_addr", sanitizeISO(r.RemoteAddr))
			next.ServeHTTP(w, r)
			return
		}

		if err := verifyHMACSignature(r); err != nil {
			slog.Warn("callback.auth.hmac_failed", "error", sanitizeISO(err.Error()), "remote_addr", sanitizeISO(r.RemoteAddr))
			http.Error(w, "HMAC verification failed", http.StatusUnauthorized)
			return
		}

		slog.Debug("callback.auth.hmac_verified", "remote_addr", sanitizeISO(r.RemoteAddr))
		next.ServeHTTP(w, r)
	})
}

// verifyHMACSignature validates the X-Signature header using HMAC-SHA256
// Expected header format: X-Signature: sha256=<hex-encoded-hmac>
func verifyHMACSignature(r *http.Request) error {
	signature := r.Header.Get("X-Signature")
	if signature == "" {
		return fmt.Errorf("missing X-Signature header")
	}

	parts := strings.SplitN(signature, "=", 2)
	if len(parts) != 2 || parts[0] != "sha256" {
		return fmt.Errorf("invalid X-Signature format, expected sha256=<hex>")
	}

	providedSignature := parts[1]

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("failed to read request body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewBuffer(body))

	secret := viper.GetString("iso20022.callback.hmac_secret")
	if secret == "" {
		return fmt.Errorf("ISO20022_CALLBACK_HMAC_SECRET not configured")
	}

	h := hmac.New(sha256.New, []byte(secret))
	h.Write(body)
	expectedSignature := hex.EncodeToString(h.Sum(nil))

	if !hmac.Equal([]byte(expectedSignature), []byte(providedSignature)) {
		return fmt.Errorf("signature mismatch")
	}

	return nil
}

// verifyMutualTLS validates the client certificate against validity period,
// allowed issuers, and whitelisted serials.
func verifyMutualTLS(r *http.Request) error {
	if r.TLS == nil {
		return fmt.Errorf("TLS connection information not available")
	}
	if len(r.TLS.PeerCertificates) == 0 {
		return fmt.Errorf("client certificate not provided")
	}

	clientCert := r.TLS.PeerCertificates[0]
	now := time.Now()

	if now.Before(clientCert.NotBefore) {
		return fmt.Errorf("certificate not yet valid")
	}
	if now.After(clientCert.NotAfter) {
		return fmt.Errorf("certificate expired")
	}

	if err := checkAllowedIssuers(clientCert.Issuer.String()); err != nil {
		return err
	}
	if err := checkWhitelistedSerials(clientCert.SerialNumber.String()); err != nil {
		return err
	}

	slog.Info("callback.auth.mtls_certificate_verified",
		"subject", sanitizeISO(clientCert.Subject.String()),
		"issuer", sanitizeISO(clientCert.Issuer.String()),
		"serial", sanitizeISO(clientCert.SerialNumber.String()),
	)
	return nil
}

func checkAllowedIssuers(issuer string) error {
	allowed := viper.GetStringSlice("iso20022.callback.tls.allowed_issuers")
	if len(allowed) == 0 {
		return nil
	}
	for _, a := range allowed {
		if strings.Contains(issuer, a) {
			return nil
		}
	}
	return fmt.Errorf("client certificate issuer not in allowed list")
}

func checkWhitelistedSerials(serial string) error {
	whitelisted := viper.GetStringSlice("iso20022.callback.tls.whitelisted_serials")
	if len(whitelisted) == 0 {
		return nil
	}
	for _, s := range whitelisted {
		if serial == s {
			return nil
		}
	}
	return fmt.Errorf("client certificate serial not whitelisted")
}

// ISO20022CallbackAuthOptional is a lenient version that logs failures but allows requests through
func ISO20022CallbackAuthOptional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if viper.GetBool("iso20022.callback.tls.enabled") {
			if err := verifyMutualTLS(r); err != nil {
				slog.Info("callback.auth.mtls_optional_failed", "error", sanitizeISO(err.Error()))
			} else {
				slog.Debug("callback.auth.mtls_verified")
			}
		} else {
			if err := verifyHMACSignature(r); err != nil {
				slog.Info("callback.auth.hmac_optional_failed", "error", sanitizeISO(err.Error()))
			} else {
				slog.Debug("callback.auth.hmac_verified")
			}
		}
		next.ServeHTTP(w, r)
	})
}
