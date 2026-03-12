package hsm

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

// HSMInterface defines the HSM operations
type HSMInterface interface {
	// Key Management
	GenerateKeyPair(keyID string) (*KeyPair, error)
	GetPublicKey(keyID string) (string, error)
	DeleteKey(keyID string) error
	RotateKeys() error

	// Encryption/Decryption
	EncryptData(keyID string, plaintext []byte) ([]byte, error)
	DecryptData(keyID string, ciphertext []byte) ([]byte, error)

	// Signing/Verification
	SignData(keyID string, data []byte) ([]byte, error)
	VerifySignature(keyID string, data, signature []byte) (bool, error)

	// Card Operations
	GenerateCardSignature(cardData *CardData) (string, error)
	VerifyCardSignature(cardData *CardData, signature string) (bool, error)
	EncryptCardData(cardData *CardData) (string, error)
	DecryptCardData(encryptedData string) (*CardData, error)

	// Transaction Security
	GenerateTransactionID() string
	SignTransaction(transaction *Transaction) (string, error)
	VerifyTransaction(transaction *Transaction, signature string) (bool, error)

	// PIN Operations
	HashPIN(pin string, salt []byte) (string, error)
	VerifyPIN(pin string, hashedPIN string) (bool, error)

	// PII Operations
	DecryptPII(encryptedData string) (string, error)
}

// SoftwareHSM implements HSMInterface
type SoftwareHSM struct {
	keys         map[string]*KeyPair
	masterKey    []byte
	mu           sync.RWMutex
	keyStorePath string
	auditLogger  AuditLogger
}

// KeyPair holds RSA key pair
type KeyPair struct {
	ID         string
	PublicKey  *rsa.PublicKey
	PrivateKey *rsa.PrivateKey
	CreatedAt  time.Time
	ExpiresAt  time.Time
	IsActive   bool
}

// CardData for NFC card operations
type CardData struct {
	CardID      string    `json:"card_id"`
	UserID      string    `json:"user_id"`
	Balance     float64   `json:"balance"`
	Currency    string    `json:"currency"`
	LastUpdated time.Time `json:"last_updated"`
	TxCounter   int       `json:"tx_counter"`
}

// Transaction for signing
type Transaction struct {
	ID            string    `json:"id"`
	FromAccountID string    `json:"from_account_id"`
	ToAccountID   string    `json:"to_account_id"`
	Amount        float64   `json:"amount"`
	Timestamp     time.Time `json:"timestamp"`
	Nonce         string    `json:"nonce"`
}

// Config holds HSM configuration
type Config struct {
	HSMType         string // "software" or "hardware"
	MasterKey       string
	KeyStorePath    string
	KeyRotationDays int
	AuditLogger     AuditLogger
	Salt            []byte // Optional: if nil, will be generated
}

// InitHSM is a factory function that initializes the appropriate HSM implementation.
func InitHSM(config Config) (HSMInterface, error) {
	switch config.HSMType {
	case "hardware":
		return NewHardwareHSM()
	case "software":
		return NewSoftwareHSM(config)
	default:
		return nil, fmt.Errorf("invalid HSMType: %s", config.HSMType)
	}
}

// NewSoftwareHSM initializes the software-based HSM server.
func NewSoftwareHSM(config Config) (*SoftwareHSM, error) {
	if config.MasterKey == "" {
		return nil, errors.New("Master Key Required for software HSM")
	}

	log.Println("Software HSM Initialized Successfully")

	// Generate or use provided salt
	salt := config.Salt
	if salt == nil {
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, fmt.Errorf("failed to generate salt: %w", err)
		}
	}

	// Derive master key using Argon2
	masterKey := deriveKey(config.MasterKey, string(salt), 32)

	hsm := &SoftwareHSM{
		keys:         make(map[string]*KeyPair),
		masterKey:    masterKey,
		keyStorePath: config.KeyStorePath,
		auditLogger:  config.AuditLogger,
	}

	// Load existing keys
	if err := hsm.loadKeys(); err != nil {
		return nil, fmt.Errorf("failed to load keys: %w", err)
	}

	// Generate default keys if none exist
	if len(hsm.keys) == 0 {
		if err := hsm.generateDefaultKeys(); err != nil {
			return nil, fmt.Errorf("failed to generate default keys: %w", err)
		}
	}

	return hsm, nil
}

