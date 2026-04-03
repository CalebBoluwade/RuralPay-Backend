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
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/nacl/secretbox"
)

// HSMInterface defines the HSM operations
type HSMInterface interface {
	// generateKeyPairInternal Key Management
	generateKeyPairInternal(keyID string) (*KeyPair, error)
	GetPublicKey(keyID string) (*rsa.PublicKey, error)
	GetPrivateKey(keyID string) (*rsa.PrivateKey, error)
	DeleteKey(keyID string) error
	RotateKeys() error
	GenerateAndSaveKeyPairExternal(keyID string) (*KeyPair, error)

	// EncryptData Encryption/Decryption
	EncryptData(keyID string, plaintext []byte) ([]byte, error)
	DecryptData(keyID string, payload []byte) (string, error)

	// Signing/Verification
	SignData(keyID string, data []byte) ([]byte, error)
	VerifySignature(keyID string, data, signature []byte) (bool, error)

	// SignTransaction Transaction Security
	SignTransaction(transaction *Transaction) (string, error)
	VerifyTransaction(transaction *Transaction, signature string) (bool, error)

	// PIN Operations
	HashPIN(pin string, salt []byte) (string, error)
	VerifyPIN(pin string, hashedPIN string) (bool, error)

	// DecryptPII PII Operations
	DecryptPII(encryptedData string) (string, error)
	EncryptPAN(pan string) (string, error)
	DecryptPAN(encrypted string) (string, error)
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
		return nil, fmt.Errorf("invalid HSM Type: %s", config.HSMType)
	}
}

// NewSoftwareHSM initializes the software-based HSM server.
func NewSoftwareHSM(config Config) (*SoftwareHSM, error) {
	if config.MasterKey == "" {
		return nil, errors.New("master Key Required for software HSM")
	}

	slog.Info("Software HSM Initialized Successfully")

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

// generateKeyPairInternal creates a new RSA key pair
func (hsm *SoftwareHSM) generateKeyPairInternal(keyID string) (*KeyPair, error) {
	hsm.mu.Lock()
	defer hsm.mu.Unlock()

	// Validate keyID to prevent path traversal
	if err := validateKeyID(keyID); err != nil {
		return nil, fmt.Errorf("invalid key ID: %w", err)
	}

	// Check if key already exists
	if _, exists := hsm.keys[keyID]; exists {
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

	hsm.keys[keyID] = keyPair

	// Save to disk
	if err := hsm.saveKeyToDisk(keyPair); err != nil {
		delete(hsm.keys, keyID)
		return nil, fmt.Errorf("failed to save key to disk: %w", err)
	}

	hsm.auditLogger.LogTransfer("KEY_GENERATED", keyID, "system", 0, "New key pair generated")
	return keyPair, nil
}

// GetPublicKey returns the public key in PEM format
func (hsm *SoftwareHSM) GetPublicKey(keyID string) (*rsa.PublicKey, error) {
	hsm.mu.RLock()
	defer hsm.mu.RUnlock()

	// First, check in-memory keys
	keyPair, exists := hsm.keys[keyID]
	if exists {
		if !keyPair.IsActive {
			return nil, fmt.Errorf("key %s is not active", keyID)
		}
		return keyPair.PublicKey, nil
	}

	// If not in memory, try to load from a PEM file (for externally saved keys)
	pemPath := filepath.Join(hsm.keyStorePath, fmt.Sprintf("%s.pem", keyID))
	pemData, err := os.ReadFile(pemPath)
	if err != nil {
		// If file doesn't exist, then the key is truly not found
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("key %s not found in memory or as a PEM file", keyID)
		}
		// For other errors (e.g., permissions), return the error
		return nil, fmt.Errorf("failed to read public key file %s: %w", pemPath, err)
	}

	// Decode the PEM data
	block, _ := pem.Decode(pemData)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("failed to decode PEM block containing public key from %s", pemPath)
	}

	// Parse the key
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse public key from %s: %w", pemPath, err)
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("key in %s is not an RSA public key", pemPath)
	}

	return rsaPub, nil
}

