package utils

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// ISO20022CallbackSignature provides utility functions for testing and generating
// callback signatures for ISO20022 messages
type ISO20022CallbackSignature struct {
	Secret string
}

// NewISO20022CallbackSignature creates a new signature utility
func NewISO20022CallbackSignature(secret string) *ISO20022CallbackSignature {
	return &ISO20022CallbackSignature{Secret: secret}
}

// GenerateSignature creates an HMAC-SHA256 signature for the given body
// Returns signature in the format: sha256=<hex-encoded-signature>
func (s *ISO20022CallbackSignature) GenerateSignature(body []byte) string {
	h := hmac.New(sha256.New, []byte(s.Secret))
	h.Write(body)
	signature := hex.EncodeToString(h.Sum(nil))
	return fmt.Sprintf("sha256=%s", signature)
}

// GenerateSignatureString is a convenience wrapper for string bodies
func (s *ISO20022CallbackSignature) GenerateSignatureString(body string) string {
	return s.GenerateSignature([]byte(body))
}

// VerifySignature checks if the provided signature matches the body
// Returns (valid, error)
func (s *ISO20022CallbackSignature) VerifySignature(body []byte, signatureHeader string) (bool, error) {
	// Parse signature header: "sha256=hexvalue"
	parts := strings.SplitN(signatureHeader, "=", 2)
	if len(parts) != 2 {
		return false, fmt.Errorf("invalid signature format, expected sha256=<hex>")
	}

	algorithm := parts[0]
	if algorithm != "sha256" {
		return false, fmt.Errorf("unsupported algorithm: %s, expected sha256", algorithm)
	}

	providedSignature := parts[1]

	// Calculate expected signature
	expected := s.GenerateSignature(body)
	expected = strings.TrimPrefix(expected, "sha256=")

	// Constant-time comparison to prevent timing attacks
	isValid := hmac.Equal([]byte(expected), []byte(providedSignature))
	return isValid, nil
}

// VerifySignatureString is a convenience wrapper for string bodies
func (s *ISO20022CallbackSignature) VerifySignatureString(body string, signatureHeader string) (bool, error) {
	return s.VerifySignature([]byte(body), signatureHeader)
}

// ValidateSignatureFormat checks if the signature header has correct format
// without verifying against actual secret
func ValidateSignatureFormat(signatureHeader string) error {
	if signatureHeader == "" {
		return fmt.Errorf("signature header is empty")
	}

	parts := strings.SplitN(signatureHeader, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid format: expected 'algorithm=value'")
	}

	algorithm := parts[0]
	if algorithm != "sha256" {
		return fmt.Errorf("unsupported algorithm: %s, expected sha256", algorithm)
	}

	signatureValue := parts[1]
	if signatureValue == "" {
		return fmt.Errorf("signature value is empty")
	}

	// Check if it's valid hex
	if _, err := hex.DecodeString(signatureValue); err != nil {
		return fmt.Errorf("signature value is not valid hex: %w", err)
	}

	// SHA256 produces 32 bytes = 64 hex characters
	if len(signatureValue) != 64 {
		return fmt.Errorf("invalid SHA256 hex length: got %d, expected 64", len(signatureValue))
	}

	return nil
}