// GenerateKeyPair creates a new RSA key pair
func (h *SoftwareHSM) GenerateKeyPair(keyID string) (*KeyPair, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Validate keyID to prevent path traversal
	if err := validateKeyID(keyID); err != nil {
		return nil, fmt.Errorf("invalid key ID: %w", err)
	}

	// Check if key already exists
	if _, exists := h.keys[keyID]; exists {
		return nil, fmt.Errorf("key with ID %s already exists", keyID)
	}

	// Generate RSA key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate RSA key: %w", err)
	}

	keyPair := &KeyPair{
		ID:         keyID,
		PublicKey:  &privateKey.PublicKey,
		PrivateKey: privateKey,
		CreatedAt:  time.Now(),
		ExpiresAt:  time.Now().Add(365 * 24 * time.Hour), // 1 year
		IsActive:   true,
	}

	h.keys[keyID] = keyPair

	// Save to disk
	if err := h.saveKeyToDisk(keyPair); err != nil {
		delete(h.keys, keyID)
		return nil, fmt.Errorf("failed to save key to disk: %w", err)
	}

	h.auditLogger.LogTransfer("KEY_GENERATED", keyID, "system", 0, "New key pair generated")
	return keyPair, nil
}

// GetPublicKey returns the public key in PEM format
func (h *SoftwareHSM) GetPublicKey(keyID string) (string, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	keyPair, exists := h.keys[keyID]
	if !exists {
		return "", fmt.Errorf("key %s not found", keyID)
	}

	if !keyPair.IsActive {
		return "", fmt.Errorf("key %s is not active", keyID)
	}

	// Encode public key to PEM
	publicKeyBytes, err := x509.MarshalPKIXPublicKey(keyPair.PublicKey)
	if err != nil {
		return "", fmt.Errorf("failed to marshal public key: %w", err)
	}

	publicKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PUBLIC KEY",
		Bytes: publicKeyBytes,
	})

	return string(publicKeyPEM), nil
}