func (hsm *SoftwareHSM) GetPrivateKey(keyID string) (*rsa.PrivateKey, error) {
	hsm.mu.RLock()
	defer hsm.mu.RUnlock()

	if keyPair, exists := hsm.keys[keyID]; exists {
		if !keyPair.IsActive {
			return nil, fmt.Errorf("key %s is not active", keyID)
		}
		return keyPair.PrivateKey, nil
	}

	// Fall back to PEM file (for keys created by GenerateAndSaveKeyPairExternal)
	pemPath := filepath.Join(hsm.keyStorePath, fmt.Sprintf("%s.pem", keyID))
	pemData, err := os.ReadFile(pemPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("key %s not found", keyID)
		}
		return nil, fmt.Errorf("failed to read private key file %s: %w", pemPath, err)
	}
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block for key %s", keyID)
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// EncryptData encrypts data using AES-GCM
func (hsm *SoftwareHSM) EncryptData(keyID string, plaintext []byte) ([]byte, error) {
	hsm.mu.RLock()
	defer hsm.mu.RUnlock()

	_, exists := hsm.keys[keyID]
	if !exists {
		return nil, fmt.Errorf("key %s not found", keyID)
	}

	// Generate a random nonce
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Create AES-GCM cipher
	block, err := aes.NewCipher(hsm.masterKey)
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

// DecryptData decrypts a hybrid-encrypted payload produced by the client.
// Format: [2 bytes LE: RSA key len] + [RSA-OAEP encrypted 32-byte key] + [12 bytes: IV] + [NaCl secretbox ciphertext]
// The client pads the 12-byte IV to 24 bytes (NaCl nonce size) with trailing zeros.
func (hsm *SoftwareHSM) DecryptData(keyID string, payload []byte) (string, error) {
	slog.Debug("hsm.decrypt_data.start", "key_id", keyID, "payload_len", len(payload))

	if len(payload) < 2 {
		return "", fmt.Errorf("payload too short: got %d bytes", len(payload))
	}

	privateKey, err := hsm.GetPrivateKey(keyID)
	if err != nil {
		slog.Error("hsm.decrypt_data.get_private_key_failed", "key_id", keyID, "error", err)
		return "", fmt.Errorf("failed to get private key: %w", err)
	}
	slog.Debug("hsm.decrypt_data.private_key_loaded", "key_id", keyID, "key_size_bits", privateKey.Size()*8)

	// Extract the RSA-encrypted key length (first 2 bytes, little-endian)
	rsaKeyLen := int(binary.LittleEndian.Uint16(payload[:2]))
	offset := 2
	slog.Debug("hsm.decrypt_data.envelope", "rsa_key_len", rsaKeyLen, "total_payload_len", len(payload))

	if offset+rsaKeyLen+12 > len(payload) {
		return "", fmt.Errorf("payload too short: need %d bytes, got %d", offset+rsaKeyLen+12, len(payload))
	}

	// Decrypt the wrapped symmetric key with RSA-OAEP (nil label = WebCrypto default)
	encryptedKey := payload[offset : offset+rsaKeyLen]
	offset += rsaKeyLen

	keyBytes, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privateKey, encryptedKey, nil)
	if err != nil {
		slog.Error("hsm.decrypt_data.rsa_oaep_failed", "key_id", keyID, "encrypted_key_len", len(encryptedKey), "error", err)
		return "", fmt.Errorf("RSA decryption failed: %w", err)
	}
	if len(keyBytes) != 32 {
		slog.Error("hsm.decrypt_data.invalid_key_len", "got", len(keyBytes))
		return "", fmt.Errorf("decrypted key has unexpected length %d, want 32", len(keyBytes))
	}
	slog.Debug("hsm.decrypt_data.key_decrypted", "key_len", len(keyBytes))

	// Build the 24-byte NaCl nonce: 12-byte IV from payload + 12 zero bytes (matches client padding)
	var nonce [24]byte
	copy(nonce[:12], payload[offset:offset+12])
	offset += 12

	ciphertext := payload[offset:]
	slog.Debug("hsm.decrypt_data.secretbox", "ciphertext_len", len(ciphertext))

	var secretKey [32]byte
	copy(secretKey[:], keyBytes)

	plaintext, ok := secretbox.Open(nil, ciphertext, &nonce, &secretKey)
	if !ok {
		slog.Error("hsm.decrypt_data.secretbox_open_failed", "ciphertext_len", len(ciphertext))
		return "", fmt.Errorf("secretbox decryption failed: authentication tag mismatch")
	}

	slog.Debug("hsm.decrypt_data.success", "plaintext_len", len(plaintext))
	return string(plaintext), nil
}

