package services

import (
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ruralpay/backend/internal/hsm"
	"github.com/ruralpay/backend/internal/utils"
)

type HSMKeyService struct {
	db  *sql.DB
	hsm hsm.HSMInterface
}

func NewHSMKeyService(db *sql.DB, hsm hsm.HSMInterface) *HSMKeyService {
	return &HSMKeyService{
		db:  db,
		hsm: hsm,
	}
}

// SyncKeysToDatabase syncs HSM keys to the database
func (s *HSMKeyService) SyncKeysToDatabase() error {
	// Get all key IDs that should exist
	keyIDs := []string{"transaction_signing"}

	for _, keyID := range keyIDs {
		if err := s.syncKeyToDatabase(keyID); err != nil {
			return fmt.Errorf("failed to sync key %s: %w", keyID, err)
		}
	}

	return nil
}

func (s *HSMKeyService) syncKeyToDatabase(keyID string) error {
	// Get public key from HSM
	publicKey, err := s.hsm.GetPublicKey(keyID)
	if err != nil {
		return fmt.Errorf("failed to get public key: %w", err)
	}

	// 2. Marshal the public key to PKIX (Required for WebCrypto/SPKI compatibility)
	publicKeyBytes, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("failed to marshal public key: %w", err)
	}

	// 3. Encode to PEM string
	block := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicKeyBytes,
	}
	publicKeyPEM := string(pem.EncodeToMemory(block))

	// Determine key type and size
	keyType, keySize := s.getKeyTypeAndSize(keyID, publicKeyPEM)

	// Call the database upsert function
	_, err = s.db.Exec(`
		SELECT upsert_hsm_key($1, $2, $3, $4, $5, $6, $7, $8)
	`, keyID, keyType, keyID, keySize, publicKeyPEM, "ENCRYPTED_BY_HSM",
		time.Now().Add(365*24*time.Hour), `{"synced_from_hsm": true}`)

	if err != nil {
		return fmt.Errorf("failed to upsert key to database: %w", err)
	}

	return nil
}

func (s *HSMKeyService) getKeyTypeAndSize(keyID, publicKeyPEM string) (string, int) {
	if keyID == "user_encryption" {
		return "AES", 256
	}

	// Parse RSA public key to get size
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block != nil {
		if pubKey, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
			if rsaKey, ok := pubKey.(*rsa.PublicKey); ok {
				return "RSA", rsaKey.Size() * 8
			}
		}
	}

	// Default fallback
	return "RSA", 2048
}

// CreateNewKeysExternal Creates User Signing Public Key
// @Summary Creates User Signing Public Key
// @Description Creates User Signing Public Key
// @Tags Keys
// @Produce json
// @Security BearerAuth
// @Success 200 {object} utils.APISuccessResponse
// @Failure 400 {object} utils.APIErrorResponse
// @Failure 401 {object} utils.APIErrorResponse
// @Router /encryption/keys [put]
func (s *HSMKeyService) CreateNewKeysExternal(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	userId, _ := utils.ExtractUserMerchantInfoFromContext(w, ctx)

	slog.Info("create.key.pair.external", "user_id", userId)

	_, err := s.hsm.GenerateAndSaveKeyPairExternal("app_signing")
	if err != nil {
		slog.ErrorContext(req.Context(), "failed to generate key pair external for app_signing: %v", err)
		utils.SendErrorResponse(w, "Public Key Not Found", http.StatusNotFound, nil)
	}

	utils.SendSuccessResponse(w, "Key Generated Successfully", nil, http.StatusOK)
}

// GetUserPublicKeys Retrieves User Signing Public Key
// @Summary Retrieves User Signing Public Key
// @Description Retrieves User Signing Public Key
// @Tags Keys
// @Produce json
// @Success 200 {object} utils.APISuccessResponse
// @Failure 400 {object} utils.APIErrorResponse
// @Failure 401 {object} utils.APIErrorResponse
// @Router /encryption/keys [get]
func (s *HSMKeyService) GetUserPublicKeys(w http.ResponseWriter, r *http.Request) {
	publicKey, err := s.hsm.GetPublicKey("app_signing_public")

	if err != nil {
		utils.SendErrorResponse(w, "Public Key Not Found", http.StatusNotFound, nil)
		return
	}

	// 2. Marshal the public key to PKIX (Required for WebCrypto/SPKI compatibility)
	publicKeyBytes, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		utils.SendErrorResponse(w, "failed to marshal public key", http.StatusInternalServerError, nil)
		return
	}

	// 3. Encode to PEM string
	block := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicKeyBytes,
	}
	userPublicKeyPEM := string(pem.EncodeToMemory(block))

	utils.SendSuccessResponse(w, "", map[string]string{
		"publicKey": userPublicKeyPEM,
		"algorithm": "RSA-OAEP",
		"hash":      "SHA-256",
	}, http.StatusOK)
}