// EncryptData encrypts data using AES-GCM
func (h *SoftwareHSM) EncryptData(keyID string, plaintext []byte) ([]byte, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	_, exists := h.keys[keyID]
	if !exists {
		return nil, fmt.Errorf("key %s not found", keyID)
	}

	// Generate a random nonce
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Create AES-GCM cipher
	block, err := aes.NewCipher(h.masterKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// Encrypt data
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	// Combine nonce + ciphertext
	result := make([]byte, len(nonce)+len(ciphertext))
	copy(result, nonce)
	copy(result[len(nonce):], ciphertext)

	return result, nil
}

// DecryptData decrypts AES-GCM encrypted data
func (h *SoftwareHSM) DecryptData(keyID string, ciphertext []byte) ([]byte, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if _, exists := h.keys[keyID]; !exists {
		return nil, fmt.Errorf("key %s not found", keyID)
	}

	if len(ciphertext) < 12 {
		return nil, errors.New("ciphertext too short")
	}

	// Extract nonce
	nonce := ciphertext[:12]
	ciphertext = ciphertext[12:]

	// Create AES-GCM cipher
	block, err := aes.NewCipher(h.masterKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// Decrypt data
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	return plaintext, nil
}

// SignData signs data with RSA private key
func (h *SoftwareHSM) SignData(keyID string, data []byte) ([]byte, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	keyPair, exists := h.keys[keyID]
	if !exists {
		return nil, fmt.Errorf("key %s not found", keyID)
	}

	if !keyPair.IsActive {
		return nil, fmt.Errorf("key %s is not active", keyID)
	}

	// Hash the data
	hashed := sha256.Sum256(data)

	// Sign the hash
	signature, err := rsa.SignPKCS1v15(rand.Reader, keyPair.PrivateKey, crypto.SHA256, hashed[:])
	if err != nil {
		return nil, fmt.Errorf("failed to sign data: %w", err)
	}

	return signature, nil
}

// VerifySignature verifies RSA signature
func (h *SoftwareHSM) VerifySignature(keyID string, data, signature []byte) (bool, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	keyPair, exists := h.keys[keyID]
	if !exists {
		return false, fmt.Errorf("key %s not found", keyID)
	}

	// Hash the data
	hashed := sha256.Sum256(data)

	// Verify the signature
	err := rsa.VerifyPKCS1v15(keyPair.PublicKey, crypto.SHA256, hashed[:], signature)
	if err != nil {
		return false, nil
	}

	return true, nil
}

// GenerateCardSignature creates a signature for card data
func (h *SoftwareHSM) GenerateCardSignature(cardData *CardData) (string, error) {
	// Create data to sign
	data := fmt.Sprintf("%s:%s:%.2f:%s:%d:%s",
		cardData.CardID,
		cardData.UserID,
		cardData.Balance,
		cardData.Currency,
		cardData.TxCounter,
		cardData.LastUpdated.Format(time.RFC3339),
	)

	// Sign with card key
	signature, err := h.SignData("card_signing", []byte(data))
	if err != nil {
		return "", fmt.Errorf("failed to sign card data: %w", err)
	}

	// Encode signature to base64
	return base64.StdEncoding.EncodeToString(signature), nil
}

// VerifyCardSignature verifies card signature
func (h *SoftwareHSM) VerifyCardSignature(cardData *CardData, signature string) (bool, error) {
	// Create data to verify
	data := fmt.Sprintf("%s:%s:%.2f:%s:%d:%s",
		cardData.CardID,
		cardData.UserID,
		cardData.Balance,
		cardData.Currency,
		cardData.TxCounter,
		cardData.LastUpdated.Format(time.RFC3339),
	)

	// Decode signature
	sigBytes, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false, fmt.Errorf("invalid signature format: %w", err)
	}

	// Verify signature
	return h.VerifySignature("card_signing", []byte(data), sigBytes)
}

// GenerateTransactionID creates a secure transaction ID
func (h *SoftwareHSM) GenerateTransactionID() string {
	uuid := uuid.New().String()
	timestamp := time.Now().UnixNano()
	random := make([]byte, 8)
	rand.Read(random)

	data := fmt.Sprintf("%s:%d:%x", uuid, timestamp, random)
	hashed := sha256.Sum256([]byte(data))

	return fmt.Sprintf("TX%x", hashed[:8])
}

// SignTransaction signs a transaction
func (h *SoftwareHSM) SignTransaction(transaction *Transaction) (string, error) {
	// Create data to sign
	data, err := json.Marshal(transaction)
	if err != nil {
		return "", fmt.Errorf("failed to marshal transaction for signing: %w", err)
	}
	// Sign with transaction key
	signature, err := h.SignData("transaction_signing", []byte(data))
	if err != nil {
		return "", fmt.Errorf("failed to sign transaction: %w", err)
	}

	return base64.StdEncoding.EncodeToString(signature), nil
}

// VerifyTransaction verifies transaction signature
func (h *SoftwareHSM) VerifyTransaction(transaction *Transaction, signature string) (bool, error) {
	// Create data to verify
	data, err := json.Marshal(transaction)
	if err != nil {
		return false, fmt.Errorf("failed to marshal transaction for verification: %w", err)
	}
	// Decode signature
	sigBytes, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false, fmt.Errorf("invalid signature format: %w", err)
	}

	// Verify signature
	if valid, _ := h.VerifySignature("transaction_signing", []byte(data), sigBytes); valid {
		return true, nil
	}

	// If primary key fails, try archived keys (fallback for rotated keys)
	var archivedKeys []string
	h.mu.RLock()
	for k := range h.keys {
		if strings.HasPrefix(k, "transaction_signing_") {
			archivedKeys = append(archivedKeys, k)
		}
	}
	h.mu.RUnlock()

	for _, keyID := range archivedKeys {
		if valid, _ := h.VerifySignature(keyID, []byte(data), sigBytes); valid {
			return true, nil
		}
	}

	return false, nil
}

// DecryptPII decrypts PII data using the user_encryption key
func (h *SoftwareHSM) DecryptPII(encryptedData string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encryptedData)
	if err != nil {
		return "", fmt.Errorf("invalid base64 PII data: %w", err)
	}

	decrypted, err := h.DecryptData("user_encryption", data)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt PII: %w", err)
	}

	return string(decrypted), nil
}

