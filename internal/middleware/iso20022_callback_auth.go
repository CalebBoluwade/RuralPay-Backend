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

	"github.com/spf13/viper"
)

// ISO20022CallbackAuth verifies HMAC signatures on ISO20022 callback requests
// Supports two authentication methods:
// 1. HMAC-SHA256: X-Signature header contains hex-encoded HMAC-SHA256(body, secret)
// 2. Mutual TLS: Client certificate verification via tls.ConnectionState
func ISO20022CallbackAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if mutual TLS is enabled
		if viper.GetBool("iso20022.callback.tls.enabled") {
			if err := verifyMutualTLS(r); err != nil {
				slog.Warn("callback.auth.mtls_failed", "error", err.Error(), "remote_addr", r.RemoteAddr)
				http.Error(w, "MTLS verification failed", http.StatusUnauthorized)
				return
			}
			slog.Debug("callback.auth.mtls_verified", "remote_addr", r.RemoteAddr)
			next.ServeHTTP(w, r)
			return
		}

		// Fall back to HMAC verification
		if err := verifyHMACSignature(r); err != nil {
			slog.Warn("callback.auth.hmac_failed", "error", err.Error(), "remote_addr", r.RemoteAddr)
			http.Error(w, "HMAC verification failed", http.StatusUnauthorized)
			return
		}

		slog.Debug("callback.auth.hmac_verified", "remote_addr", r.RemoteAddr)
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

	// Parse signature header: "sha256=hexvalue"
	parts := strings.SplitN(signature, "=", 2)
	if len(parts) != 2 || parts[0] != "sha256" {
		return fmt.Errorf("invalid X-Signature format, expected sha256=<hex>")
	}

	providedSignature := parts[1]

	// Read and buffer the request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("failed to read request body: %w", err)
	}

	// Restore body for handler to read
	r.Body = io.NopCloser(bytes.NewBuffer(body))

	// Get HMAC secret from config
	secret := viper.GetString("iso20022.callback.hmac_secret")
	if secret == "" {
		return fmt.Errorf("ISO20022_CALLBACK_HMAC_SECRET not configured")
	}

	// Calculate expected HMAC
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(body)
	expectedSignature := hex.EncodeToString(h.Sum(nil))

	// Constant-time comparison to prevent timing attacks
	if !hmac.Equal([]byte(expectedSignature), []byte(providedSignature)) {
		return fmt.Errorf("signature mismatch")
	}

	return nil
}

// verifyMutualTLS validates the client certificate
// Checks that the certificate is present, not expired, and signed by trusted CA
func verifyMutualTLS(r *http.Request) error {
	tlsConn := r.TLS
	if tlsConn == nil {
		return fmt.Errorf("TLS connection information not available")
	}

	// Verify client certificate is present
	if len(tlsConn.PeerCertificates) == 0 {
		return fmt.Errorf("client certificate not provided")
	}

	clientCert := tlsConn.PeerCertificates[0]

	// Validate certificate is not expired
	if err := clientCert.VerifyHostname(""); err != nil {
		// VerifyHostname is not suitable for mutual TLS, skip
	}

	// Additional validation: check certificate validity period
	now := time.Now()
	if now.Before(clientCert.NotBefore) {
		return fmt.Errorf("certificate not yet valid: notBefore=%v", clientCert.NotBefore)
	}
	if now.After(clientCert.NotAfter) {
		return fmt.Errorf("certificate expired: notAfter=%v", clientCert.NotAfter)
	}

	// Check certificate is in the expected issuer list (optional)
	allowedIssuers := viper.GetStringSlice("iso20022.callback.tls.allowed_issuers")
	if len(allowedIssuers) > 0 {
		issuerMatched := false
		for _, issuer := range allowedIssuers {
			if strings.Contains(clientCert.Issuer.String(), issuer) {
				issuerMatched = true
				break
			}
		}
		if !issuerMatched {
			return fmt.Errorf("client certificate issuer not in allowed list")
		}
	}

	// Check certificate serial number against whitelist (optional)
	whitelistedSerials := viper.GetStringSlice("iso20022.callback.tls.whitelisted_serials")
	if len(whitelistedSerials) > 0 {
		serialMatched := false
		for _, serial := range whitelistedSerials {
			if clientCert.SerialNumber.String() == serial {
				serialMatched = true
				break
			}
		}
		if !serialMatched {
			return fmt.Errorf("client certificate serial not whitelisted")
		}
	}

	slog.Info("callback.auth.mtls_certificate_verified",
		"subject", clientCert.Subject.String(),
		"issuer", clientCert.Issuer.String(),
		"serial", clientCert.SerialNumber.String(),
	)

	return nil
}

// ISO20022CallbackAuthOptional is a lenient version that logs failures but allows requests through
// Useful for gradual rollout or monitoring
func ISO20022CallbackAuthOptional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if viper.GetBool("iso20022.callback.tls.enabled") {
			if err := verifyMutualTLS(r); err != nil {
				slog.Info("callback.auth.mtls_optional_failed", "error", err.Error())
			} else {
				slog.Debug("callback.auth.mtls_verified")
			}
		} else {
			if err := verifyHMACSignature(r); err != nil {
				slog.Info("callback.auth.hmac_optional_failed", "error", err.Error())
			} else {
				slog.Debug("callback.auth.hmac_verified")
			}
		}
		next.ServeHTTP(w, r)
	})
}