// SignData signs data with RSA private key
func (hsm *SoftwareHSM) SignData(keyID string, data []byte) ([]byte, error) {
	hsm.mu.RLock()
	defer hsm.mu.RUnlock()

	keyPair, exists := hsm.keys[keyID]
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
func (hsm *SoftwareHSM) VerifySignature(keyID string, data, signature []byte) (bool, error) {
	hsm.mu.RLock()
	defer hsm.mu.RUnlock()

	keyPair, exists := hsm.keys[keyID]
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

// SignTransaction signs a transaction
func (hsm *SoftwareHSM) SignTransaction(transaction *Transaction) (string, error) {
	// Create data to sign
	data, err := json.Marshal(transaction)
	if err != nil {
		return "", fmt.Errorf("failed to marshal transaction for signing: %w", err)
	}
	// Sign with transaction key
	signature, err := hsm.SignData("transaction_signing", []byte(data))
	if err != nil {
		return "", fmt.Errorf("failed to sign transaction: %w", err)
	}

	return base64.StdEncoding.EncodeToString(signature), nil
}

// VerifyTransaction verifies transaction signature
func (hsm *SoftwareHSM) VerifyTransaction(transaction *Transaction, signature string) (bool, error) {
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
	if valid, _ := hsm.VerifySignature("transaction_signing", []byte(data), sigBytes); valid {
		return true, nil
	}

	// If primary key fails, try archived keys (fallback for rotated keys)
	var archivedKeys []string
	hsm.mu.RLock()
	for k := range hsm.keys {
		if strings.HasPrefix(k, "transaction_signing_") {
			archivedKeys = append(archivedKeys, k)
		}
	}
	hsm.mu.RUnlock()

	for _, keyID := range archivedKeys {
		if valid, _ := hsm.VerifySignature(keyID, []byte(data), sigBytes); valid {
			return true, nil
		}
	}

	return false, nil
}

// EncryptPAN encrypts a plaintext PAN for storage using AES-GCM with the master key.
func (hsm *SoftwareHSM) EncryptPAN(pan string) (string, error) {
	block, err := aes.NewCipher(hsm.masterKey)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(pan), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptPAN decrypts a PAN that was encrypted with EncryptPAN.
func (hsm *SoftwareHSM) DecryptPAN(encrypted string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", fmt.Errorf("invalid base64: %w", err)
	}
	block, err := aes.NewCipher(hsm.masterKey)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	plaintext, err := gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("decryption failed: %w", err)
	}
	return string(plaintext), nil
}

// DecryptPII Decrypts PII Data
func (hsm *SoftwareHSM) DecryptPII(encryptedData string) (string, error) {
	slog.Debug("hsm.decrypt_pii.start", "encoded_len", len(encryptedData))

	payload, err := base64.StdEncoding.DecodeString(encryptedData)
	if err != nil {
		slog.Error("hsm.decrypt_pii.base64_failed", "error", err)
		return "", fmt.Errorf("invalid Base64 PII Data: %w", err)
	}
	slog.Debug("hsm.decrypt_pii.decoded", "payload_len", len(payload))

	decrypted, err := hsm.DecryptData("app_signing_private", payload)
	if err != nil {
		slog.Error("hsm.decrypt_pii.failed", "error", err)
		return "", fmt.Errorf("failed to Decrypt PII: %w", err)
	}

	slog.Debug("hsm.decrypt_pii.success")
	return decrypted, nil
}

// HashPIN hashes a PIN using Argon2
func (hsm *SoftwareHSM) HashPIN(pin string, salt []byte) (string, error) {
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
func (hsm *SoftwareHSM) VerifyPIN(pin string, hashedPIN string) (bool, error) {
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
func (hsm *SoftwareHSM) RotateKeys() error {
	hsm.mu.Lock()
	defer hsm.mu.Unlock()

	// Collect keys to rotate first to avoid modifying map during iteration
	var rotated []string
	var keysToRotate []string
	now := time.Now()

	for keyID, keyPair := range hsm.keys {
		// Only rotate base keys (not archived ones) that are expired or inactive
		if (now.After(keyPair.ExpiresAt) || !keyPair.IsActive) && !strings.Contains(keyID, "_1") {
			keysToRotate = append(keysToRotate, keyID)
		}
	}

	for _, keyID := range keysToRotate {
		oldKeyPair := hsm.keys[keyID]

		// 1. Archive the old key
		archiveID := fmt.Sprintf("%s_%d", keyID, now.Unix())
		archivedKey := *oldKeyPair // Shallow copy
		archivedKey.ID = archiveID
		archivedKey.IsActive = false // Archived keys cannot be used for new signing
		hsm.keys[archiveID] = &archivedKey
		hsm.saveKeyToDisk(&archivedKey)

		// 2. Generate new key for the base ID
		newKeyPair, err := hsm.generateKeyPairInternal(keyID)
		if err != nil {
			hsm.auditLogger.LogError(keyID, keyID, err)
			continue
		}
		hsm.keys[keyID] = newKeyPair
		hsm.saveKeyToDisk(newKeyPair)

		rotated = append(rotated, keyID)

		hsm.auditLogger.LogTransfer("KEY_ROTATED", keyID, archiveID, 0, "Key rotated")
	}

	hsm.auditLogger.LogTransfer("KEY_ROTATION_COMPLETE", "system", "system", int64(len(rotated)), "Key rotation complete")

	return nil
}

// DeleteKey removes a key from the HSM
func (hsm *SoftwareHSM) DeleteKey(keyID string) error {
	hsm.mu.Lock()
	defer hsm.mu.Unlock()

	if _, exists := hsm.keys[keyID]; !exists {
		return fmt.Errorf("key %s not found", keyID)
	}

	// Delete from memory
	delete(hsm.keys, keyID)

	// Validate keyID to prevent path traversal
	if err := validateKeyID(keyID); err != nil {
		return fmt.Errorf("invalid key ID: %w", err)
	}

	// Delete from disk using secure path construction
	keyPath := filepath.Join(hsm.keyStorePath, keyID+".key")
	if err := os.Remove(keyPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete key file: %w", err)
	}

	hsm.auditLogger.LogTransfer("KEY_DELETED", keyID, "system", 0, "Key deleted from HSM")
	return nil
}

// Private helper methods
func (hsm *SoftwareHSM) loadKeys() error {
	if hsm.keyStorePath == "" {
		return nil
	}

	files, err := os.ReadDir(hsm.keyStorePath)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(hsm.keyStorePath, 0700)
		}
		return err
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		keyPath := filepath.Join(hsm.keyStorePath, file.Name())
		keyData, err := os.ReadFile(keyPath)
		if err != nil {
			continue
		}

		// Decrypt key data
		decrypted, err := hsm.decryptWithMasterKey(keyData)
		if err != nil {
			continue
		}

		// Parse key pair
		var keyPair KeyPair
		if err := json.Unmarshal(decrypted, &keyPair); err != nil {
			continue
		}

		hsm.keys[keyPair.ID] = &keyPair
	}

	return nil
}

func (hsm *SoftwareHSM) saveKeyToDisk(keyPair *KeyPair) error {
	if hsm.keyStorePath == "" {
		return nil
	}

	// Serialize key pair
	keyData, err := json.Marshal(keyPair)
	if err != nil {
		return err
	}

	// Encrypt with master key
	encrypted, err := hsm.encryptWithMasterKey(keyData)
	if err != nil {
		return err
	}

	// Validate keyID to prevent path traversal
	if err := validateKeyID(keyPair.ID); err != nil {
		return fmt.Errorf("invalid key ID: %w", err)
	}

	// Save to file using secure path construction
	keyPath := filepath.Join(hsm.keyStorePath, keyPair.ID+".key")
	return os.WriteFile(keyPath, encrypted, 0600)
}

func (hsm *SoftwareHSM) encryptWithMasterKey(data []byte) ([]byte, error) {
	block, err := aes.NewCipher(hsm.masterKey)
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

func (hsm *SoftwareHSM) decryptWithMasterKey(data []byte) ([]byte, error) {
	block, err := aes.NewCipher(hsm.masterKey)
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

func (hsm *SoftwareHSM) generateDefaultKeys() error {
	// Generate card signing key
	if _, err := hsm.generateKeyPairInternal("card_signing"); err != nil {
		return err
	}

	// Generate transaction signing key
	if _, err := hsm.generateKeyPairInternal("transaction_signing"); err != nil {
		return err
	}

	return nil
}

func (hsm *SoftwareHSM) GenerateAndSaveKeyPairExternal(keyID string) (*KeyPair, error) {
	slog.Info(fmt.Sprintf("Generating %d-bit RSA key pair...\n", 2048))
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	// 2. Save Private Key to file (private.pem)
	// PKCS1 is the standard format for RSA Private Keys
	privPath := filepath.Join(hsm.keyStorePath, fmt.Sprintf("%s_private.pem", keyID))
	privateKeyFile, err := os.OpenFile(privPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	defer privateKeyFile.Close()

	privateKeyBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}
	if err := pem.Encode(privateKeyFile, privateKeyBlock); err != nil {
		return nil, err
	}

	// 3. Save Public Key to file (public.pem)
	// PKIX is the standard format for RSA Public Keys
	pubPath := filepath.Join(hsm.keyStorePath, fmt.Sprintf("%s_public.pem", keyID))
	pubFile, err := os.OpenFile(pubPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	defer pubFile.Close()

	// Use MarshalPKIXPublicKey for compatibility with WebCrypto (SPKI)
	pubBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, err
	}

	pubBlock := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	}
	if err := pem.Encode(pubFile, pubBlock); err != nil {
		return nil, err
	}

	slog.Info(fmt.Sprintf("Success! Keys saved to %s and %s", privPath, pubPath))

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
func (hsm *SoftwareHSM) EncryptCardData(cardData *CardData) (string, error) {
	// Serialize card data
	data, err := json.Marshal(cardData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal card data: %w", err)
	}

	// Encrypt with card encryption key
	encrypted, err := hsm.EncryptData("user_encryption", data)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt card data: %w", err)
	}

	return base64.StdEncoding.EncodeToString(encrypted), nil
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