// HashPIN hashes a PIN using Argon2
func (h *SoftwareHSM) HashPIN(pin string, salt []byte) (string, error) {
	if len(salt) == 0 {
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return "", fmt.Errorf("failed to generate salt: %w", err)
		}
	}

	// Use Argon2id for PIN hashing
	hash := argon2.IDKey([]byte(pin), salt, 1, 64*1024, 4, 32)

	// Combine salt + hash
	result := make([]byte, len(salt)+len(hash))
	copy(result, salt)
	copy(result[len(salt):], hash)

	return base64.StdEncoding.EncodeToString(result), nil
}

// VerifyPIN verifies a PIN against its hash
func (h *SoftwareHSM) VerifyPIN(pin string, hashedPIN string) (bool, error) {
	// Decode hashed PIN
	decoded, err := base64.StdEncoding.DecodeString(hashedPIN)
	if err != nil {
		return false, fmt.Errorf("invalid PIN hash format: %w", err)
	}

	if len(decoded) < 16 {
		return false, errors.New("PIN hash too short")
	}

	// Extract salt and hash
	salt := decoded[:16]
	storedHash := decoded[16:]

	// Hash the input PIN with the same salt
	inputHash := argon2.IDKey([]byte(pin), salt, 1, 64*1024, 4, 32)

	// Compare hashes
	return subtle.ConstantTimeCompare(inputHash, storedHash) == 1, nil
}

// RotateKeys rotates expired or compromised keys
func (h *SoftwareHSM) RotateKeys() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Collect keys to rotate first to avoid modifying map during iteration
	var rotated []string
	var keysToRotate []string
	now := time.Now()

	for keyID, keyPair := range h.keys {
		// Only rotate base keys (not archived ones) that are expired or inactive
		if (now.After(keyPair.ExpiresAt) || !keyPair.IsActive) && !strings.Contains(keyID, "_1") {
			keysToRotate = append(keysToRotate, keyID)
		}
	}

	for _, keyID := range keysToRotate {
		oldKeyPair := h.keys[keyID]

		// 1. Archive the old key
		archiveID := fmt.Sprintf("%s_%d", keyID, now.Unix())
		archivedKey := *oldKeyPair // Shallow copy
		archivedKey.ID = archiveID
		archivedKey.IsActive = false // Archived keys cannot be used for new signing
		h.keys[archiveID] = &archivedKey
		h.saveKeyToDisk(&archivedKey)

		// 2. Generate new key for the base ID
		newKeyPair, err := h.generateKeyPairInternal(keyID)
		if err != nil {
			h.auditLogger.LogError(keyID, keyID, err)
			continue
		}
		h.keys[keyID] = newKeyPair
		h.saveKeyToDisk(newKeyPair)

		rotated = append(rotated, keyID)

		h.auditLogger.LogTransfer("KEY_ROTATED", keyID, archiveID, 0, "Key rotated")
	}

	h.auditLogger.LogTransfer("KEY_ROTATION_COMPLETE", "system", "system", int64(len(rotated)), "Key rotation complete")

	return nil
}

// DeleteKey removes a key from the HSM
func (h *SoftwareHSM) DeleteKey(keyID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, exists := h.keys[keyID]; !exists {
		return fmt.Errorf("key %s not found", keyID)
	}

	// Delete from memory
	delete(h.keys, keyID)

	// Validate keyID to prevent path traversal
	if err := validateKeyID(keyID); err != nil {
		return fmt.Errorf("invalid key ID: %w", err)
	}

	// Delete from disk using secure path construction
	keyPath := filepath.Join(h.keyStorePath, keyID+".key")
	if err := os.Remove(keyPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete key file: %w", err)
	}

	h.auditLogger.LogTransfer("KEY_DELETED", keyID, "system", 0, "Key deleted from HSM")
	return nil
}

// Private helper methods
func (h *SoftwareHSM) loadKeys() error {
	if h.keyStorePath == "" {
		return nil
	}

	files, err := os.ReadDir(h.keyStorePath)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(h.keyStorePath, 0700)
		}
		return err
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		keyPath := filepath.Join(h.keyStorePath, file.Name())
		keyData, err := os.ReadFile(keyPath)
		if err != nil {
			continue
		}

		// Decrypt key data
		decrypted, err := h.decryptWithMasterKey(keyData)
		if err != nil {
			continue
		}

		// Parse key pair
		var keyPair KeyPair
		if err := json.Unmarshal(decrypted, &keyPair); err != nil {
			continue
		}

		h.keys[keyPair.ID] = &keyPair
	}

	return nil
}

