package utils

import (
	"testing"
)

func TestISO20022CallbackSignature_GenerateSignature(t *testing.T) {
	tests := []struct {
		name     string
		secret   string
		body     string
		expected string
	}{
		{
			name:     "valid signature generation",
			secret:   "test-secret-key",
			body:     "<Document>test</Document>",
			expected: "sha256=", // We'll verify the format, not exact value since HMAC is deterministic
		},
		{
			name:     "empty body",
			secret:   "test-secret-key",
			body:     "",
			expected: "sha256=",
		},
		{
			name:     "long secret",
			secret:   "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1",
			body:     "<Document>long secret test</Document>",
			expected: "sha256=",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sig := NewISO20022CallbackSignature(tt.secret)
			result := sig.GenerateSignatureString(tt.body)

			// Should start with sha256=
			if len(result) < len("sha256=") {
				t.Errorf("signature too short: %s", result)
			}

			// Should start with "sha256="
			if !startsWith(result, "sha256=") {
				t.Errorf("signature should start with 'sha256=', got %s", result)
			}

			// Signature part should be 64 hex characters (SHA256 = 32 bytes = 64 hex chars)
			sigPart := result[7:] // Skip "sha256="
			if len(sigPart) != 64 {
				t.Errorf("signature part should be 64 hex chars, got %d", len(sigPart))
			}

			// Should only contain hex characters
			for _, c := range sigPart {
				if !isHexChar(c) {
					t.Errorf("signature contains non-hex character: %c", c)
				}
			}
		})
	}
}

func TestISO20022CallbackSignature_VerifySignature(t *testing.T) {
	const secret = "test-secret-key"
	sig := NewISO20022CallbackSignature(secret)

	tests := []struct {
		name          string
		body          string
		signatureFunc func(string) string
		shouldVerify  bool
		shouldError   bool
		errorContains string
	}{
		{
			name: "valid signature",
			body: "<Document>test</Document>",
			signatureFunc: func(body string) string {
				return sig.GenerateSignatureString(body)
			},
			shouldVerify: true,
			shouldError:  false,
		},
		{
			name: "invalid signature",
			body: "<Document>test</Document>",
			signatureFunc: func(body string) string {
				return "sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			},
			shouldVerify: false,
			shouldError:  false,
		},
		{
			name: "wrong algorithm",
			body: "<Document>test</Document>",
			signatureFunc: func(body string) string {
				return "md5=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			},
			shouldVerify:  false,
			shouldError:   true,
			errorContains: "unsupported algorithm",
		},
		{
			name: "malformed signature - no equals",
			body: "<Document>test</Document>",
			signatureFunc: func(body string) string {
				return "sha256aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			},
			shouldVerify:  false,
			shouldError:   true,
			errorContains: "invalid signature format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signatureHeader := tt.signatureFunc(tt.body)
			valid, err := sig.VerifySignatureString(tt.body, signatureHeader)

			if tt.shouldError && err == nil {
				t.Errorf("expected error, got nil")
			}

			if !tt.shouldError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if tt.shouldError && err != nil && tt.errorContains != "" {
				if !contains(err.Error(), tt.errorContains) {
					t.Errorf("error should contain '%s', got '%s'", tt.errorContains, err.Error())
				}
			}

			if valid != tt.shouldVerify {
				t.Errorf("expected valid=%v, got valid=%v", tt.shouldVerify, valid)
			}
		})
	}
}

func TestISO20022CallbackSignature_DifferentSecret(t *testing.T) {
	body := "<Document>test</Document>"

	sig1 := NewISO20022CallbackSignature("secret-1")
	signature1 := sig1.GenerateSignatureString(body)

	sig2 := NewISO20022CallbackSignature("secret-2")
	valid, err := sig2.VerifySignatureString(body, signature1)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if valid {
		t.Errorf("signature with different secret should not verify")
	}
}

func TestISO20022CallbackSignature_ModifiedBody(t *testing.T) {
	sig := NewISO20022CallbackSignature("test-secret")

	originalBody := "<Document><Amount>100</Amount></Document>"
	signature := sig.GenerateSignatureString(originalBody)

	modifiedBody := "<Document><Amount>200</Amount></Document>"
	valid, err := sig.VerifySignatureString(modifiedBody, signature)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if valid {
		t.Errorf("modified body should not verify with original signature")
	}
}

func TestValidateSignatureFormat(t *testing.T) {
	tests := []struct {
		name        string
		signature   string
		shouldError bool
		errorMsg    string
	}{
		{
			name:        "valid signature format",
			signature:   "sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			shouldError: false,
		},
		{
			name:        "empty signature",
			signature:   "",
			shouldError: true,
			errorMsg:    "empty",
		},
		{
			name:        "no equals sign",
			signature:   "sha256aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			shouldError: true,
			errorMsg:    "invalid format",
		},
		{
			name:        "wrong algorithm",
			signature:   "md5=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			shouldError: true,
			errorMsg:    "unsupported algorithm",
		},
		{
			name:        "empty signature value",
			signature:   "sha256=",
			shouldError: true,
			errorMsg:    "empty",
		},
		{
			name:        "invalid hex",
			signature:   "sha256=zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
			shouldError: true,
			errorMsg:    "not valid hex",
		},
		{
			name:        "too short hex",
			signature:   "sha256=aaaaaaaaaa",
			shouldError: true,
			errorMsg:    "invalid SHA256 hex length",
		},
		{
			name:        "too long hex",
			signature:   "sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaabbbb",
			shouldError: true,
			errorMsg:    "invalid SHA256 hex length",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSignatureFormat(tt.signature)

			if tt.shouldError && err == nil {
				t.Errorf("expected error, got nil")
			}

			if !tt.shouldError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if tt.shouldError && err != nil && tt.errorMsg != "" {
				if !contains(err.Error(), tt.errorMsg) {
					t.Errorf("error should contain '%s', got '%s'", tt.errorMsg, err.Error())
				}
			}
		})
	}
}

// Helper functions
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func isHexChar(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