func (h *SoftwareHSM) saveKeyToDisk(keyPair *KeyPair) error {
	if h.keyStorePath == "" {
		return nil
	}

	// Serialize key pair
	keyData, err := json.Marshal(keyPair)
	if err != nil {
		return err
	}

	// Encrypt with master key
	encrypted, err := h.encryptWithMasterKey(keyData)
	if err != nil {
		return err
	}

	// Validate keyID to prevent path traversal
	if err := validateKeyID(keyPair.ID); err != nil {
		return fmt.Errorf("invalid key ID: %w", err)
	}

	// Save to file using secure path construction
	keyPath := filepath.Join(h.keyStorePath, keyPair.ID+".key")
	return os.WriteFile(keyPath, encrypted, 0600)
}

func (h *SoftwareHSM) encryptWithMasterKey(data []byte) ([]byte, error) {
	block, err := aes.NewCipher(h.masterKey)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	ciphertext := gcm.Seal(nonce, nonce, data, nil)
	return ciphertext, nil
}

func (h *SoftwareHSM) decryptWithMasterKey(data []byte) ([]byte, error) {
	block, err := aes.NewCipher(h.masterKey)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func (h *SoftwareHSM) generateDefaultKeys() error {
	// Generate card signing key
	if _, err := h.GenerateKeyPair("card_signing"); err != nil {
		return err
	}

	// Generate transaction signing key
	if _, err := h.GenerateKeyPair("transaction_signing"); err != nil {
		return err
	}

	// Generate user encryption key
	if _, err := h.GenerateKeyPair("user_encryption"); err != nil {
		return err
	}

	return nil
}

func (h *SoftwareHSM) generateKeyPairInternal(keyID string) (*KeyPair, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	return &KeyPair{
		ID:         keyID,
		PublicKey:  &privateKey.PublicKey,
		PrivateKey: privateKey,
		CreatedAt:  time.Now(),
		ExpiresAt:  time.Now().Add(365 * 24 * time.Hour),
		IsActive:   true,
	}, nil
}

// EncryptCardData encrypts card data to a base64 string
func (h *SoftwareHSM) EncryptCardData(cardData *CardData) (string, error) {
	// Serialize card data
	data, err := json.Marshal(cardData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal card data: %w", err)
	}

	// Encrypt with card encryption key
	encrypted, err := h.EncryptData("user_encryption", data)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt card data: %w", err)
	}

	return base64.StdEncoding.EncodeToString(encrypted), nil
}

// DecryptCardData decrypts base64 encoded card data
func (h *SoftwareHSM) DecryptCardData(encryptedData string) (*CardData, error) {
	// Decode base64
	encrypted, err := base64.StdEncoding.DecodeString(encryptedData)
	if err != nil {
		return nil, fmt.Errorf("invalid encrypted data format: %w", err)
	}

	// Decrypt data
	decrypted, err := h.DecryptData("user_encryption", encrypted)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt card data: %w", err)
	}

	// Unmarshal card data
	var cardData CardData
	if err := json.Unmarshal(decrypted, &cardData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal card data: %w", err)
	}

	return &cardData, nil
}

func deriveKey(password, salt string, keyLen uint32) []byte {
	return argon2.IDKey([]byte(password), []byte(salt), 3, 32*1024, 4, keyLen)
}

// validateKeyID validates key ID to prevent path traversal attacks
func validateKeyID(keyID string) error {
	if keyID == "" {
		return errors.New("key ID cannot be empty")
	}

	// Check for path traversal patterns
	if filepath.IsAbs(keyID) {
		return errors.New("key ID cannot be an absolute path")
	}

	if filepath.Clean(keyID) != keyID {
		return errors.New("key ID contains invalid path elements")
	}

	// Only allow alphanumeric characters, underscores, and hyphens
	validKeyID := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if !validKeyID.MatchString(keyID) {
		return errors.New("key ID contains invalid characters")
	}

	return nil
}
